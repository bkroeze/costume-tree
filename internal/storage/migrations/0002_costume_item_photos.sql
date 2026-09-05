CREATE TABLE costume_item_photos (
    id INTEGER PRIMARY KEY,
    production_id INTEGER NOT NULL,
    costume_item_id INTEGER NOT NULL,
    original_name TEXT NOT NULL
        CHECK (length(trim(original_name)) > 0
            AND original_name NOT IN ('.', '..')
            AND instr(original_name, '/') = 0
            AND instr(original_name, char(92)) = 0
            AND instr(original_name, char(0)) = 0),
    display_name TEXT NOT NULL
        CHECK (length(trim(display_name)) > 0
            AND display_name NOT IN ('.', '..')
            AND instr(display_name, '/') = 0
            AND instr(display_name, char(92)) = 0
            AND instr(display_name, char(0)) = 0),
    thumbnail_name TEXT NOT NULL
        CHECK (length(trim(thumbnail_name)) > 0
            AND thumbnail_name NOT IN ('.', '..')
            AND instr(thumbnail_name, '/') = 0
            AND instr(thumbnail_name, char(92)) = 0
            AND instr(thumbnail_name, char(0)) = 0),
    media_type TEXT NOT NULL CHECK (length(trim(media_type)) > 0),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'ready', 'failed')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    FOREIGN KEY (production_id, costume_item_id)
        REFERENCES costume_items (production_id, id) ON DELETE RESTRICT
);

CREATE INDEX idx_costume_item_photos_item
    ON costume_item_photos (production_id, costume_item_id, created_at, id);
CREATE INDEX idx_costume_item_photos_pending
    ON costume_item_photos (status, created_at, id)
    WHERE status = 'pending';
