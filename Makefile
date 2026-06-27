.PHONY: build run test tidy fmt vet docker-up docker-down clean

BINARY := gateway
CMD    := ./cmd/gateway

build: ## Compile the gateway binary
	go build -o bin/$(BINARY) $(CMD)

run: ## Run the gateway locally (uses configs/config.yaml)
	go run $(CMD)

test: ## Run all unit tests with race detector
	go test -race -cover ./...

tidy: ## Tidy go.mod/go.sum
	go mod tidy

fmt: ## Format all source files
	gofmt -s -w .

vet: ## Run go vet static analysis
	go vet ./...

docker-up: ## Bring up the full stack (gateway + redis + upstream)
	docker compose -f deployments/docker-compose.yml up -d --build

docker-down: ## Tear down the stack
	docker compose -f deployments/docker-compose.yml down

clean: ## Remove build artifacts
	rm -rf bin/
