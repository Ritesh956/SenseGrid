.PHONY: build test lint certs env docker-up docker-down docker-logs clean

SERVICES := ingest processor control fleet hostagent
COMPOSE  := docker compose -f deploy/docker-compose.yml --env-file .env

env:
	@test -f .env || cp .env.example .env

certs:
	@bash scripts/gen-certs.sh

build:
	@mkdir -p bin
	@for s in $(SERVICES); do \
		echo "building $$s"; \
		go build -o bin/$$s ./cmd/$$s || exit 1; \
	done

test:
	go test ./... -race

lint:
	go vet ./...

run-%:
	go run ./cmd/$*

docker-up: env certs
	$(COMPOSE) up -d --build

docker-down:
	$(COMPOSE) down

docker-logs:
	$(COMPOSE) logs -f

clean:
	rm -rf bin
