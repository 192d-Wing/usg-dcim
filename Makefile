.PHONY: help sync up down logs build migrate seed test lint fmt collector finch frontend otter backend worker clean

help:
	@echo "USG DCIM dev tasks (Python via uv)"
	@echo "  make sync         install backend + collector deps into .venv"
	@echo "  make up           start full stack via podman compose"
	@echo "  make down         stop stack"
	@echo "  make logs         tail all service logs"
	@echo "  make migrate      apply Alembic migrations against running stack"
	@echo "  make seed         load demo enterprise dataset"
	@echo "  make test         backend pytest + frontend vitest"
	@echo "  make lint         ruff + eslint"
	@echo "  make fmt          ruff format + prettier"
	@echo "  make otter        run API (otter) locally on :8000   [alias: backend]"
	@echo "  make worker       run arq worker locally"
	@echo "  make collector    run sample collector (mole, deprecated) locally"
	@echo "  make finch        vite dev server (finch) on :5173    [alias: frontend]"

sync:
	uv sync --all-packages --all-extras

up:
	podman compose -f infra/docker/docker-compose.yml up -d --build

down:
	podman compose -f infra/docker/docker-compose.yml down

logs:
	podman compose -f infra/docker/docker-compose.yml logs -f

build:
	podman compose -f infra/docker/docker-compose.yml build

migrate:
	podman compose -f infra/docker/docker-compose.yml exec api alembic upgrade head

seed:
	podman compose -f infra/docker/docker-compose.yml exec api python -m dcim.scripts.seed_demo

migrate-local:
	uv run --project packages/otter alembic -c packages/otter/alembic.ini upgrade head

seed-local:
	uv run --project packages/otter python -m dcim.scripts.seed_demo

test:
	uv run --project packages/otter pytest packages/otter/tests -q
	cd packages/finch && npm test --silent

lint:
	uv run --project packages/otter ruff check packages/otter
	cd packages/finch && npm run lint

fmt:
	uv run --project packages/otter ruff format packages/otter
	cd packages/finch && npm run fmt

otter:
	uv run --project packages/otter uvicorn dcim.main:app --reload --port 8000 --host 0.0.0.0

backend: otter

worker:
	uv run --project packages/otter arq dcim.worker.WorkerSettings

collector:
	uv run --project packages/mole dcim-collector --config packages/mole/sample-config.yaml

finch:
	cd packages/finch && npm install && npm run dev

frontend: finch

clean:
	rm -rf .venv packages/otter/.pytest_cache packages/otter/.ruff_cache packages/otter/.mypy_cache
	rm -rf packages/finch/node_modules packages/finch/dist
