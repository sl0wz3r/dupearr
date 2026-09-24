-- 0001_init: initial Dupearr schema.
--
-- Conventions:
--   * times are UTC text in the fixed-width layout 2006-01-02T15:04:05.000000000Z (RFC3339 with
--     nanoseconds), so lexicographic order == chronological order; nullable times are NULL.
--   * JSON-ish values (slices, maps, nested structs) are JSON text columns that are never NULL.
--   * booleans are INTEGER 0/1.

CREATE TABLE settings (
    key   TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL COLLATE NOCASE UNIQUE,
    password_hash TEXT    NOT NULL,
    created_at    TEXT    NOT NULL
);

CREATE TABLE media_servers (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    name               TEXT    NOT NULL,
    kind               TEXT    NOT NULL,
    url                TEXT    NOT NULL,
    token              TEXT    NOT NULL DEFAULT '',
    machine_identifier TEXT    NOT NULL DEFAULT '',
    verify_tls         INTEGER NOT NULL DEFAULT 1,
    enabled            INTEGER NOT NULL DEFAULT 1,
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL
);

CREATE TABLE profiles (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    is_default  INTEGER NOT NULL DEFAULT 0,
    criteria    TEXT    NOT NULL DEFAULT '[]',
    keep_count  INTEGER NOT NULL DEFAULT 1,
    keep_per    TEXT    NOT NULL DEFAULT '',
    protections TEXT    NOT NULL DEFAULT '[]',
    created_at  TEXT    NOT NULL,
    updated_at  TEXT    NOT NULL
);
-- Safety net: at most one default profile can ever exist.
CREATE UNIQUE INDEX idx_profiles_single_default ON profiles(is_default) WHERE is_default = 1;

CREATE TABLE libraries (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   INTEGER NOT NULL REFERENCES media_servers(id) ON DELETE CASCADE,
    section_key TEXT    NOT NULL,
    title       TEXT    NOT NULL DEFAULT '',
    type        TEXT    NOT NULL DEFAULT '',
    locations   TEXT    NOT NULL DEFAULT '[]',
    enabled     INTEGER NOT NULL DEFAULT 1,
    profile_id  INTEGER REFERENCES profiles(id) ON DELETE SET NULL,
    scope_group TEXT    NOT NULL DEFAULT '',
    updated_at  TEXT    NOT NULL,
    UNIQUE (server_id, section_key)
);
CREATE INDEX idx_libraries_profile ON libraries(profile_id);

CREATE TABLE arr_instances (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    kind       TEXT    NOT NULL,
    url        TEXT    NOT NULL,
    api_key    TEXT    NOT NULL DEFAULT '',
    verify_tls INTEGER NOT NULL DEFAULT 1,
    enabled    INTEGER NOT NULL DEFAULT 1,
    tags       TEXT    NOT NULL DEFAULT '[]',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

-- source_id points at media_servers.id or arr_instances.id depending on source_type, so it cannot
-- be a foreign key; the repositories cascade deletes explicitly.
CREATE TABLE path_mappings (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    source_type TEXT    NOT NULL CHECK (source_type IN ('server', 'arr')),
    source_id   INTEGER NOT NULL,
    remote_path TEXT    NOT NULL,
    local_path  TEXT    NOT NULL
);
CREATE INDEX idx_path_mappings_source ON path_mappings(source_type, source_id);

CREATE TABLE duplicate_groups (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    key               TEXT    NOT NULL UNIQUE,
    status            TEXT    NOT NULL,
    status_reason     TEXT    NOT NULL DEFAULT '',
    media_type        TEXT    NOT NULL DEFAULT '',
    title             TEXT    NOT NULL DEFAULT '',
    show_title        TEXT    NOT NULL DEFAULT '',
    year              INTEGER NOT NULL DEFAULT 0,
    season            INTEGER NOT NULL DEFAULT 0,
    episode           INTEGER NOT NULL DEFAULT 0,
    server_id         INTEGER NOT NULL DEFAULT 0,
    library_ids       TEXT    NOT NULL DEFAULT '[]',
    flags             TEXT    NOT NULL DEFAULT '[]',
    external_ids      TEXT    NOT NULL DEFAULT '{}',
    thumb             TEXT    NOT NULL DEFAULT '',
    profile_id        INTEGER NOT NULL DEFAULT 0,
    reclaimable_bytes INTEGER NOT NULL DEFAULT 0,
    signature         TEXT    NOT NULL DEFAULT '',
    stable_count      INTEGER NOT NULL DEFAULT 0,
    -- lower-cased (Unicode-aware, computed in Go) "title\nshow_title" for case-insensitive search.
    search_text       TEXT    NOT NULL DEFAULT '',
    first_seen_at     TEXT    NOT NULL,
    last_seen_at      TEXT    NOT NULL,
    updated_at        TEXT    NOT NULL,
    last_scan_id      INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_duplicate_groups_status ON duplicate_groups(status);
CREATE INDEX idx_duplicate_groups_last_scan ON duplicate_groups(last_scan_id);
CREATE INDEX idx_duplicate_groups_last_seen ON duplicate_groups(last_seen_at);
CREATE INDEX idx_duplicate_groups_server ON duplicate_groups(server_id);

CREATE TABLE group_files (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id           INTEGER NOT NULL REFERENCES duplicate_groups(id) ON DELETE CASCADE,
    position           INTEGER NOT NULL DEFAULT 0,
    version_key        TEXT    NOT NULL,
    rating_key         TEXT    NOT NULL DEFAULT '',
    decision           TEXT    NOT NULL DEFAULT '',
    engine_decision    TEXT    NOT NULL DEFAULT '',
    override           TEXT    NOT NULL DEFAULT '',
    rank               INTEGER NOT NULL DEFAULT 0,
    protected          INTEGER NOT NULL DEFAULT 0,
    protected_reason   TEXT    NOT NULL DEFAULT '',
    deciding_criterion TEXT    NOT NULL DEFAULT '',
    reasons            TEXT    NOT NULL DEFAULT '[]',
    criterion_values   TEXT    NOT NULL DEFAULT '{}',
    version            TEXT    NOT NULL DEFAULT '{}',
    -- also serves lookups by group_id (leftmost column).
    UNIQUE (group_id, version_key)
);
CREATE INDEX idx_group_files_rating_key ON group_files(rating_key);

-- group_id / group_file_id are deliberately not foreign keys: actions are the audit trail of
-- removals (and hold recycle-bin locations for restores), so they must outlive group rows and the
-- re-scans that replace group files.
CREATE TABLE actions (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    group_id      INTEGER NOT NULL,
    group_file_id INTEGER NOT NULL DEFAULT 0,
    version_key   TEXT    NOT NULL DEFAULT '',
    title         TEXT    NOT NULL DEFAULT '',
    paths         TEXT    NOT NULL DEFAULT '[]',
    size          INTEGER NOT NULL DEFAULT 0,
    method        TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL,
    dry_run       INTEGER NOT NULL DEFAULT 0,
    message       TEXT    NOT NULL DEFAULT '',
    recycle_path  TEXT    NOT NULL DEFAULT '',
    permanent     INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL,
    started_at    TEXT,
    finished_at   TEXT
);
CREATE INDEX idx_actions_status ON actions(status, created_at);
CREATE INDEX idx_actions_group ON actions(group_id);

CREATE TABLE history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT    NOT NULL,
    group_id   INTEGER,
    action_id  INTEGER,
    title      TEXT    NOT NULL DEFAULT '',
    message    TEXT    NOT NULL DEFAULT '',
    data       TEXT,
    created_at TEXT    NOT NULL
);
CREATE INDEX idx_history_created ON history(created_at);
CREATE INDEX idx_history_event_type ON history(event_type, created_at);
CREATE INDEX idx_history_group ON history(group_id, created_at);

CREATE TABLE exclusions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    kind       TEXT    NOT NULL,
    value      TEXT    NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    reason     TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    UNIQUE (kind, value)
);

CREATE TABLE notifications (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    name     TEXT    NOT NULL,
    kind     TEXT    NOT NULL,
    settings TEXT    NOT NULL DEFAULT '{}',
    triggers TEXT    NOT NULL DEFAULT '[]',
    enabled  INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE scan_runs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    run_trigger TEXT    NOT NULL DEFAULT '',
    targeted    INTEGER NOT NULL DEFAULT 0,
    status      TEXT    NOT NULL,
    stats       TEXT    NOT NULL DEFAULT '{}',
    error       TEXT    NOT NULL DEFAULT '',
    started_at  TEXT    NOT NULL,
    finished_at TEXT
);

CREATE TABLE commands (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    name        TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '{}',
    status      TEXT    NOT NULL,
    run_trigger TEXT    NOT NULL DEFAULT '',
    message     TEXT    NOT NULL DEFAULT '',
    queued_at   TEXT    NOT NULL,
    started_at  TEXT,
    ended_at    TEXT,
    duration    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_commands_status ON commands(status);
CREATE INDEX idx_commands_queued ON commands(queued_at);

CREATE TABLE tasks (
    task_name        TEXT PRIMARY KEY NOT NULL,
    name             TEXT    NOT NULL DEFAULT '',
    interval_minutes INTEGER NOT NULL DEFAULT 0,
    last_execution   TEXT,
    last_start_time  TEXT,
    last_duration    TEXT    NOT NULL DEFAULT '',
    next_execution   TEXT
);
