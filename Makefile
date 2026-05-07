.PHONY: help sync up down logs build migrate seed test lint fmt collector frontend backend worker clean

help:
	@echo "USG DCIM dev tasks (Python via uv)"
	@echo "  make sync         install backend + collector deps into .venv"
	@echo "  make up           start full stack via docker-compose"
	@echo "  make down         stop stack"
	@echo "  make logs         tail all service logs"
	@echo "  make migrate      apply Alembic migrations against running stack"
	@echo "  make seed         load demo enterprise dataset"
	@echo "  make test         backend pytest + frontend vitest"
	@echo "  make lint         ruff + eslint"
	@echo "  make fmt          ruff format + prettier"
	@echo "  make backend      run API locally on :8000"
	@echo "  make worker       run arq worker locally"
	@echo "  make collector    run sample collector locally"
	@echo "  make frontend     vite dev server on :5173"

sync:
	uv sync --all-packages --all-extras

up:
	docker compose -f infra/docker/docker-compose.yml up -d --build

down:
	docker compose -f infra/docker/docker-compose.yml down

logs:
	docker compose -f infra/docker/docker-compose.yml logs -f

build:
	docker compose -f infra/docker/docker-compose.yml build

migrate:
	docker compose -f infra/docker/docker-compose.yml exec api alembic upgrade head

seed:
	docker compose -f infra/docker/docker-compose.yml exec api python -m dcim.scripts.seed_demo

migrate-local:
	uv run --project backend alembic -c backend/alembic.ini upgrade head

seed-local:
	uv run --project backend python -m dcim.scripts.seed_demo

test:
	uv run --project backend pytest backend/tests -q
	cd frontend && npm test --silent

lint:
	uv run --project backend ruff check backend
	cd frontend && npm run lint

fmt:
	uv run --project backend ruff format backend
	cd frontend && npm run fmt

backend:
	uv run --project backend uvicorn dcim.main:app --reload --port 8000 --host 0.0.0.0

worker:
	uv run --project backend arq dcim.worker.WorkerSettings

collector:
	uv run --project collector dcim-collector --config collector/sample-config.yaml

frontend:
	cd frontend && npm install && npm run dev

clean:
	rm -rf .venv backend/.pytest_cache backend/.ruff_cache backend/.mypy_cache
	rm -rf frontend/node_modules frontend/dist
