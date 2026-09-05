PRAGMA foreign_keys = ON;

CREATE TABLE productions (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    archived_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (name)
);

CREATE TABLE actors (
    id INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    role TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    archived_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (production_id, id),
    UNIQUE (production_id, name),
    FOREIGN KEY (production_id) REFERENCES productions (id) ON DELETE RESTRICT
);

CREATE TABLE item_types (
    id INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL,
    name TEXT NOT NULL CHECK (length(trim(name)) > 0),
    archived_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (production_id, id),
    UNIQUE (production_id, name),
    FOREIGN KEY (production_id) REFERENCES productions (id) ON DELETE RESTRICT
);

CREATE TABLE production_item_sequences (
    production_id INTEGER PRIMARY KEY,
    next_value INTEGER NOT NULL DEFAULT 1 CHECK (next_value > 0),
    FOREIGN KEY (production_id) REFERENCES productions (id) ON DELETE RESTRICT
);

CREATE TABLE costume_items (
    id INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL,
    actor_id INTEGER NOT NULL,
    item_type_id INTEGER NOT NULL,
    code TEXT NOT NULL CHECK (length(trim(code)) > 0),
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'Not Started'
        CHECK (status IN ('Not Started', 'In Progress', 'Blocked', 'Complete')),
    progress INTEGER NOT NULL DEFAULT 0 CHECK (progress BETWEEN 0 AND 100),
    next_action TEXT NOT NULL DEFAULT '',
    blocker TEXT NOT NULL DEFAULT '',
    notes TEXT NOT NULL DEFAULT '',
    archived_at TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (production_id, id),
    UNIQUE (production_id, code),
    CHECK (status <> 'Complete' OR progress = 100),
    FOREIGN KEY (production_id, actor_id)
        REFERENCES actors (production_id, id) ON DELETE RESTRICT,
    FOREIGN KEY (production_id, item_type_id)
        REFERENCES item_types (production_id, id) ON DELETE RESTRICT
);

CREATE INDEX idx_productions_active ON productions (archived_at, name);
CREATE INDEX idx_actors_production_active ON actors (production_id, archived_at, name);
CREATE INDEX idx_item_types_production_active ON item_types (production_id, archived_at, name);
CREATE INDEX idx_costume_items_production_active
    ON costume_items (production_id, archived_at, id);
CREATE INDEX idx_costume_items_actor_active
    ON costume_items (production_id, actor_id, archived_at, id);
CREATE INDEX idx_costume_items_type_active
    ON costume_items (production_id, item_type_id, archived_at, id);
CREATE INDEX idx_costume_items_status_active
    ON costume_items (production_id, status, archived_at, id);
CREATE INDEX idx_costume_items_code
    ON costume_items (production_id, code);
