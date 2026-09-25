package models

import (
	"slices"
	"strconv"
	"strings"
)

// Media-server keys (docs/ARCHITECTURE.md §4.1, §4.2). A version key names one version on one
// configured server, "<kind>:<serverID>:<versionID>"; an item key names one item the same way and
// is the group-key fallback and the "@<kind>:<serverID>:<itemID>" disambiguation of colliding
// group keys. For Plex (kind "plex" or "") these are exactly the keys stored since the first
// release: "plex:<serverID>:<mediaID>" and "plex:<serverID>:<ratingKey>". Keys decide group
// identity, signatures and stable counts, so their bytes must never change for a stored kind.

// keyKinds are the kinds whose prefix a stored key can carry, one per prefix. The disambiguation
// parser only recognises these: accepting any "@x:" would cut a base key that happens to contain
// one. They come from SupportedMediaServerKinds, so adding a kind there also makes its "@<kind>:"
// suffix and version keys recognised (the exclusion forms, stripDisambiguation and the scanner's
// kind of a disambiguated key). A kind that is ever dropped from that list while keys of it are
// still stored must stay here.
func keyKinds() []MediaServerKind {
	var out []MediaServerKind
	for _, s := range SupportedMediaServerKinds() {
		k := MediaServerKind(s)
		if k == "" {
			k = MediaServerPlex // "" is Plex and carries its prefix
		}
		if !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// VersionKey returns the key of a version on a media server: "<kind>:<serverID>:<versionID>"
// (Plex: "plex:<serverID>:<mediaID>").
func VersionKey(kind MediaServerKind, serverID int64, versionID string) string {
	return kind.KeyPrefix() + ":" + strconv.FormatInt(serverID, 10) + ":" + versionID
}

// ServerItemKey returns the key of an item on a media server: "<kind>:<serverID>:<itemID>" (Plex:
// "plex:<serverID>:<ratingKey>"). Callers pass a trimmed id (MediaItem.KeyItemID).
func ServerItemKey(kind MediaServerKind, serverID int64, itemID string) string {
	return kind.KeyPrefix() + ":" + strconv.FormatInt(serverID, 10) + ":" + itemID
}

// DisambiguationIndex returns where the first "@<kind>:" disambiguation marker starts in s and
// the marker's length (i = -1, n = 0 when s has none). Only the kinds stored keys can carry are
// recognised (keyKinds).
func DisambiguationIndex(s string) (i, n int) {
	i = -1
	for _, k := range keyKinds() {
		m := "@" + k.KeyPrefix() + ":"
		if j := strings.Index(s, m); j >= 0 && (i < 0 || j < i) {
			i, n = j, len(m)
		}
	}
	return i, n
}

// KindOfVersionKey returns the media-server kind a version key names ("plex:1:2" → plex). A key of
// another form (a disc found on disk, "disc:…") or of an unknown kind returns false.
func KindOfVersionKey(key string) (MediaServerKind, bool) {
	for _, k := range keyKinds() {
		if strings.HasPrefix(key, k.KeyPrefix()+":") {
			return k, true
		}
	}
	return "", false
}

// KeyItemID is the item id the item's keys are built from: KeyID when the client set a stable one,
// else the rating key (both trimmed).
func (it *MediaItem) KeyItemID() string {
	if id := strings.TrimSpace(it.KeyID); id != "" {
		return id
	}
	return strings.TrimSpace(it.RatingKey)
}

// KeyItemID is the item id of the version's item as its keys use it: ItemKeyID when the item had a
// stable KeyID, else the rating key (both trimmed).
func (v *MediaVersion) KeyItemID() string {
	if id := strings.TrimSpace(v.ItemKeyID); id != "" {
		return id
	}
	return strings.TrimSpace(v.RatingKey)
}

// ServerVersionID is the server's id of the version, the last field of its version key: the Plex
// media id when set, else the source id of another kind (Jellyfin: the media source id), else ""
// (a disc found on disk has none). A version gets a version key exactly when it is non-empty (the
// scanner's decorate, the engine's fillVersionIdentity), so another kind's version id is added
// here and nowhere else.
func (v *MediaVersion) ServerVersionID() string {
	if v.MediaID > 0 {
		return strconv.FormatInt(v.MediaID, 10)
	}
	return strings.TrimSpace(v.SourceID)
}
