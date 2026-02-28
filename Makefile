# Makefile for Tor Proxy Docker Management

.PHONY: help build run stop logs status clean clean-all restart shell test benchmark setup dev rebuild

help:
	@echo "Tor Proxy Docker Management"
	@echo ""
	@echo "Usage: make [command]"
	@echo ""
	@echo "Commands:"
	@echo "  help        - Show this help"
	@echo "  build       - Pull base images and warm caches"
	@echo "  run         - Run the Tor proxy service"
	@echo "  stop        - Stop the Tor proxy service"
	@echo "  logs        - View container logs"
	@echo "  status      - Check container status"
	@echo "  clean       - Stop and remove containers, networks"
	@echo "  clean-all   - Stop and remove containers, networks, images, and volumes"
	@echo "  restart     - Restart the Tor proxy service"
	@echo "  shell       - Access the container shell"
	@echo "  test        - Run tests"
	@echo "  benchmark   - Run benchmark tests"
	@echo "  setup       - Build and run the service"
	@echo "  dev         - Run in development mode (with logs)"
	@echo "  rebuild     - Clean and pull base images again"

build:
	docker compose pull

run:
	docker compose up -d

stop:
	docker compose down

logs:
	docker compose logs -f

status:
	docker compose ps

clean:
	docker compose down -v --remove-orphans

clean-all:
	docker compose down -v --rmi all --remove-orphans

restart:
	docker compose restart

shell:
	docker compose exec tor-proxy sh

test:
	./test-endpoints.sh

benchmark:
	./benchmark.sh

setup: build run

dev:
	docker compose up

rebuild: clean build
