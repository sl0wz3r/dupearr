-- 0002_retained_overrides: keep users' overrides of versions that temporarily leave a group.
--
-- A version drops out of a group whenever a scan does not see it as a candidate (e.g. Plex marks
-- its file unavailable while a NAS share is offline). Its group_files row is deleted, and so was
-- its override: when the version came back, a user's explicit "keep" was gone and the version
-- could be decided (and, in auto mode, approved) for removal. The override is now retained here,
-- as a JSON object version_key -> "keep" | "remove", and restored when the version reappears.
ALTER TABLE duplicate_groups ADD COLUMN retained_overrides TEXT NOT NULL DEFAULT '{}';
