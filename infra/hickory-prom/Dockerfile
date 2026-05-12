# Custom Hickory DNS image built with the `prometheus-metrics`
# feature enabled, plus `dnssec-ring` so the recursive can validate
# signed answers it forwards on behalf of (i.e. the CoreDNS auth pod
# upstream).
#
# Why this image exists: the upstream `hickorydns/hickory-dns:latest`
# binary is built with default features only. That excludes the
# prometheus exporter, so a stock Hickory recursive listens only on
# port 53 and exposes no observability surface — the collector can't
# scrape QPS, error rates, or latency. Building from source with the
# feature flag enabled restores the same data shape the CoreDNS
# `prometheus` plugin gives us on the auth side, so the same
# collector code path covers both engines.
#
# The output image is a drop-in for `hickorydns/hickory-dns:<version>`
# — same entrypoint, same flags, same listening ports — plus a
# prometheus listener at `prometheus_listen_addr` (set in the
# rendered `config.toml`).

ARG HICKORY_VERSION=v0.26.0
ARG RUST_VERSION=1.88

# ---------- builder stage -----------------------------------------------------

FROM rust:${RUST_VERSION}-bookworm AS builder

ARG HICKORY_VERSION

WORKDIR /src

# Clone Hickory at the pinned tag. --depth=1 keeps the image build
# fast; we never need its full history.
RUN git clone --depth=1 --branch ${HICKORY_VERSION} https://github.com/hickory-dns/hickory-dns.git

WORKDIR /src/hickory-dns

# Build the bin with prometheus-metrics + dnssec-ring on top of the
# default feature set (sqlite, resolver, rustls-platform-verifier).
#
#   prometheus-metrics : exposes /metrics for the collector to scrape
#   dnssec-ring        : validates RRSIGs on forwarded answers, so a
#                        recursive that forwards to a signed auth zone
#                        actually catches tampering
#
# We deliberately don't enable __tls / __https / __quic — none of the
# site stack's DoT/DoH paths are wired today, and pulling rustls in
# bloats the binary by ~5 MB for unused code.
RUN cargo build --release --bin hickory-dns \
    --features "prometheus-metrics dnssec-ring"

# ---------- runtime stage -----------------------------------------------------

# Distroless so the image surface matches the upstream `hickorydns/`
# build — operators can swap image references without revisiting
# anything else in the compose / k8s manifests. Non-root user is
# baked into the distroless image (uid 65532).
FROM gcr.io/distroless/cc-debian12:nonroot

COPY --from=builder /src/hickory-dns/target/release/hickory-dns /hickory-dns

EXPOSE 53 53/udp
ENTRYPOINT ["/hickory-dns"]
