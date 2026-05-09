"""Runtime configuration. All values overridable via environment."""

from functools import lru_cache
from typing import Literal

from pydantic import Field, PostgresDsn, RedisDsn
from pydantic_settings import BaseSettings, SettingsConfigDict


class Settings(BaseSettings):
    model_config = SettingsConfigDict(env_prefix="DCIM_", env_file=".env", extra="ignore")

    env: Literal["dev", "staging", "prod"] = "dev"
    log_level: str = "INFO"
    log_json: bool = True

    # Network
    api_host: str = "0.0.0.0"
    api_port: int = 8000
    cors_origins: list[str] = Field(default_factory=lambda: ["http://localhost:5173"])

    # Datastores
    postgres_dsn: PostgresDsn = Field(
        default="postgresql+asyncpg://dcim:dcim@postgres:5432/dcim"  # type: ignore[arg-type]
    )
    redis_dsn: RedisDsn = Field(default="redis://redis:6379/0")  # type: ignore[arg-type]
    elastic_url: str = "http://elastic:9200"
    elastic_username: str | None = None
    elastic_password: str | None = None

    # Auth
    jwt_secret: str = "change-me-in-prod"
    jwt_algorithm: str = "HS256"
    jwt_ttl_seconds: int = 3600
    oidc_issuer: str | None = None
    oidc_client_id: str | None = None
    oidc_client_secret: str | None = None
    # Where the issuer should redirect after the user authenticates. Must
    # match the client config in Keycloak/AzureAD. Optional override per
    # request via the `redirect_uri` query param.
    oidc_redirect_uri: str | None = None
    saml_metadata_url: str | None = None

    # Collector ingest
    collector_mtls_required: bool = True
    collector_ca_bundle: str | None = None
    collector_token_secret: str = "change-me-in-prod"
    collector_stale_seconds: int = 600

    # Telemetry
    telemetry_index_prefix: str = "dcim-telemetry"
    events_index_prefix: str = "dcim-events"
    rollup_index_prefix: str = "dcim-rollup"
    telemetry_batch_max: int = 5000

    # Alerting
    alert_eval_interval_seconds: int = 30
    alert_dedupe_window_seconds: int = 300

    # Object storage
    s3_endpoint: str | None = None
    s3_bucket: str = "dcim-reports"
    s3_access_key: str | None = None
    s3_secret_key: str | None = None

    # Outbound email (notifications). Leave host unset to disable email channels.
    smtp_host: str | None = None
    smtp_port: int = 587
    smtp_username: str | None = None
    smtp_password: str | None = None
    smtp_sender: str = "dcim-alerts@example.org"


@lru_cache
def get_settings() -> Settings:
    return Settings()
