"""OpenTelemetry tracing setup.

Off by default; ops enables via `settings.otel_enabled`. When enabled,
`install_tracing(service_name)` configures a global TracerProvider with
an OTLP/HTTP exporter pointed at `settings.otel_exporter_endpoint` and
instruments FastAPI + asyncpg + httpx. The api and worker call
`install_app_tracing(app)` / `install_worker_tracing()` respectively so
each process advertises a distinct `service.name`.

`install_*` is a no-op when `otel_enabled` is False, so importing this
module — and calling `install_*` unconditionally from startup — has zero
runtime cost when traces are off. The opentelemetry SDK packages are
imported lazily for the same reason: the import graph stays clean for
deployments that never enable tracing.
"""

from __future__ import annotations

from .settings import get_settings


def install_tracing(service_name: str | None = None) -> bool:
    """Configure the global TracerProvider. Returns True iff enabled.

    Called from both api startup and worker startup. Safe to call more
    than once — the second call is a no-op (opentelemetry's
    set_tracer_provider warns if a non-default provider is replaced, so
    we check first).
    """
    s = get_settings()
    if not s.otel_enabled:
        return False

    # Lazy imports so a deployment that never enables OTel doesn't pay
    # the import cost of the SDK + exporter wheels.
    from opentelemetry import trace
    from opentelemetry.exporter.otlp.proto.http.trace_exporter import (
        OTLPSpanExporter,
    )
    from opentelemetry.sdk.resources import Resource
    from opentelemetry.sdk.trace import TracerProvider
    from opentelemetry.sdk.trace.export import BatchSpanProcessor

    # The DefaultTracerProvider is a ProxyTracerProvider; only replace
    # it once. Subsequent install_tracing() calls — e.g. worker reload
    # — would otherwise stack BatchSpanProcessors and double-emit.
    current = trace.get_tracer_provider()
    if isinstance(current, TracerProvider):
        return True

    resource = Resource.create({
        "service.name": service_name or s.otel_service_name,
        "service.version": "0.1.0",
        "deployment.environment": s.otel_environment,
    })
    provider = TracerProvider(resource=resource)
    # `/v1/traces` is appended by OTLPSpanExporter when endpoint ends at
    # the collector's base URL.
    endpoint = s.otel_exporter_endpoint.rstrip("/") + "/v1/traces"
    provider.add_span_processor(
        BatchSpanProcessor(OTLPSpanExporter(endpoint=endpoint))
    )
    trace.set_tracer_provider(provider)
    return True


def install_app_tracing(app) -> None:
    """Instrument a FastAPI app + the shared httpx and asyncpg clients.

    The FastAPI instrumentation hooks into the ASGI lifecycle to emit
    one span per request. httpx and asyncpg are instrumented globally
    (their client classes are patched in place), so every call site
    picks up spans without needing a code change.
    """
    if not install_tracing(get_settings().otel_service_name):
        return
    from opentelemetry.instrumentation.asyncpg import AsyncPGInstrumentor
    from opentelemetry.instrumentation.fastapi import FastAPIInstrumentor
    from opentelemetry.instrumentation.httpx import HTTPXClientInstrumentor

    FastAPIInstrumentor.instrument_app(app)
    AsyncPGInstrumentor().instrument()
    HTTPXClientInstrumentor().instrument()


def install_worker_tracing(service_name: str = "dcim-worker") -> None:
    """Instrument the arq worker process — asyncpg + httpx only, no FastAPI."""
    if not install_tracing(service_name):
        return
    from opentelemetry.instrumentation.asyncpg import AsyncPGInstrumentor
    from opentelemetry.instrumentation.httpx import HTTPXClientInstrumentor

    AsyncPGInstrumentor().instrument()
    HTTPXClientInstrumentor().instrument()
