-- 0003_tautulli: Tautulli connections, the play-history source of the played / last_played
-- criteria (docs/DECISIONS.md D10).
--
-- One Tautulli per media server (UNIQUE server_id): Tautulli monitors exactly one Plex server, and
-- its identity is checked against that server's machine identifier on every scan. Deleting the
-- media server deletes its Tautulli connection. The history itself is not stored here: each scan
-- reads it and keeps a snapshot per version in group_files.version (MediaVersion.watch).
CREATE TABLE tautulli_instances (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    server_id  INTEGER NOT NULL UNIQUE REFERENCES media_servers(id) ON DELETE CASCADE,
    url        TEXT    NOT NULL,
    api_key    TEXT    NOT NULL DEFAULT '',
    verify_tls INTEGER NOT NULL DEFAULT 1,
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);
