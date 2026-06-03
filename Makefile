.PHONY: help up down logs build finch frontend

help:
	@echo "USG DCIM dev tasks"
	@echo "  make up           start full stack via podman compose"
	@echo "  make down         stop stack"
	@echo "  make logs         tail all service logs"
	@echo "  make build        build container images via compose"
	@echo "  make finch        vite dev server (finch) on :5173    [alias: frontend]"

up:
	podman compose -f deploy/docker/docker-compose.yml up -d --build

down:
	podman compose -f deploy/docker/docker-compose.yml down

logs:
	podman compose -f deploy/docker/docker-compose.yml logs -f

build:
	podman compose -f deploy/docker/docker-compose.yml build

finch:
	cd packages/finch && npm install && npm run dev

frontend: finch
