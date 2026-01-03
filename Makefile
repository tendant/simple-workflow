.PHONY: help migrate-up migrate-down migrate-status migrate-reset test build clean

# Database configuration
DB_HOST ?= localhost
DB_PORT ?= 5432
DB_USER ?= pas
DB_PASSWORD ?= pwd
DB_NAME ?= pas
DB_SSLMODE ?= disable

# Goose configuration
MIGRATIONS_DIR = migrations
DB_URL = "host=$(DB_HOST) port=$(DB_PORT) user=$(DB_USER) password=$(DB_PASSWORD) dbname=$(DB_NAME) sslmode=$(DB_SSLMODE) search_path=workflow"

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
	@echo "  DB_USER              - Database user (default: pas)"
	@echo "  DB_PASSWORD          - Database password (default: pwd)"
	@echo "  DB_NAME              - Database name (default: pas)"
	@echo "  DB_SSLMODE           - SSL mode (default: disable)"
	@echo ""
	@echo "Example:"
	@echo "  make migrate-up DB_USER=myuser DB_PASSWORD=mypass"

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
