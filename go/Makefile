.PHONY: help deps run build build-linux build-windows clean \
	migrate-up migrate-down migrate-create migrate-status migrate-force model-gen \
	test test-unit test-integration test-all test-bench test-cover test-race \
	lint fmt check check-all install

# =============================================================================
# Variables
# =============================================================================
APP_NAME=nof0-api
CONFIG_FILE=etc/nof0.yaml
BUILD_DIR=bin
TEST_SCRIPT=scripts/run_tests.py

# =============================================================================
# Help and Documentation
# =============================================================================
help: ## Display this help message
	@echo "Available commands:"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# =============================================================================
# Development Environment Setup
# =============================================================================
deps: ## Install dependencies
	go mod download
	go mod tidy

# =============================================================================
# Development and Running
# =============================================================================
run: ## Run the application in development mode
	go run nof0.go -f $(CONFIG_FILE)

# =============================================================================
# Build Commands
# =============================================================================
build: ## Build the application
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(APP_NAME) nof0.go
	@echo "Build complete: $(BUILD_DIR)/$(APP_NAME)"

build-linux: ## Build for Linux
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build -o $(BUILD_DIR)/$(APP_NAME)-linux nof0.go
	@echo "Build complete: $(BUILD_DIR)/$(APP_NAME)-linux"

build-windows: ## Build for Windows
	@mkdir -p $(BUILD_DIR)
	GOOS=windows GOARCH=amd64 go build -o $(BUILD_DIR)/$(APP_NAME).exe nof0.go
	@echo "Build complete: $(BUILD_DIR)/$(APP_NAME).exe"

clean: ## Clean build artifacts
	rm -rf $(BUILD_DIR)
	go clean

# =============================================================================
# Database Operations (requires golang-migrate cli and POSTGRES_DSN env var)
# =============================================================================
.check-postgres-dsn:
	@test -n "$(POSTGRES_DSN)" || (echo "\033[31mError: POSTGRES_DSN environment variable is not set\033[0m" && \
		echo "\033[33mPlease set POSTGRES_DSN with your database connection string.\033[0m" && \
		echo "\033[33mExample: export POSTGRES_DSN='postgres://user:password@localhost:5432/dbname?sslmode=disable'\033[0m" && \
		exit 1)

migrate-up: .check-postgres-dsn ## Run all pending migrations
	migrate -path migrations -database "$(POSTGRES_DSN)" -verbose up

migrate-down: .check-postgres-dsn ## Rollback last migration
	migrate -path migrations -database "$(POSTGRES_DSN)" -verbose down 1

migrate-create: ## Create new migration file (usage: make migrate-create NAME=add_users)
	@test -n "$(NAME)" || (echo "\033[31mError: NAME parameter is required\033[0m" && \
		echo "\033[33mUsage: make migrate-create NAME=add_users\033[0m" && exit 1)
	migrate create -ext sql -dir migrations -seq $(NAME)

migrate-status: .check-postgres-dsn ## Show current migration version
	migrate -path migrations -database "$(POSTGRES_DSN)" version

migrate-force: .check-postgres-dsn ## Force migration version (usage: make migrate-force VERSION=2)
	@test -n "$(VERSION)" || (echo "\033[31mError: VERSION parameter is required\033[0m" && \
		echo "\033[33mUsage: make migrate-force VERSION=2\033[0m" && exit 1)
	migrate -path migrations -database "$(POSTGRES_DSN)" force $(VERSION)

model-gen: .check-postgres-dsn ## Regenerate Go models from database schema
	goctl model pg datasource --url "$(POSTGRES_DSN)" --dir internal/model --cache --table "*"

# =============================================================================
# Test Commands
# =============================================================================
test: ## Run basic Go tests
	go test -v ./...

test-unit: ## Run unit tests using Python test script
	python $(TEST_SCRIPT) unit

test-integration: ## Run integration tests using Python test script
	python $(TEST_SCRIPT) integration

test-all: ## Run all tests (unit + integration) using Python test script
	python $(TEST_SCRIPT) all

test-bench: ## Run benchmarks
	go test -bench=. -benchmem ./...

test-cover: ## Generate test coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

test-race: ## Run tests with race detector
	go test -race ./...

# =============================================================================
# Code Quality
# =============================================================================
lint: ## Run linter
	golangci-lint run

fmt: ## Format code
	go fmt ./...
	gofmt -s -w .

check: fmt lint test ## Run all checks (format, lint, test)

check-all: fmt lint test-all ## Run all checks including integration tests

# =============================================================================
# Installation
# =============================================================================
install: build ## Install the binary
	@cp $(BUILD_DIR)/$(APP_NAME) $(GOPATH)/bin/
	@echo "Installed to $(GOPATH)/bin/$(APP_NAME)"
