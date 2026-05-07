"""Collector entrypoint. Schedules per-device pollers and drains the buffer."""

from __future__ import annotations

import asyncio
import signal

import click
import structlog

from .buffer import Buffer
from .config import CollectorConfig
from .drivers import load_driver
from .forwarder import Forwarder

log = structlog.get_logger("collector.main")


async def _device_loop(device, buffer: Buffer) -> None:
    driver = load_driver(device.driver)
    while True:
        try:
            samples = await driver.poll(device)
            if samples:
                await buffer.enqueue_many(samples)
                log.info("polled", driver=device.driver, asset=str(device.asset_id), n=len(samples))
        except asyncio.CancelledError:
            raise
        except Exception as e:  # noqa: BLE001
            log.warning("device_poll_failed", asset=str(device.asset_id), err=str(e))
        await asyncio.sleep(device.poll_interval_seconds)


async def _drain_loop(fwd: Forwarder) -> None:
    while True:
        try:
            n = await fwd.drain_once()
            if n == 0:
                await asyncio.sleep(2)
        except asyncio.CancelledError:
            raise
        except Exception as e:  # noqa: BLE001
            log.warning("drain_failed_will_retry", err=str(e))
            await asyncio.sleep(5)


async def _heartbeat_loop(fwd: Forwarder, interval: int) -> None:
    while True:
        try:
            await fwd.heartbeat()
        except asyncio.CancelledError:
            raise
        except Exception as e:  # noqa: BLE001
            log.warning("heartbeat_failed", err=str(e))
        await asyncio.sleep(interval)


async def run(config_path: str) -> None:
    cfg = CollectorConfig.load(config_path)
    log.info("collector_start", site=str(cfg.site_id), devices=len(cfg.devices))

    buffer = Buffer(cfg.buffer_path)
    await buffer.open()
    fwd = Forwarder(cfg, buffer)

    tasks = [asyncio.create_task(_device_loop(d, buffer)) for d in cfg.devices]
    tasks.append(asyncio.create_task(_drain_loop(fwd)))
    tasks.append(asyncio.create_task(_heartbeat_loop(fwd, cfg.heartbeat_interval_seconds)))

    stop = asyncio.Event()
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(sig, stop.set)
        except NotImplementedError:  # Windows
            pass

    await stop.wait()
    log.info("collector_shutdown_initiated")
    for t in tasks:
        t.cancel()
    await asyncio.gather(*tasks, return_exceptions=True)
    await buffer.close()
    log.info("collector_shutdown_complete")


@click.command()
@click.option("--config", "config_path", required=True, help="Path to collector YAML config")
def cli(config_path: str) -> None:
    """Run the DCIM site collector."""
    asyncio.run(run(config_path))


if __name__ == "__main__":
    cli()
