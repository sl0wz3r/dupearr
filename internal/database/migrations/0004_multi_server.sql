-- 0004_multi_server: several Plex servers (issue #8, docs/DECISIONS.md D11). Additive only.
--
-- media_servers.storage: '' (the server may share storage with the other servers; its paths are
-- compared) or 'separate' (another host or a friend's server: only its mapped folders are
-- compared, never its raw paths or file names).
ALTER TABLE media_servers ADD COLUMN storage TEXT NOT NULL DEFAULT '';

-- *arr instance ↔ media server links: which Plex servers an instance feeds. With two or more
-- enabled servers an instance's files are matched by raw path or by name and size only to versions
-- of a linked server, and only once a person confirmed the links (links_confirmed = 1). An older
-- database (or a restored older backup) starts with unconfirmed links, the fail-closed state; the
-- data upgrade links every instance to the only enabled server of a one-server installation.
ALTER TABLE arr_instances ADD COLUMN links_confirmed INTEGER NOT NULL DEFAULT 0;
CREATE TABLE arr_server_links (
    arr_id    INTEGER NOT NULL REFERENCES arr_instances(id) ON DELETE CASCADE,
    server_id INTEGER NOT NULL REFERENCES media_servers(id) ON DELETE CASCADE,
    PRIMARY KEY (arr_id, server_id)
);
CREATE INDEX idx_arr_server_links_server ON arr_server_links(server_id);

-- The scan's cross-server record of a group (models.CrossServerRecord as JSON; '' = none: a group
-- stored before this migration, or by a scan with one enabled server).
ALTER TABLE duplicate_groups ADD COLUMN cross_server TEXT NOT NULL DEFAULT '';
