.PHONY: help up down build test dev agent-run clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# --- Docker Compose ---

up: ## Start all services (MongoDB + API + Dashboard)
	docker compose up -d --build

down: ## Stop all services
	docker compose down

reset: ## Stop all services and remove volumes
	docker compose down -v

logs: ## Follow logs from all services
	docker compose logs -f

logs-api: ## Follow API logs
	docker compose logs -f api

# --- Build ---

build: build-agent build-api ## Build all Go binaries

build-agent: ## Build the DecisionBox Agent
	cd services/agent && go build -o ../../bin/decisionbox-agent .

build-api: ## Build the DecisionBox API
	cd services/api && go build -o ../../bin/decisionbox-api .

build-dashboard: ## Build the Dashboard
	cd ui/dashboard && npm run build

# --- Test ---

test: test-go test-ui ## Run all tests

test-go: ## Run Go unit tests
	cd libs/go-common && go test ./...
	cd domain-packs/gaming/go && go test ./...
	cd providers/llm/openai && go test ./...
	cd providers/llm/vertex-ai && go test ./...
	cd providers/secrets/mongodb && go test ./...
	cd services/agent && go test ./...
	cd services/api && go test ./...

test-integration: ## Run integration tests (requires Docker)
	cd services/agent && go test -tags=integration -count=1 .
	cd services/agent && go test -tags=integration -count=1 ./internal/database/
	cd services/api && go test -tags=integration -count=1 .

test-k8s: ## Run K8s runner integration tests (requires Docker, uses K3s testcontainer)
	cd services/api && go test -tags=integration -count=1 -timeout=5m ./internal/runner/

test-secrets: ## Run secrets provider integration tests (requires Docker)
	cd providers/secrets/mongodb && go test -tags=integration -count=1 ./...

test-ollama: ## Run Ollama LLM integration tests (requires Docker, slow)
	cd services/agent && go test -tags='integration ollama' -count=1 -timeout=10m -run TestOllama .

test-ui: ## Run dashboard tests
	cd ui/dashboard && npm test

# --- Development ---

dev-api: ## Run API locally (requires MongoDB on localhost:27017)
	cd services/api && MONGODB_URI=mongodb://localhost:27017 MONGODB_DB=decisionbox go run .

dev-dashboard: ## Run Dashboard locally
	cd ui/dashboard && npm run dev

agent-run: ## Run discovery agent for a project (usage: make agent-run PROJECT_ID=xxx)
	cd services/agent && go run . --project-id=$(PROJECT_ID)

# --- Docker ---

REGISTRY ?= ghcr.io/decisionbox-io
TAG ?= latest

docker-build: docker-build-api docker-build-agent docker-build-dashboard ## Build all Docker images

docker-build-api: ## Build API Docker image
	docker build -t $(REGISTRY)/decisionbox-api:$(TAG) -f services/api/Dockerfile .

docker-build-agent: ## Build Agent Docker image
	docker build -t $(REGISTRY)/decisionbox-agent:$(TAG) -f services/agent/Dockerfile .

docker-build-dashboard: ## Build Dashboard Docker image
	docker build -t $(REGISTRY)/decisionbox-dashboard:$(TAG) -f ui/dashboard/Dockerfile ui/dashboard

docker-push: ## Push all Docker images to registry
	docker push $(REGISTRY)/decisionbox-api:$(TAG)
	docker push $(REGISTRY)/decisionbox-agent:$(TAG)
	docker push $(REGISTRY)/decisionbox-dashboard:$(TAG)

# --- Clean ---

clean: ## Remove build artifacts
	rm -rf bin/
	rm -rf ui/dashboard/.next
