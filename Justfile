set shell := ["bash", "-eu", "-o", "pipefail", "-c"]
set dotenv-load := true

image := env_var_or_default("IMAGE", "costume-tree:dev")
port := env_var_or_default("PORT", "8080")
image_repository := env_var_or_default("IMAGE_REPOSITORY", "ghcr.io/bkroeze/costume-tree")
image_tag := env_var_or_default("IMAGE_TAG", `git rev-parse --short HEAD`)

# Show the available project commands.
default:
    @just --list

# Run the Go unit and integration tests.
test:
    go test ./...

# Run static analysis.
vet:
    go vet ./...

# Run the local verification gate.
check: test vet

# Format tracked Go sources.
fmt:
    gofmt -w $(git ls-files '*.go')

# Start the application from the host Go toolchain using .env.
run:
    go run ./cmd/costume-tree

# Build the production binary.
build:
    go build ./cmd/costume-tree

# Build the OCI image. Override IMAGE=... when needed.
image:
    docker build --tag {{image}} .

# Build and push an image. Set GHCR visibility in the package settings.
docker-push:
    @docker build --quiet --tag {{image_repository}}:{{image_tag}} . >/dev/null
    @docker push {{image_repository}}:{{image_tag}} >&2
    @printf '%s:%s\n' '{{image_repository}}' '{{image_tag}}'

# Build and run the application against the external PostgreSQL server.
up:
    docker compose up --build

# Build and start the application against the external PostgreSQL server.
start:
    docker compose up --build --detach

# Stop the application container.
stop:
    docker compose down

# Follow application logs.
logs:
    docker compose logs --follow costume-tree

# Check the local health endpoint.
health:
    curl --fail --silent --show-error http://127.0.0.1:{{port}}/healthz

# Open a shell in the application container.
shell:
    docker compose exec costume-tree /bin/sh

# Open psql against the external PostgreSQL server configured in .env.
psql:
    @PGHOST="$PG_HOST" PGPORT="${PG_PORT:-5432}" PGUSER="$PG_USER" PGPASSWORD="$PG_PASSWORD" PGDATABASE="${PG_DATABASE:-costume_tree}" PGSSLMODE="${PG_SSLMODE:-prefer}" psql

# Write a custom-format backup from the external PostgreSQL server.
pg-dump output="backups/costume-tree.dump":
    @mkdir -p "$(dirname '{{output}}')"
    @PGHOST="$PG_HOST" PGPORT="${PG_PORT:-5432}" PGUSER="$PG_USER" PGPASSWORD="$PG_PASSWORD" PGDATABASE="${PG_DATABASE:-costume_tree}" PGSSLMODE="${PG_SSLMODE:-prefer}" pg_dump --format=custom --no-owner --no-privileges > '{{output}}'
    @printf 'backup: %s\n' '{{output}}'

# Cold-restore a custom-format backup to the external PostgreSQL server.
pg-restore backup:
    docker compose stop costume-tree
    @PGHOST="$PG_HOST" PGPORT="${PG_PORT:-5432}" PGUSER="$PG_USER" PGPASSWORD="$PG_PASSWORD" PGDATABASE="${PG_DATABASE:-costume_tree}" PGSSLMODE="${PG_SSLMODE:-prefer}" pg_restore --clean --if-exists --exit-on-error --single-transaction --no-owner --no-privileges < '{{backup}}'
    docker compose start costume-tree

# Remove the application container; external PostgreSQL and host photos remain.
clean:
    docker compose down --remove-orphans
