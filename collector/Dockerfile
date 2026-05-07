FROM ghcr.io/astral-sh/uv:0.5-python3.12-bookworm-slim AS build
ENV UV_LINK_MODE=copy UV_COMPILE_BYTECODE=1 UV_PYTHON_DOWNLOADS=never
WORKDIR /app
COPY pyproject.toml ./
COPY src ./src
RUN --mount=type=cache,target=/root/.cache/uv uv venv && uv pip install ".[all]"

FROM python:3.12-slim AS runtime
ENV PYTHONUNBUFFERED=1 PATH="/app/.venv/bin:$PATH"
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends \
        libsnmp40 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /app/.venv /app/.venv
RUN mkdir -p /var/lib/dcim-collector
VOLUME ["/var/lib/dcim-collector"]
ENTRYPOINT ["dcim-collector"]
CMD ["--config", "/etc/dcim/collector.yaml"]
