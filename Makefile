SHELL := /bin/bash
COMPOSE ?= docker compose

.PHONY: help
help:
	@echo "Targets:"
	@echo "  make test      - run go tests"
	@echo "  make fmt       - gofmt (writes changes)"
	@echo "  make fmtcheck  - verify gofmt is clean"
	@echo "  make up        - docker compose up (build + detached)"
	@echo "  make down      - docker compose down"
	@echo "  make ps        - docker compose ps"
	@echo "  make logs      - follow logs for all nodes"
	@echo "  make kill-n2   - stop node n2 (failure demo)"
	@echo "  make start-n2  - start node n2"
	@echo "  make smoke     - quick PUT/GET smoke test"

.PHONY: test
test:
	go test ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: fmtcheck
fmtcheck:
	@test -z "$$(gofmt -l .)"

.PHONY: up
up:
	mkdir -p data/n1 data/n2 data/n3
	$(COMPOSE) up --build -d

.PHONY: down
down:
	$(COMPOSE) down

.PHONY: ps
ps:
	$(COMPOSE) ps

.PHONY: logs
logs:
	$(COMPOSE) logs -f

.PHONY: kill-n2
kill-n2:
	$(COMPOSE) stop n2

.PHONY: start-n2
start-n2:
	$(COMPOSE) start n2

.PHONY: smoke
smoke:
	@echo "PUT foo=bar via n1"
	@curl -sS -X PUT http://localhost:9001/kv/foo -d "bar" -o /dev/null -w "status=%{http_code}\n"
	@sleep 0.5
	@echo "GET foo via n2"
	@curl -sS http://localhost:9002/kv/foo -o /dev/null -w "status=%{http_code}\n"
	@echo "GET foo via n3"
	@curl -sS http://localhost:9003/kv/foo -o /dev/null -w "status=%{http_code}\n"
