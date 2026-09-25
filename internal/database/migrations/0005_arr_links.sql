-- 0005_arr_links: links to Radarr/Sonarr and explained queue deferrals. Additive only.
--
-- arr_instances.external_url: the address a browser opens the *arr at (with its URL base), used
-- only to build "Open in Radarr/Sonarr" links; '' = the connection URL. Dupearr never sends a
-- request to it, and links are only built from an http(s) value without credentials.
ALTER TABLE arr_instances ADD COLUMN external_url TEXT NOT NULL DEFAULT '';

-- The *arr items of a group as its last scan found them (models.ArrItemRef list as JSON; '' = none:
-- a group without *arr items, or stored before this migration): each item's page slug and, while
-- its download/import queue deferred the group, a short summary of the queue entries. Display
-- only; a value that cannot be read is ignored.
ALTER TABLE duplicate_groups ADD COLUMN arr_items TEXT NOT NULL DEFAULT '';
