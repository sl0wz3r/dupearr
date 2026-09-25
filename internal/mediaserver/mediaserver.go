// Package mediaserver is the kind-neutral media-server contract (docs/CONTRACTS.md
// "internal/mediaserver", docs/research/jellyfin-emby.md §5.2): the listing types, the read-only
// Client every media-server integration implements, and the optional capabilities only some
// servers have. The scanner, the executor and the health checks depend on this contract, so a
// client for another kind of server can be added without touching the Plex code paths.
//
// The core Client is read-only: it never deletes, changes or notifies anything. Everything that
// changes a server (deleting one version, refreshing an item, scanning a folder, reporting changed
// paths) is an optional capability found by a type assertion where it is used. A client that does
// not implement a capability makes the feature that needs it structurally unavailable (for
// VersionDeleter: the "plex" deletion method), never a fallback to another call. Plex implements
// VersionDeleter, ItemRefresher and FolderScanner; Jellyfin (a read-only source,
// docs/DECISIONS.md D12) implements only ChangeNotifier and RemovalGate.
//
// The types carry the Plex-era names (RatingKey, Sections, AllItems) because the Plex client was the
// first implementation and existing fakes and stored data use them; their meaning is neutral and
// documented on each type. The integrations/plex types are aliases of these.
//
// This package holds types and contracts only: no I/O, and no import of any other Dupearr package
// but models.
package mediaserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/models"
)

// Identity is a server's identity.
type Identity struct {
	// MachineIdentifier is the server's stable identity (Plex: machineIdentifier). Dupearr stores it
	// (models.MediaServer.MachineIdentifier) and refuses to act when a URL answers with another.
	MachineIdentifier, Version, FriendlyName string
}

// Section is a library of the server.
type Section struct {
	// Key is the server's library id (models.Library.SectionKey); Type is "movie" or "show" (a
	// client normalises its own library kinds to these; any other value is not synced).
	Key, Type, Title, UUID string
	Locations              []string
	// Refreshing reports a library scan in progress; ScannedAt and ContentChangedAt are Unix
	// seconds of the last scan and of the last change of the library's content. All three are 0 or
	// false when the server does not report them. With several media servers the executor compares
	// them with the scan's record right before a removal (docs/DECISIONS.md D11). A library that
	// reports 0 needs another change signal: for a non-Plex kind the scan records the
	// ListingFingerprint of the library's listing and the executor lists the library again; without
	// one every removal of a group the library could list is refused ("a change since the scan
	// cannot be ruled out").
	Refreshing       bool
	ScannedAt        int64
	ContentChangedAt int64
	// OtherVideo reports a library whose Type is not synced but which may list video files
	// (Jellyfin: mixed content, home videos, music videos, or a kind the client does not know).
	// Dupearr never lists such a library, so with several media servers it cannot know which files
	// it lists: the server counts as unread for the groups it may list files of, and a removal it
	// may list is refused (docs/DECISIONS.md D11, D12). Plex never sets it.
	OtherVideo bool
}

// ItemRef is a lightweight listing row of one movie or episode with every version and part.
type ItemRef struct {
	RatingKey string // the server's item id (Plex: rating key)
	MediaType models.MediaType
	Title     string
	Year      int
	ShowTitle string
	Season    int    // episodes: -1 when the server did not report the season (never confused with 0 = specials)
	Episode   int    // episodes: 0 when not reported
	GUID      string // Plex: plex://movie/… or legacy agent guid
	// ExternalIDs holds "tmdb"/"imdb"/"tvdb" (and, for Plex, "plex": the full plex:// GUID) when
	// present. For episodes these are episode-level ids. Never nil.
	ExternalIDs map[string]string
	MediaCount  int // number of NON-optimized media
	Media       []MediaRef
	AddedAt     time.Time
	// Shortcuts are local .strm files the item lists (Jellyfin lists a .strm to a local path as an
	// ordinary file source). They are never versions: the scanner reads their targets into the
	// shared-file index, so the file a shortcut points to is never removed.
	Shortcuts []PartRef
}

// MediaRef is one version of a listing row.
type MediaRef struct {
	ID         int64 // Plex media id (0 when the server's version id is not a number, see VersionID)
	Optimized  bool  // Plex: proxyType == 42, a "target" (optimizer profile) or a part under "/Plex Versions/"
	Width      int
	Height     int
	DurationMs int64
	Parts      []PartRef
	// VersionID is the server's version id when it is not a Plex media id ("" for Plex, whose
	// version id is ID). Use VersionIDOf.
	VersionID string
}

// PartRef is one file of a listed version.
type PartRef struct {
	ID   int64
	File string // path as the server sees it
	Size int64
	// ItemID is the server's item id of the part when it has one (Jellyfin: a stack part's own
	// item, named by a session while that part plays); "" for Plex.
	ItemID string
}

// VersionIDOf returns the server's id of a listed version: VersionID, else the Plex media id
// (the form version keys are built from, models.VersionKey).
func VersionIDOf(m *MediaRef) string {
	if m.VersionID != "" {
		return m.VersionID
	}
	return strconv.FormatInt(m.ID, 10)
}

// Client is what every media-server integration implements. It is read-only: no call deletes,
// changes or notifies anything (see the capabilities below).
type Client interface {
	// Identity reads the server's identity (it also validates the credential).
	Identity(ctx context.Context) (*Identity, error)
	// Sections lists the server's libraries with their root locations as the server sees them.
	Sections(ctx context.Context) ([]Section, error)
	// AllItems lists every movie (mt=movie) or episode (mt=episode) of a library with every
	// version and part. A listing that cannot be proven complete is an error, never a partial
	// result: the shared-file index and the resolution of groups depend on it.
	AllItems(ctx context.Context, sectionKey string, mt models.MediaType) ([]ItemRef, error)
	// Item fetches one item's detail with every version (file state when the server reports it).
	// ServerID, LibraryID and the version Keys are left to the caller. An item the server no
	// longer has is an error matching ErrNotFound.
	Item(ctx context.Context, itemID string) (*models.MediaItem, error)
	// ActiveSessions returns every id a session names, playing or paused: the item's id (Plex: the
	// rating key, the only id its sessions carry) and, for a kind whose sessions also name the
	// version or part being played, those ids too. The executor looks up the ids its playingKeys
	// and listingPlayingIDs return. The map is empty (not nil) when nothing plays; an answer that
	// cannot be read as a session list is an error, never "nothing is playing".
	ActiveSessions(ctx context.Context) (map[string]bool, error)
}

// Factory returns the client of a configured media server, or nil when there is none for its
// kind (a nil interface, never a typed nil).
type Factory func(s models.MediaServer) Client

// VersionDeleter is the capability to delete ONE version of an item on the server (Plex:
// DELETE /library/metadata/{rk}/media/{id}). Only a client that implements it can remove through
// the server (the "plex" deletion method); for any other client that method is unavailable.
type VersionDeleter interface {
	DeleteMedia(ctx context.Context, itemID string, mediaID int64) error
	// MediaDeletionAllowed reports the server's own permission for such deletes (Plex: "Allow
	// media deletion").
	MediaDeletionAllowed(ctx context.Context) (bool, error)
}

// ItemRefresher is the capability to refresh one item's metadata (Plex).
type ItemRefresher interface {
	RefreshItem(ctx context.Context, itemID string) error
}

// FolderScanner is the capability to scan one folder of a library (Plex partial scan).
type FolderScanner interface {
	ScanPath(ctx context.Context, sectionKey, dir string) error
}

// ChangeNotifier is the capability to report removed paths to the server (Jellyfin:
// POST /Library/Media/Updated). The executor calls it once per group after the removals, with the
// removed paths as the server sees them, after confirming the server's identity; a restore reports
// the restored paths (a client may offer a "created" variant, see the jellyfin package). It is the
// only way a server that never deletes through Dupearr drops the removed copy's entry.
type ChangeNotifier interface {
	NotifyChanged(ctx context.Context, libraryKey string, paths []string) error
}

// RemovalGate is the capability to say whether files this server lists may be removed at all
// (Jellyfin: no path substitutions set, the credential proven an API key or an administrator).
// RemovalProblem returns "" when they may, a non-empty reason when they may not (the groups are
// report-only), and an error when that cannot be told: unknown is never "no problem".
type RemovalGate interface {
	RemovalProblem(ctx context.Context) (string, error)
}

// ListingFingerprint identifies a library listing by what decides which files it lists: every
// row's id and every version's id, part paths, sizes and part item ids (SHA-256 over the sorted
// lines, hex). Equal listings give the same fingerprint whatever their order. It is the change
// signal of a library whose server reports no scan times (docs/DECISIONS.md D11): the scan records
// it and the executor lists the library again right before a removal.
func ListingFingerprint(refs []ItemRef) string {
	lines := make([]string, 0, len(refs))
	for i := range refs {
		r := &refs[i]
		row := strings.TrimSpace(r.RatingKey)
		for j := range r.Media {
			m := &r.Media[j]
			vid := VersionIDOf(m)
			for _, p := range m.Parts {
				lines = append(lines, strings.Join([]string{row, vid, p.File, strconv.FormatInt(p.Size, 10), p.ItemID}, "\x00"))
			}
			if len(m.Parts) == 0 {
				lines = append(lines, row+"\x00"+vid)
			}
		}
		for _, p := range r.Shortcuts {
			lines = append(lines, strings.Join([]string{row, "strm", p.File, strconv.FormatInt(p.Size, 10), p.ItemID}, "\x00"))
		}
		if len(r.Media) == 0 && len(r.Shortcuts) == 0 {
			lines = append(lines, row)
		}
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ErrNotFound is matched (errors.Is) by every client's "the server no longer has it" error.
var ErrNotFound = errors.New("media server: not found")
