# Costume Tree

Costume Tree is a single-container costume inventory application. It stores productions, actors, reusable item types, and physical costume pieces in SQLite. Each physical piece receives an immutable production-scoped code such as `C-0001`.

## Prerequisites

- Go 1.27 or newer for host development
- Docker with BuildKit for the OCI image

The application serves on port `8080` and stores its database under `/data`.

## Local development

```sh
go test ./...
go vet ./...
go run ./cmd/costume-tree
```

## Justfile shortcuts

Install [just](https://just.systems/) and inspect the available recipes:

```sh
just --list
just check
just image
```

Container management uses `IMAGE`, `CONTAINER`, `VOLUME`, and `PORT` environment overrides:

```sh
IMAGE=costume-tree:dev CONTAINER=costume-tree VOLUME=costume-tree-data PORT=8080 just start
PORT=8080 just health
CONTAINER=costume-tree just logs
CONTAINER=costume-tree just backup costume-tree-backup.db
just restore backups/costume-tree-backup.db restored.db costume-tree-restore
CONTAINER=costume-tree VOLUME=costume-tree-data just stop
```

`just restore` stages a host-owned backup with a short-lived helper, then runs the application’s validated cold-restore command into a new volume.

The default local process expects `/data` to exist and uses:

- `COSTUME_TREE_ADDR=:8080`
- `COSTUME_TREE_DB_PATH=/data/costume-tree.db`
- `COSTUME_TREE_SHUTDOWN_TIMEOUT=10s`
- `COSTUME_TREE_REQUEST_TIMEOUT=30s`
- `COSTUME_TREE_MAX_BODY_BYTES=1048576`

`COSTUME_TREE_DB_PATH` must name a file under `/data`. Body limits may be increased up to 64 MiB when required for controlled operations.

## Container

Build and run with a persistent volume:

```sh
docker build -t costume-tree:dev .
docker volume create costume-tree-data
docker run --rm --name costume-tree \
  -p 8080:8080 \
  -v costume-tree-data:/data \
  costume-tree:dev
```

The image runs as UID/GID `65532:65532`. `/healthz` performs a lightweight database readiness check and returns HTTP 200 only after startup migration, integrity, schema, and migration-history validation succeed. Startup exits non-zero for an unreadable, corrupt, incompatible, or partially migrated database.

The process applies embedded migrations transactionally. It never replays migrations over a database that has user tables but no migration history. SIGINT and SIGTERM stop the HTTP server within the configured shutdown timeout.

## Backup and cold restore

Backups use SQLite `VACUUM INTO`, so a live WAL database is not copied naively. Run the backup command as the application user while the container is serving:

```sh
docker exec costume-tree /costume-tree backup /data/backups/costume-tree-$(date +%Y%m%d).db
```

The destination must be a new path in an owned directory. The command validates integrity and schema before and after creating the backup.

Restore is deliberately cold and never overwrites an existing path. Mount the fresh application volume at `/data` so the non-root process owns the destination:

```sh
docker run --rm \
  -v costume-tree-data:/source \
  -v costume-tree-restore:/data \
  costume-tree:dev \
  restore /source/backups/costume-tree-20260905.db /data/restored.db
```

Validate the restored file by starting a stopped container against it:

```sh
docker run --rm --name costume-tree-restore \
  -p 8081:8080 \
  -e COSTUME_TREE_DB_PATH=/data/restored.db \
  -v costume-tree-restore:/data \
  costume-tree:dev
```

Before an upgrade: stop the old container, create and verify a backup, build the new image, and start the new image against the same volume. If startup validation fails, stop it and restart the prior image against the unchanged volume; do not delete or overwrite the backup.

## Architecture

- `cmd/costume-tree`: composition root, signal handling, operator commands, request limits.
- `internal/config`: validated environment configuration.
- `internal/storage`: embedded migrations, SQLite setup/readiness, repositories, aggregate queries, backup/restore.
- `internal/bulk`: pure blank-line-delimited bulk-input parser.
- `internal/web`: route composition, handlers, server-rendered templates, HTMX fragments, and local static assets.
- `internal/web/templates`: progressive-enhancement HTML; ordinary POST/GET requests remain canonical when JavaScript is unavailable.
- `internal/web/assets`: vendored HTMX and Alpine assets plus application CSS; no runtime asset network access.

The web layer owns HTTP and rendering. HTMX requests receive focused server-rendered fragments; Alpine is used only for local presentation behavior. SQLite remains authoritative for all state, identifiers, aggregates, filters, and optimistic edit timestamps.

## Verification

```sh
go test ./...
go vet ./...
docker build -t costume-tree:dev .
```

The MVP intentionally does not include photos, costs, reassignment history, label printing, scanning, audit-history UI, fuzzy/global search, CSV/XLSX upload, exports, charts, replication, TLS termination, or cloud backup scheduling.
