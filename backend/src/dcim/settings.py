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
    # Zero-trust posture: short JWT TTL so an IdP role revocation
    # propagates within this window. 15 minutes is the tradeoff between
    # security (faster propagation) and UX (less-frequent re-auth).
    jwt_ttl_seconds: int = 900
    # When true, /auth/login (form login with email+password) returns 403.
    # admin@dcim.local remains in the DB for break-glass via the API token
    # path if the seed creates one. Flip on in any deployment with SSO.
    local_login_disabled: bool = False
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

    # AS number used as the originating AS for all DNS recursive
    # anycast announcements. Renders into every recursive server's
    # GoBGP `global.config.as` so the leaf/ToR sees the same origin AS
    # regardless of which site the announcement comes from. 4200000000
    # is a 4-byte private ASN (RFC 6996).
    dns_anycast_originate_asn: int = 4_200_000_000

    # Emit `prometheus_listen_addr` into rendered Hickory configs so
    # the resolver exposes /metrics for the collector to scrape. The
    # site-dns Hickory overlay references our custom `hickory-prom`
    # image (built with --features prometheus-metrics) by default, so
    # this is on out of the box. Operators who point HICKORY_IMAGE
    # at the upstream `hickorydns/hickory-dns` build MUST flip this
    # off — the upstream binary doesn't recognize the field and
    # would crash on TOML parse.
    dns_hickory_prom_metrics: bool = True
    dns_hickory_prom_port: int = 9090

    # Emit a `dnstap` directive in the CoreDNS auth Corefile so every
    # query lands on a UNIX socket the collector tails. Powers the
    # per-name top-K reservoir behind the dashboard's "Top queried
    # names" widget. Hickory doesn't support dnstap at all, so this
    # only affects CoreDNS auth pods. Off by default — turning it on
    # requires the collector to be running a version that reads
    # the socket (otherwise the file fills up on the shared volume).
    dns_dnstap_enabled: bool = True
    dns_dnstap_socket_filename: str = "dnstap.sock"

    # DNSSEC private-key encryption secret. When set, DnsKey.private_pem
    # is encrypted at rest with Fernet (AES-128-CBC + HMAC-SHA-256).
    # Generate with `python -c "from cryptography.fernet import Fernet;
    # print(Fernet.generate_key().decode())"`. Optional in dev; required
    # in any environment that holds production zone keys — leave unset
    # and DnsKey rows fall back to plaintext (with a warning at write
    # time). Existing plaintext rows are lazily re-encrypted on next use.
    dns_dnssec_secret: str | None = None

    # How long to keep CoreDNS metric samples in
    # dns_server_metrics_samples. The retention cron walks every hour
    # and drops anything older than this window. Default 14 days
    # balances chart depth against table size — a busy stack with
    # 60-second scrapes and 5 servers produces ~36k rows per fortnight.
    dns_metrics_retention_days: int = 14

    # Default algorithm picked when an operator clicks Enable DNSSEC
    # without specifying one. ECDSAP256SHA256 is short-key,
    # widely-supported, and recommended by RFC 8624 — switch to
    # ed25519 if the resolver fleet has been validated.
    dns_dnssec_default_algorithm: Literal[
        "ecdsap256sha256", "ed25519", "rsasha256",
    ] = "ecdsap256sha256"

    # Whether the IPAM → DNS projector projects DHCP-sourced
    # IPAddress rows. Operators who don't want lease churn driving
    # DNS can flip this off and let the projector handle only
    # static IPAM rows (source=ipam).
    dns_ddns_enabled: bool = True

    # Catch-all upstreams the recursive Corefile forwards to when no
    # conditional forwarder or apex stub matches. Operators with an
    # internal recursive (e.g. Active Directory DNS) override this
    # to keep DNS off the public internet.
    dns_recursive_upstreams: list[str] = Field(
        default_factory=lambda: ["1.1.1.1", "8.8.8.8"],
    )


@lru_cache
def get_settings() -> Settings:
    return Settings()
