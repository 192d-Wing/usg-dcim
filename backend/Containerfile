FROM ghcr.io/astral-sh/uv:0.5-python3.12-bookworm-slim AS build
ENV UV_LINK_MODE=copy UV_COMPILE_BYTECODE=1 UV_PYTHON_DOWNLOADS=never
WORKDIR /app
COPY pyproject.toml ./
COPY src ./src
RUN --mount=type=cache,target=/root/.cache/uv uv venv && uv pip install ".[dev]"

FROM python:3.12-slim AS runtime
ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1 PATH="/app/.venv/bin:$PATH"
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends \
        libpq5 ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
# Bring in the installed package + alembic config. Migrations live inside the package.
COPY --from=build /app/.venv /app/.venv
COPY alembic.ini ./
# Re-link the package's migrations into the alembic.ini script_location path.
COPY src/dcim/migrations ./src/dcim/migrations
EXPOSE 8000
CMD ["uvicorn", "dcim.main:app", "--host", "0.0.0.0", "--port", "8000"]
