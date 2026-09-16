# Costume Tree

Costume Tree is a costume inventory application backed by PostgreSQL. It stores productions, actors, reusable item types, and physical costume pieces. The home page lists active productions with links to each production's dashboard and actor roster. Each physical piece receives an immutable production-scoped code such as `C-0001`.

## Prerequisites

- Go 1.27 or newer for host development
- Access to an external PostgreSQL 17 or newer server plus matching `psql`, `pg_dump`, and `pg_restore` client tools
- Docker with BuildKit and Docker Compose for the application container

The application serves on port `8080`. PostgreSQL owns the structured data, while uploaded photo originals and derivatives are stored separately under `COSTUMETREE_DIR`.

## Configuration

Create a gitignored `.env` in the repository root for local commands and Compose:

```dotenv
PG_HOST=database.example.com
PG_PORT=5432
PG_USER=costume_tree
PG_PASSWORD=replace-with-a-long-random-password
PG_DATABASE=costume_tree
PG_SSLMODE=disable
DB_MAX_OPEN_CONNS=20
COSTUMETREE_DIR=./costume-tree-photos
PORT=8080
APP_UID=1000
APP_GID=1000
```

`PG_HOST`, `PG_USER`, and `PG_PASSWORD` are mandatory. `PG_PORT` defaults to `5432`, `PG_DATABASE` defaults to `costume_tree`, `PG_SSLMODE` defaults to `prefer`, and `DB_MAX_OPEN_CONNS` defaults to `20`. Use an SSL mode appropriate for the database provider; hosted production databases should normally use `verify-full` with trusted certificates. Credentials and database names are URL-escaped when the application constructs its connection string, and the connection string is not logged.

The remaining application settings are optional:

- `COSTUME_TREE_ADDR=:8080`
- `COSTUMETREE_DIR=.`
- `COSTUME_TREE_SHUTDOWN_TIMEOUT=10s`
- `COSTUME_TREE_REQUEST_TIMEOUT=30s`
- `COSTUME_TREE_MAX_BODY_BYTES=25165824`

`COSTUMETREE_DIR` may be relative or absolute. For Compose it names an existing host directory; the container uses `/photos` internally. `APP_UID` and `APP_GID` default to `1000` and should match the owner of that host directory. The 24 MiB request limit accommodates the 20 MiB photo limit plus multipart overhead; it may be increased up to 64 MiB.

`just` loads `.env` for local commands. Compose passes the same file into the application container. `PG_HOST` must resolve and be reachable from inside that container; do not use `127.0.0.1` unless PostgreSQL actually runs in the same container network namespace.

## Local development

Ensure the configured external PostgreSQL server is reachable, then run the application from the host:

```sh
just run
```

The process applies embedded PostgreSQL migrations transactionally before accepting traffic. SIGINT and SIGTERM stop the HTTP server within the configured shutdown timeout.

## Justfile shortcuts

Install [just](https://just.systems/) and inspect the available recipes:

```sh
just --list
just check
just image
just docker-push
just start
just health
just logs
just psql
```

`just up` runs the application container in the foreground. `just start` starts it in the background, and `just stop` removes it. These commands never start, stop, or delete the external PostgreSQL server. The host photo directory is never removed by these recipes.

The production dashboard links to **Reports**, where a status-filtered category count can be previewed in a modal and copied as rich text for email.

`just docker-push` builds and pushes `ghcr.io/bkroeze/costume-tree:<git-short-sha>` by default, then prints the full image reference. Configure the package visibility separately in GitHub's package settings:

```sh
IMAGE_REPOSITORY=ghcr.io/bkroeze/costume-tree IMAGE_TAG=latest just docker-push
```

## Compose

The Compose stack contains only the application. It passes the gitignored `.env` into the container and connects directly to the external PostgreSQL server named by `PG_HOST`. No PostgreSQL server, database volume, or database lifecycle is managed by this repository.

Photos use an existing host bind mount. Compose runs the application as `APP_UID:APP_GID`—`1000:1000` by default—to match the directory owner, and deliberately refuses to auto-create a root-owned source directory. Prepare it as the deployment user before first start:

```sh
mkdir -p costume-tree-photos
chmod 700 costume-tree-photos
just start
just health
```

Set a different host photo directory, host UID/GID, or published application port in `.env`:

```dotenv
COSTUMETREE_DIR=/srv/costume-tree/photos
APP_UID=1000
APP_GID=1000
PORT=8081
```

The `/healthz` endpoint performs a lightweight external PostgreSQL readiness check and returns HTTP 200 only after startup migration and schema-history validation succeed. Startup exits non-zero if PostgreSQL is unreachable, credentials are invalid, or the schema is incompatible or partially migrated.

## Costume item photos

Add or edit a costume item to attach a JPEG, PNG, or GIF photo up to 20 MiB, 60 megapixels, and 16,384 pixels on either axis. The original is saved immediately with a name beginning with the immutable costume code and a UTC timestamp, for example `C-0001-20260905T141530.123-a1b2c3d4.jpg`. Two background workers create a same-format display image with a maximum dimension of 1200 pixels and a thumbnail with a maximum dimension of 320 pixels. Item pages show a processing graphic and poll until the derivatives are ready; persisted pending work resumes after a restart.

Photo files are not stored in PostgreSQL. Back up both PostgreSQL and `COSTUMETREE_DIR` to preserve complete records.

## Removing costume items

The actor item-management page provides a confirmed **Delete** action. Delete removes the piece from active inventory by archiving it; the immutable code and detail history remain available for production records.

## PostgreSQL backup and restore

The database remains under the external server operator’s control. The recipes use local PostgreSQL client tools and the connection values from `.env`; they never start or enter a database container.

Create a native custom-format backup while the application is serving:

```sh
just pg-dump backups/costume-tree-$(date +%Y%m%d).dump
```

Copy or snapshot `COSTUMETREE_DIR` separately. Restore is a cold operation: the recipe stops only the application container, runs `pg_restore --clean --if-exists --single-transaction` against the configured external database, and restarts the application after success:

```sh
just pg-restore backups/costume-tree-20260912.dump
```

Keep a verified dump before upgrades. Coordinate retention, availability, and point-in-time recovery with the external PostgreSQL operator; this repository does not manage the server’s storage lifecycle.

## Migrating an existing SQLite database

The one-time loader imports an existing SQLite database into the external PostgreSQL server while preserving identifiers, production-scoped costume codes, timestamps, and relationships. It requires Bash, `sqlite3`, `psql`, and either `sha256sum` or `shasum`. Ensure that server is reachable, export the connection environment, and pass the source file as the only positional argument:

```sh
set -a
. ./.env
set +a
scripts/migrate-sqlite-to-postgres.sh /path/to/costume-tree.db
```

The loader uses the same `PG_HOST`, `PG_PORT`, `PG_USER`, `PG_PASSWORD`, `PG_DATABASE`, and `PG_SSLMODE` contract as the application. If the target database is unreachable because it does not exist, `PG_ADMIN_DB` selects the administrative database used to create it and defaults to `postgres`; an existing target does not require administrative-database access or `CREATEDB`. The loader initializes only the application’s `public` schema and leaves extension-owned schemas untouched. It is idempotent and may be rerun after interruption; it does not modify the source SQLite file. Stop the application while loading, then start it after the loader completes:

```sh
just start
```

Take a source-file backup before migration and retain it until row counts, photos, immutable IDs, codes, and application workflows have been verified in PostgreSQL.

## Architecture

- `cmd/costume-tree`: composition root, signal handling, and request limits.
- `internal/config`: validated environment configuration and PostgreSQL URL construction.
- `internal/storage`: pgx-backed PostgreSQL connection, embedded migrations, readiness, repositories, and aggregate queries.
- `internal/photos`: upload validation, atomic original storage, bounded background resizing, and pending-work recovery.
- `internal/bulk`: pure blank-line-delimited bulk-input parser.
- `internal/web`: route composition, handlers, server-rendered templates, HTMX fragments, and local static assets.
- `internal/web/templates`: progressive-enhancement HTML; ordinary POST/GET requests remain canonical when JavaScript is unavailable.
- `internal/web/assets`: vendored HTMX and Alpine scripts, application CSS, and local brand and web-app assets; no runtime asset network access.

The web layer owns HTTP and rendering. HTMX requests receive focused server-rendered fragments; Alpine is used only for local presentation behavior. PostgreSQL remains authoritative for all state, identifiers, aggregates, filters, and optimistic edit timestamps.

## Verification

Database-backed tests require `PG_TEST_URL` for a disposable PostgreSQL cluster. They create and remove isolated schemas, a database, and a login role; use a dedicated local administrator and never point this variable at production. Without it, the test run fails immediately.

```sh
PG_TEST_URL='postgres://costume_tree:password@127.0.0.1:5432/costume_tree_test?sslmode=disable' just check
docker build -t costume-tree:dev .
```

The MVP intentionally does not include costs, reassignment history, label printing, scanning, audit-history UI, fuzzy/global search, CSV/XLSX upload, exports, charts, replication, TLS termination, or cloud backup scheduling.
