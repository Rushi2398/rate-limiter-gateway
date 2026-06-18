.PHONY: build run test tidy fmt vet clean

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

clean: ## Remove build artifacts
	rm -rf bin/
