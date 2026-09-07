# Makefile — atalhos do dia a dia. `make check` roda o que o CI roda.
SHELL := /bin/bash
APP     := http-server-projeto-korp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)
PROM_IMG:= prom/prometheus:v3.5.0
PROM_DIR:= $(CURDIR)/observability/prometheus

.PHONY: help build run test lint sec swagger swagger-check check docker up up-full down logs smoke promtool-test ansible-check ansible-run bench sbom clean

help: ## lista os alvos
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

build: ## binário local com versão injetada
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(APP) ./cmd/$(APP)

run: build ## roda o binário local na porta do.env.example
./bin/$(APP)

test: ## testes com detector de corrida e cobertura
	go test -race -shuffle=on -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint: ## go vet + golangci-lint
	go vet./...
	golangci-lint run ./...

sec: ## gosec + govulncheck
	gosec -quiet./...
	govulncheck./...

swagger: ## regenera api/ a partir das anotações
	swag init -g cmd/$(APP)/main.go -o api

swagger-check: swagger ## falha se api/ estiver desatualizada (mesmo check do CI)
	git diff --exit-code -- api/

check: lint test sec swagger-check promtool-test ## tudo que o CI verifica, localmente

docker: ## imagem local
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t $(APP):$(VERSION) .

up: ## stack mínima do brief (app + nginx + prometheus + grafana)
	VERSION=$(VERSION) docker compose up -d --build --wait

up-full: ## stack estendido: + alertmanager, otel-collector, tempo, loki, alloy, pyroscope, cadvisor, node-exporter, blackbox, nginx-exporter
	VERSION=$(VERSION) docker compose --profile full up -d --build --wait

down:
	docker compose --profile full down -v --remove-orphans

logs:
	docker compose logs -f --tail=100 $(APP) nginx

smoke: ## chama o serviço pelo NGINX (:80), como o avaliador vai fazer
	curl -fsS http://localhost/projeto-korp; echo
	curl -fsS -o /dev/null -w "swagger: %{http_code}\n" http://localhost/swagger/index.html
	curl -fsS http://localhost:9090/-/ready

promtool-test: ## config, regras (lint estrito) e testes unitários das regras
	docker run --rm -v "$(PROM_DIR):/cfg:ro" --entrypoint promtool $(PROM_IMG) check config /cfg/prometheus.yml
	docker run --rm -v "$(PROM_DIR):/cfg:ro" --entrypoint promtool $(PROM_IMG) check rules --lint=all --lint-fatal /cfg/rules/*.rules.yml
	docker run --rm -v "$(PROM_DIR):/cfg:ro" --entrypoint promtool $(PROM_IMG) test rules /cfg/rules/tests/*.test.yml

ansible-check: ## lint + syntax + check mode (no alvo Linux)
	yamllint ansible/
	ansible-lint --profile production ansible/playbook.yml
	ansible-playbook -i ansible/inventory.ini --syntax-check ansible/playbook.yml
	ansible-playbook -i ansible/inventory.ini ansible/playbook.yml --check --diff

ansible-run: ## o comando único do brief
	ansible-playbook -i ansible/inventory.ini ansible/playbook.yml

bench: ## benchmarks do hot path
	go test -run '^$$' -bench . -benchmem -count=3 ./...

sbom: ## SBOM em SPDX
	syft dir:. -o spdx-json > sbom.spdx.json

clean:
	rm -rf bin coverage.out sbom.spdx.json
