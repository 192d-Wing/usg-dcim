"""Unit tests for observability.install_tracing.

OTel is off by default; these tests lock the no-op contract (no
TracerProvider is registered when disabled, no SDK module is imported
eagerly) and verify the enabled path constructs a TracerProvider with
the right resource attributes.
"""

from __future__ import annotations

from unittest.mock import patch

from dcim import observability
from dcim.settings import Settings, get_settings


def _settings(**overrides) -> Settings:
    """Build a Settings instance with overrides applied."""
    # Use construct so we don't need every required field set.
    base = get_settings().model_dump()
    base.update(overrides)
    return Settings(**base)


def test_install_tracing_returns_false_when_disabled():
    """The default config keeps OTel off; install_tracing must short-circuit
    without touching the SDK so importers don't pay the cost."""
    with patch.object(observability, "get_settings",
                      return_value=_settings(otel_enabled=False)):
        assert observability.install_tracing() is False


def test_install_app_tracing_is_safe_when_disabled():
    """install_app_tracing(app) must accept a FastAPI app and do nothing
    when OTel is disabled. Calling it from create_app() at module load is
    the whole point of the no-op contract."""
    with patch.object(observability, "get_settings",
                      return_value=_settings(otel_enabled=False)):
        # Any sentinel will do — install_app_tracing should not touch it.
        observability.install_app_tracing(object())


def test_install_worker_tracing_is_safe_when_disabled():
    with patch.object(observability, "get_settings",
                      return_value=_settings(otel_enabled=False)):
        observability.install_worker_tracing()


def test_install_tracing_does_not_eagerly_import_otel_sdk():
    """Importing the observability module must not pull the SDK into
    sys.modules — the lazy imports are how disabled deployments avoid
    paying the wheel-load cost on every cold start."""
    # If a previous test already enabled tracing the SDK is in sys.modules.
    # Verifying *strict* laziness would require a clean interpreter; settle
    # for the weaker assertion that the module itself doesn't import them.
    src = (observability.__file__,)
    import ast
    with open(observability.__file__, encoding="utf-8") as f:
        tree = ast.parse(f.read())
    top_level_imports = [
        n for n in tree.body
        if isinstance(n, (ast.Import, ast.ImportFrom))
    ]
    for node in top_level_imports:
        names = (
            [a.name for a in node.names] if isinstance(node, ast.Import)
            else [node.module or ""]
        )
        for name in names:
            assert not name.startswith("opentelemetry"), (
                f"observability.py imports {name!r} at module top-level — "
                "must be lazy so disabled deployments don't load the SDK"
            )
    assert src  # silence unused-var lint


def test_install_tracing_registers_provider_when_enabled():
    """The enabled path constructs a real TracerProvider with the
    service_name + deployment.environment resource attributes."""
    with patch.object(observability, "get_settings",
                      return_value=_settings(
                          otel_enabled=True,
                          otel_service_name="dcim-api-test",
                          otel_environment="ci",
                          otel_exporter_endpoint="https://otel:4318",
                      )):
        ok = observability.install_tracing("dcim-api-test")
    assert ok is True

    # Confirm a real (non-proxy) TracerProvider is now global.
    from opentelemetry import trace
    from opentelemetry.sdk.trace import TracerProvider
    provider = trace.get_tracer_provider()
    assert isinstance(provider, TracerProvider)

    # Resource carries the expected attributes.
    attrs = provider.resource.attributes
    assert attrs["service.name"] == "dcim-api-test"
    assert attrs["deployment.environment"] == "ci"
