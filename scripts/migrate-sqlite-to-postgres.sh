#!/usr/bin/env bash
set +x
set -Eeuo pipefail
IFS=$'\n\t'
umask 077

usage() {
    printf 'Usage: %s SOURCE_SQLITE_PATH\n' "${0##*/}" >&2
}

die() {
    printf 'error: %s\n' "$1" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

if (( $# != 1 )); then
    usage
    exit 2
fi

SOURCE_SQLITE=$1
[[ -f "$SOURCE_SQLITE" && -r "$SOURCE_SQLITE" ]] || die "source must be a readable regular SQLite file"

[[ -n ${PG_HOST:-} ]] || die "PG_HOST is required"
[[ -n ${PG_USER:-} ]] || die "PG_USER is required"
[[ -n ${PG_PASSWORD:-} ]] || die "PG_PASSWORD is required"

PG_PORT=${PG_PORT:-5432}
PG_DATABASE=${PG_DATABASE:-costume_tree}
PG_ADMIN_DB=${PG_ADMIN_DB:-postgres}
PG_SSLMODE=${PG_SSLMODE:-prefer}

[[ $PG_PORT =~ ^[0-9]{1,5}$ ]] || die "PG_PORT must be an integer between 1 and 65535"
PG_PORT_NUMBER=$((10#$PG_PORT))
(( PG_PORT_NUMBER >= 1 && PG_PORT_NUMBER <= 65535 )) || die "PG_PORT must be an integer between 1 and 65535"

require_command sqlite3
require_command psql
require_command mktemp
require_command cp
require_command rm

if command -v sha256sum >/dev/null 2>&1; then
    HASH_PROGRAM=sha256sum
elif command -v shasum >/dev/null 2>&1; then
    HASH_PROGRAM=shasum
else
    die "required SHA-256 command not found (sha256sum or shasum)"
fi

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P) || die "cannot resolve script directory"
REPOSITORY_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P) || die "cannot resolve repository directory"
MIGRATION_0001="$REPOSITORY_DIR/internal/storage/migrations/0001_initial.sql"
MIGRATION_0002="$REPOSITORY_DIR/internal/storage/migrations/0002_costume_item_photos.sql"
[[ -f "$MIGRATION_0001" && -r "$MIGRATION_0001" && -s "$MIGRATION_0001" ]] || die "required repository migration is missing or unreadable: 0001_initial.sql"
[[ -f "$MIGRATION_0002" && -r "$MIGRATION_0002" && -s "$MIGRATION_0002" ]] || die "required repository migration is missing or unreadable: 0002_costume_item_photos.sql"

WORK_DIR=''
cleanup() {
    if [[ -n $WORK_DIR && -d $WORK_DIR ]]; then
        rm -rf -- "$WORK_DIR"
    fi
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

WORK_DIR=$(mktemp -d /tmp/costume-tree-sqlite-migration.XXXXXX) || die "cannot create private temporary directory"
SOURCE_COPY="$WORK_DIR/source.sqlite"

checksum_file() {
    local path=$1
    local output digest

    if [[ $HASH_PROGRAM == sha256sum ]]; then
        output=$(sha256sum -- "$path" 2>/dev/null) || return 1
    else
        output=$(shasum -a 256 -- "$path" 2>/dev/null) || return 1
    fi
    digest=${output:0:64}
    [[ $digest =~ ^[0-9a-fA-F]{64}$ ]] || return 1
    printf '%s' "${digest,,}"
}

SOURCE_SHA256=$(checksum_file "$SOURCE_SQLITE") || die "cannot checksum source SQLite file"
cp -- "$SOURCE_SQLITE" "$SOURCE_COPY" || die "cannot copy source SQLite file"
COPY_SHA256=$(checksum_file "$SOURCE_COPY") || die "cannot checksum private SQLite copy"
SOURCE_SHA256_AFTER_COPY=$(checksum_file "$SOURCE_SQLITE") || die "cannot re-checksum source SQLite file"
[[ $SOURCE_SHA256 == "$COPY_SHA256" && $SOURCE_SHA256 == "$SOURCE_SHA256_AFTER_COPY" ]] || die "source SQLite file changed while it was being copied"

if ! sqlite3 -batch -noheader "$SOURCE_COPY" 'PRAGMA integrity_check;' >"$WORK_DIR/integrity.out" 2>"$WORK_DIR/sqlite-error.log"; then
    die "SQLite integrity check could not be completed"
fi
mapfile -t INTEGRITY_RESULTS <"$WORK_DIR/integrity.out"
(( ${#INTEGRITY_RESULTS[@]} == 1 )) && [[ ${INTEGRITY_RESULTS[0]} == ok ]] || die "SQLite integrity check failed"

if ! sqlite3 -batch -noheader "$SOURCE_COPY" 'PRAGMA foreign_keys = ON; PRAGMA foreign_key_check;' >"$WORK_DIR/foreign-keys.out" 2>"$WORK_DIR/sqlite-error.log"; then
    die "SQLite foreign-key check could not be completed"
fi
[[ ! -s "$WORK_DIR/foreign-keys.out" ]] || die "SQLite foreign-key check failed"

sqlite_scalar() {
    local query=$1
    local output

    output=$(sqlite3 -batch -noheader "$SOURCE_COPY" "$query" 2>"$WORK_DIR/sqlite-error.log") || return 1
    printf '%s' "$output"
}

SOURCE_HISTORY_VALID=$(sqlite_scalar "
SELECT CASE
    WHEN COUNT(*) = 2
     AND SUM(CASE WHEN version = 1 AND name = '0001_initial.sql' THEN 1 ELSE 0 END) = 1
     AND SUM(CASE WHEN version = 2 AND name = '0002_costume_item_photos.sql' THEN 1 ELSE 0 END) = 1
     AND SUM(CASE WHEN applied_at IS NULL THEN 1 ELSE 0 END) = 0
    THEN 1 ELSE 0
END
FROM schema_migrations;") || die "source migration history is missing or unreadable"
[[ $SOURCE_HISTORY_VALID == 1 ]] || die "source migration history does not match repository migrations"

NULL_SENTINEL="__COSTUME_TREE_NULL_${SOURCE_SHA256}__"
NULL_COLLISION=$(sqlite_scalar "
SELECT EXISTS (
    SELECT 1 FROM schema_migrations
     WHERE CAST(version AS TEXT) = '$NULL_SENTINEL'
        OR CAST(name AS TEXT) = '$NULL_SENTINEL'
        OR CAST(applied_at AS TEXT) = '$NULL_SENTINEL'
    UNION ALL
    SELECT 1 FROM productions
     WHERE CAST(id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(name AS TEXT) = '$NULL_SENTINEL'
        OR CAST(archived_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(created_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(updated_at AS TEXT) = '$NULL_SENTINEL'
    UNION ALL
    SELECT 1 FROM actors
     WHERE CAST(id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(production_id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(name AS TEXT) = '$NULL_SENTINEL'
        OR CAST(role AS TEXT) = '$NULL_SENTINEL'
        OR CAST(notes AS TEXT) = '$NULL_SENTINEL'
        OR CAST(archived_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(created_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(updated_at AS TEXT) = '$NULL_SENTINEL'
    UNION ALL
    SELECT 1 FROM item_types
     WHERE CAST(id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(production_id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(name AS TEXT) = '$NULL_SENTINEL'
        OR CAST(archived_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(created_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(updated_at AS TEXT) = '$NULL_SENTINEL'
    UNION ALL
    SELECT 1 FROM production_item_sequences
     WHERE CAST(production_id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(next_value AS TEXT) = '$NULL_SENTINEL'
    UNION ALL
    SELECT 1 FROM costume_items
     WHERE CAST(id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(production_id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(actor_id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(item_type_id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(code AS TEXT) = '$NULL_SENTINEL'
        OR CAST(description AS TEXT) = '$NULL_SENTINEL'
        OR CAST(status AS TEXT) = '$NULL_SENTINEL'
        OR CAST(progress AS TEXT) = '$NULL_SENTINEL'
        OR CAST(next_action AS TEXT) = '$NULL_SENTINEL'
        OR CAST(blocker AS TEXT) = '$NULL_SENTINEL'
        OR CAST(notes AS TEXT) = '$NULL_SENTINEL'
        OR CAST(archived_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(created_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(updated_at AS TEXT) = '$NULL_SENTINEL'
    UNION ALL
    SELECT 1 FROM costume_item_photos
     WHERE CAST(id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(production_id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(costume_item_id AS TEXT) = '$NULL_SENTINEL'
        OR CAST(original_name AS TEXT) = '$NULL_SENTINEL'
        OR CAST(display_name AS TEXT) = '$NULL_SENTINEL'
        OR CAST(thumbnail_name AS TEXT) = '$NULL_SENTINEL'
        OR CAST(media_type AS TEXT) = '$NULL_SENTINEL'
        OR CAST(status AS TEXT) = '$NULL_SENTINEL'
        OR CAST(error_message AS TEXT) = '$NULL_SENTINEL'
        OR CAST(created_at AS TEXT) = '$NULL_SENTINEL'
        OR CAST(updated_at AS TEXT) = '$NULL_SENTINEL'
);") || die "source schema is missing required migration columns"
[[ $NULL_COLLISION == 0 ]] || die "source contains the reserved CSV NULL sentinel"

TABLES=(
    schema_migrations
    productions
    actors
    item_types
    production_item_sequences
    costume_items
    costume_item_photos
)
COLUMNS=(
    'version, name, applied_at'
    'id, name, archived_at, created_at, updated_at'
    'id, production_id, name, role, notes, archived_at, created_at, updated_at'
    'id, production_id, name, archived_at, created_at, updated_at'
    'production_id, next_value'
    'id, production_id, actor_id, item_type_id, code, description, status, progress, next_action, blocker, notes, archived_at, created_at, updated_at'
    'id, production_id, costume_item_id, original_name, display_name, thumbnail_name, media_type, status, error_message, created_at, updated_at'
)
ORDERINGS=(
    'version'
    'id'
    'production_id, id'
    'production_id, id'
    'production_id'
    'production_id, id'
    'production_id, costume_item_id, id'
)
SOURCE_COUNTS=()
CSV_FILES=()

for (( index = 0; index < ${#TABLES[@]}; index++ )); do
    table=${TABLES[$index]}
    count=$(sqlite_scalar "SELECT COUNT(*) FROM $table;") || die "cannot count source table: $table"
    [[ $count =~ ^[0-9]+$ ]] || die "source table returned an invalid count: $table"
    SOURCE_COUNTS+=("$count")

    csv_file="$WORK_DIR/$table.csv"
    CSV_FILES+=("$csv_file")
    if ! sqlite3 -batch -noheader -csv -nullvalue "$NULL_SENTINEL" "$SOURCE_COPY" \
        "SELECT ${COLUMNS[$index]} FROM $table ORDER BY ${ORDERINGS[$index]};" \
        >"$csv_file" 2>"$WORK_DIR/sqlite-error.log"; then
        die "cannot export source table: $table"
    fi
done

COPY_SHA256_AFTER_EXPORT=$(checksum_file "$SOURCE_COPY") || die "cannot re-checksum private SQLite copy"
SOURCE_SHA256_BEFORE_LOAD=$(checksum_file "$SOURCE_SQLITE") || die "cannot re-checksum source SQLite file"
[[ $COPY_SHA256_AFTER_EXPORT == "$SOURCE_SHA256" ]] || die "private SQLite copy changed during export"
[[ $SOURCE_SHA256_BEFORE_LOAD == "$SOURCE_SHA256" ]] || die "source SQLite file changed during export"

export PGHOST=$PG_HOST
export PGPORT=$PG_PORT_NUMBER
export PGUSER=$PG_USER
export PGPASSWORD=$PG_PASSWORD
export PGSSLMODE=$PG_SSLMODE
export PGCLIENTENCODING=UTF8

psql_admin() {
    PGDATABASE=$PG_ADMIN_DB psql -X -qAt --set=ON_ERROR_STOP=1 "$@"
}

psql_target() {
    PGDATABASE=$PG_DATABASE psql -X -qAt --set=ON_ERROR_STOP=1 "$@"
}

psql_target_scalar() {
    local query=$1
    local output

    output=$(psql_target --command "$query" 2>"$WORK_DIR/postgres-error.log") || return 1
    printf '%s' "$output"
}

if psql_target --command 'SELECT 1;' >/dev/null 2>"$WORK_DIR/postgres-error.log"; then
    TARGET_DATABASE_EXISTS=1
else
    TARGET_DATABASE_EXISTS=$(psql_admin --set=target_database="$PG_DATABASE" --command "
SELECT CASE WHEN EXISTS (
    SELECT 1 FROM pg_database WHERE datname = :'target_database'
) THEN 1 ELSE 0 END;
" 2>"$WORK_DIR/postgres-error.log") || die "cannot connect to the target or PostgreSQL admin database"
fi
[[ $TARGET_DATABASE_EXISTS =~ ^[01]$ ]] || die "PostgreSQL returned an invalid target database status"

if [[ $TARGET_DATABASE_EXISTS == 0 ]]; then
    CAN_CREATE_DATABASE=$(psql_admin --command "
SELECT CASE WHEN rolsuper OR rolcreatedb THEN 1 ELSE 0 END
FROM pg_roles
WHERE rolname = current_user;
" 2>"$WORK_DIR/postgres-error.log") || die "cannot inspect PostgreSQL database privileges"
    [[ $CAN_CREATE_DATABASE == 1 ]] || die "PG_USER must have CREATEDB-compatible privilege when the target database does not exist"

    if ! psql_admin --set=target_database="$PG_DATABASE" >"$WORK_DIR/create-database.out" 2>"$WORK_DIR/postgres-error.log" <<'SQL'
SELECT format('CREATE DATABASE %I', :'target_database')
WHERE NOT EXISTS (
    SELECT 1 FROM pg_database WHERE datname = :'target_database'
)
\gexec
SQL
    then
        die "cannot create the target PostgreSQL database"
    fi
fi

TARGET_RELATION_COUNT=$(psql_target_scalar "
SELECT COUNT(*)
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'S', 'f')
  AND n.nspname = 'public';
") || die "cannot inspect the target PostgreSQL database"
[[ $TARGET_RELATION_COUNT =~ ^[0-9]+$ ]] || die "target PostgreSQL database returned an invalid relation count"

if (( TARGET_RELATION_COUNT == 0 )); then
    INIT_SQL="$WORK_DIR/initialize-target.sql"
    {
        cat <<'SQL'
BEGIN;
SET LOCAL search_path = public, pg_catalog;
CREATE TABLE public.schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
SQL
        cat "$MIGRATION_0001"
        printf '\n'
        cat "$MIGRATION_0002"
        printf '\n'
        printf '%s\n' "\\copy public.schema_migrations (version, name, applied_at) FROM '${CSV_FILES[0]}' WITH (FORMAT csv, NULL '$NULL_SENTINEL')"
        cat <<'SQL'
COMMIT;
SQL
    } >"$INIT_SQL"

    if ! psql_target --file "$INIT_SQL" >"$WORK_DIR/initialize-target.out" 2>"$WORK_DIR/postgres-error.log"; then
        die "cannot initialize the blank target database from repository migrations"
    fi
fi

EXPECTED_TARGET_TABLES=$(psql_target_scalar "
SELECT COUNT(*)
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p')
  AND c.relname IN (
      'schema_migrations',
      'productions',
      'actors',
      'item_types',
      'production_item_sequences',
      'costume_items',
      'costume_item_photos'
  );
") || die "cannot inspect target tables"
[[ $EXPECTED_TARGET_TABLES == 7 ]] || die "target does not contain the complete PostgreSQL schema"

UNEXPECTED_TARGET_TABLES=$(psql_target_scalar "
SELECT COUNT(*)
FROM pg_class AS c
JOIN pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relkind IN ('r', 'p', 'v', 'm', 'f')
  AND c.relname NOT IN (
      'schema_migrations',
      'productions',
      'actors',
      'item_types',
      'production_item_sequences',
      'costume_items',
      'costume_item_photos',
      'sqlite_migration_source'
  );
") || die "cannot inspect target tables"
[[ $UNEXPECTED_TARGET_TABLES == 0 ]] || die "target contains tables outside the owned PostgreSQL schema"

TARGET_HISTORY_VALID=$(psql_target_scalar "
SELECT CASE
    WHEN COUNT(*) = 2
     AND COUNT(*) FILTER (WHERE version = 1 AND name = '0001_initial.sql') = 1
     AND COUNT(*) FILTER (WHERE version = 2 AND name = '0002_costume_item_photos.sql') = 1
     AND COUNT(*) FILTER (WHERE applied_at IS NULL) = 0
    THEN 1 ELSE 0
END
FROM public.schema_migrations;
") || die "cannot inspect target migration history"
[[ $TARGET_HISTORY_VALID == 1 ]] || die "target migration history does not match repository migrations"

MARKER_EXISTS=$(psql_target_scalar "
SELECT CASE WHEN EXISTS (
    SELECT 1
    FROM pg_class AS c
    JOIN pg_namespace AS n ON n.oid = c.relnamespace
    WHERE n.nspname = 'public'
      AND c.relname = 'sqlite_migration_source'
      AND c.relkind = 'r'
) THEN 1 ELSE 0 END;
") || die "cannot inspect target migration marker"

if [[ $MARKER_EXISTS == 1 ]]; then
    MARKER_ROWS=$(psql_target_scalar 'SELECT COUNT(*) FROM public.sqlite_migration_source;') || die "target migration marker is unreadable"
    [[ $MARKER_ROWS == 1 ]] || die "target migration marker is malformed"
    STORED_SOURCE_SHA256=$(psql_target_scalar 'SELECT source_sha256 FROM public.sqlite_migration_source WHERE id = 1;') || die "target migration marker is unreadable"
    [[ $STORED_SOURCE_SHA256 == "$SOURCE_SHA256" ]] || die "target was loaded from a different SQLite source; refusing to replace it"
    printf 'Migration already complete; source SHA-256 marker matches.\n'
    exit 0
else
    TARGET_DATA_ROWS=$(psql_target_scalar "
SELECT (SELECT COUNT(*) FROM public.productions)
     + (SELECT COUNT(*) FROM public.actors)
     + (SELECT COUNT(*) FROM public.item_types)
     + (SELECT COUNT(*) FROM public.production_item_sequences)
     + (SELECT COUNT(*) FROM public.costume_items)
     + (SELECT COUNT(*) FROM public.costume_item_photos);
") || die "cannot inspect target data"
    [[ $TARGET_DATA_ROWS =~ ^[0-9]+$ ]] || die "target PostgreSQL database returned an invalid data count"
    (( TARGET_DATA_ROWS == 0 )) || die "target contains data without a matching SQLite source marker; refusing to replace it"
fi

LOAD_SQL="$WORK_DIR/load-target.sql"
cat >"$LOAD_SQL" <<SQL
BEGIN;
SET LOCAL search_path = public, pg_catalog;
CREATE TABLE IF NOT EXISTS public.sqlite_migration_source (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    source_sha256 TEXT NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    imported_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);
LOCK TABLE
    public.schema_migrations,
    public.productions,
    public.actors,
    public.item_types,
    public.production_item_sequences,
    public.costume_items,
    public.costume_item_photos,
    public.sqlite_migration_source
IN ACCESS EXCLUSIVE MODE;

SELECT
    (SELECT COUNT(*) FROM public.sqlite_migration_source) = 1
        AND EXISTS (
            SELECT 1 FROM public.sqlite_migration_source
            WHERE id = 1 AND source_sha256 = '$SOURCE_SHA256'
        ) AS migration_already_complete,
    (SELECT COUNT(*) FROM public.sqlite_migration_source) = 0
        AND (
            (SELECT COUNT(*) FROM public.productions)
          + (SELECT COUNT(*) FROM public.actors)
          + (SELECT COUNT(*) FROM public.item_types)
          + (SELECT COUNT(*) FROM public.production_item_sequences)
          + (SELECT COUNT(*) FROM public.costume_items)
          + (SELECT COUNT(*) FROM public.costume_item_photos)
        ) = 0 AS migration_target_empty
\gset
\if :migration_already_complete
COMMIT;
\echo migration_already_complete
\quit 0
\endif
\if :migration_target_empty
\else
ROLLBACK;
\quit 4
\endif

\copy public.productions (id, name, archived_at, created_at, updated_at) FROM '${CSV_FILES[1]}' WITH (FORMAT csv, NULL '$NULL_SENTINEL')
\copy public.actors (id, production_id, name, role, notes, archived_at, created_at, updated_at) FROM '${CSV_FILES[2]}' WITH (FORMAT csv, NULL '$NULL_SENTINEL')
\copy public.item_types (id, production_id, name, archived_at, created_at, updated_at) FROM '${CSV_FILES[3]}' WITH (FORMAT csv, NULL '$NULL_SENTINEL')
\copy public.production_item_sequences (production_id, next_value) FROM '${CSV_FILES[4]}' WITH (FORMAT csv, NULL '$NULL_SENTINEL')
\copy public.costume_items (id, production_id, actor_id, item_type_id, code, description, status, progress, next_action, blocker, notes, archived_at, created_at, updated_at) FROM '${CSV_FILES[5]}' WITH (FORMAT csv, NULL '$NULL_SENTINEL')
\copy public.costume_item_photos (id, production_id, costume_item_id, original_name, display_name, thumbnail_name, media_type, status, error_message, created_at, updated_at) FROM '${CSV_FILES[6]}' WITH (FORMAT csv, NULL '$NULL_SENTINEL')

SELECT (
       (SELECT COUNT(*) FROM public.schema_migrations) = ${SOURCE_COUNTS[0]}
   AND (SELECT COUNT(*) FROM public.productions) = ${SOURCE_COUNTS[1]}
   AND (SELECT COUNT(*) FROM public.actors) = ${SOURCE_COUNTS[2]}
   AND (SELECT COUNT(*) FROM public.item_types) = ${SOURCE_COUNTS[3]}
   AND (SELECT COUNT(*) FROM public.production_item_sequences) = ${SOURCE_COUNTS[4]}
   AND (SELECT COUNT(*) FROM public.costume_items) = ${SOURCE_COUNTS[5]}
   AND (SELECT COUNT(*) FROM public.costume_item_photos) = ${SOURCE_COUNTS[6]}
) AS migration_counts_match
\gset
\if :migration_counts_match
\else
ROLLBACK;
\quit 5
\endif

SELECT GREATEST(COALESCE(MAX(id)::numeric + 1, 1), 1) AS productions_next FROM public.productions
\gset
SELECT GREATEST(COALESCE(MAX(id)::numeric + 1, 1), 1) AS actors_next FROM public.actors
\gset
SELECT GREATEST(COALESCE(MAX(id)::numeric + 1, 1), 1) AS item_types_next FROM public.item_types
\gset
SELECT GREATEST(COALESCE(MAX(id)::numeric + 1, 1), 1) AS costume_items_next FROM public.costume_items
\gset
SELECT GREATEST(COALESCE(MAX(id)::numeric + 1, 1), 1) AS costume_item_photos_next FROM public.costume_item_photos
\gset

ALTER TABLE public.productions ALTER COLUMN id RESTART WITH :productions_next;
ALTER TABLE public.actors ALTER COLUMN id RESTART WITH :actors_next;
ALTER TABLE public.item_types ALTER COLUMN id RESTART WITH :item_types_next;
ALTER TABLE public.costume_items ALTER COLUMN id RESTART WITH :costume_items_next;
ALTER TABLE public.costume_item_photos ALTER COLUMN id RESTART WITH :costume_item_photos_next;

INSERT INTO public.sqlite_migration_source (id, source_sha256)
VALUES (1, '$SOURCE_SHA256')
ON CONFLICT (id) DO NOTHING;
COMMIT;
SQL

if ! psql_target --file "$LOAD_SQL" >"$WORK_DIR/load-target.out" 2>"$WORK_DIR/postgres-error.log"; then
    die "transactional PostgreSQL load failed; previous target data was retained"
fi
if [[ $(<"$WORK_DIR/load-target.out") == migration_already_complete ]]; then
    printf 'Migration already complete; source SHA-256 marker matches.\n'
    exit 0
fi

SOURCE_SHA256_AFTER_LOAD=$(checksum_file "$SOURCE_SQLITE") || die "cannot re-checksum source SQLite file after load"
[[ $SOURCE_SHA256_AFTER_LOAD == "$SOURCE_SHA256" ]] || die "source SQLite file changed during migration"

for (( index = 0; index < ${#TABLES[@]}; index++ )); do
    printf '%s: source=%s target=%s\n' "${TABLES[$index]}" "${SOURCE_COUNTS[$index]}" "${SOURCE_COUNTS[$index]}"
done
printf 'Migration complete; source SHA-256 marker stored.\n'
