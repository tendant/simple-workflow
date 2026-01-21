.PHONY: help migrate-up migrate-down migrate-status migrate-reset test build clean

# Load .env file if it exists
ifneq (,$(wildcard .env))
    include .env
    export
endif

# Database configuration (defaults, can be overridden by .env or command line)
DB_HOST ?= localhost
DB_PORT ?= 5432
DB_USER ?= postgres
DB_PASSWORD ?= postgres
DB_NAME ?= workflow
DB_SSLMODE ?= disable
DB_SCHEMA ?= workflow

# Goose configuration
MIGRATIONS_DIR = migrations
DB_URL = "host=$(DB_HOST) port=$(DB_PORT) user=$(DB_USER) password=$(DB_PASSWORD) dbname=$(DB_NAME) sslmode=$(DB_SSLMODE) search_path=$(DB_SCHEMA)"

help:
	@echo "Simple-Workflow Makefile"
	@echo "========================"
	@echo ""
	@echo "Database Migrations:"
	@echo "  make migrate-up      - Apply all pending migrations"
	@echo "  make migrate-down    - Rollback the last migration"
	@echo "  make migrate-status  - Show migration status"
	@echo "  make migrate-reset   - Rollback all migrations and reapply"
	@echo ""
	@echo "Testing:"
	@echo "  make test            - Run integration tests"
	@echo ""
	@echo "Build:"
	@echo "  make build           - Build the library"
	@echo "  make clean           - Clean build artifacts"
	@echo ""
	@echo "Environment Variables:"
	@echo "  DB_HOST              - Database host (default: localhost)"
	@echo "  DB_PORT              - Database port (default: 5432)"
	@echo "  DB_USER              - Database user (default: postgres)"
	@echo "  DB_PASSWORD          - Database password (default: postgres)"
	@echo "  DB_NAME              - Database name (default: workflow)"
	@echo "  DB_SSLMODE           - SSL mode (default: disable)"
	@echo ""
	@echo "Configuration:"
	@echo "  1. Copy .env.example to .env and edit (recommended)"
	@echo "  2. Set environment variables in shell"
	@echo "  3. Pass variables on command line"
	@echo ""
	@echo "Example:"
	@echo "  cp .env.example .env  # Edit .env with your credentials"
	@echo "  make migrate-up       # Uses .env"
	@echo "  make migrate-up DB_USER=myuser DB_PASSWORD=mypass  # Override .env"

migrate-up:
	@echo "Applying migrations..."
	@goose -dir $(MIGRATIONS_DIR) postgres $(DB_URL) up
	@echo "✓ Migrations applied successfully"

migrate-down:
	@echo "Rolling back last migration..."
	@goose -dir $(MIGRATIONS_DIR) postgres $(DB_URL) down
	@echo "✓ Migration rolled back successfully"

migrate-status:
	@echo "Migration status:"
	@goose -dir $(MIGRATIONS_DIR) postgres $(DB_URL) status

migrate-reset:
	@echo "WARNING: This will rollback ALL migrations and reapply them!"
	@echo "Press Ctrl+C to cancel, or Enter to continue..."
	@read confirm
	@echo "Rolling back all migrations..."
	@goose -dir $(MIGRATIONS_DIR) postgres $(DB_URL) reset
	@echo "Reapplying all migrations..."
	@goose -dir $(MIGRATIONS_DIR) postgres $(DB_URL) up
	@echo "✓ Database reset complete"

test:
	@echo "Running integration tests..."
	@./test_integration.sh

build:
	@echo "Building simple-workflow library..."
	@go build
	@echo "✓ Build successful"

clean:
	@echo "Cleaning build artifacts..."
	@go clean
	@echo "✓ Clean complete"

# Create a new migration file
migrate-create:
	@if [ -z "$(NAME)" ]; then \
		echo "Error: NAME is required. Usage: make migrate-create NAME=my_migration"; \
		exit 1; \
	fi
	@goose -dir $(MIGRATIONS_DIR) create $(NAME) sql
	@echo "✓ Migration file created"
