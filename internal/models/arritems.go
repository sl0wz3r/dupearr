package models

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ArrItemRef is one Radarr movie or Sonarr series of a duplicate group (DuplicateGroup.ArrItems):
// an item one of the group's versions is tracked by, or whose download queue deferred the group.
// It says where the item is in the *arr's web UI and, while the item has download/import queue
// entries, what they are. It is display only: no decision, status or flag is derived from it.
type ArrItemRef struct {
	InstanceID   int64   `json:"instanceId"`
	InstanceName string  `json:"instanceName"`
	Kind         ArrKind `json:"kind"`
	ItemID       int64   `json:"itemId"` // movieId (Radarr) / seriesId (Sonarr)
	// TitleSlug is the item's page in the *arr's web UI (/movie/<slug>, /series/<slug>): Radarr's
	// titleSlug is the TMDB id, Sonarr's a slug such as "the-expanse". "" when not known.
	TitleSlug string `json:"titleSlug,omitempty"`
	// QueueCount is the number of queue entries of the item when the group was scanned (0 = none);
	// Queue summarizes the first of them (at most MaxArrQueueEntries).
	QueueCount int             `json:"queueCount,omitempty"`
	Queue      []ArrQueueEntry `json:"queue,omitempty"`
}

// ArrQueueEntry summarizes one entry of an *arr's download/import queue (Activity → Queue) as the
// *arr shows it: the release, its state and the *arr's own messages. Download ids, download client
// and indexer names and output paths are never recorded.
type ArrQueueEntry struct {
	Title                 string `json:"title"` // release title
	Status                string `json:"status,omitempty"`
	TrackedDownloadState  string `json:"trackedDownloadState,omitempty"`
	TrackedDownloadStatus string `json:"trackedDownloadStatus,omitempty"`
	// Label is the status as the *arr's queue page words it, e.g. "Downloaded - Waiting to Import".
	Label        string   `json:"label,omitempty"`
	Messages     []string `json:"messages,omitempty"`
	ErrorMessage string   `json:"errorMessage,omitempty"`
}

// Caps of the *arr item summaries. They are applied when a scan records the summaries and again
// whenever a stored group is read (SanitizeArrItems), because a restored backup is not trusted: the
// summaries are only displayed, so oversized or malformed values are shortened or dropped, never
// refused.
const (
	MaxArrItems           = 20  // items per group
	MaxArrQueueEntries    = 10  // entries per item (QueueCount still counts every entry)
	MaxArrQueueMessages   = 5   // messages per entry
	MaxArrQueueTitleRunes = 200 // a release title
	MaxArrQueueTextRunes  = 300 // one message, the error message
	MaxArrSlugRunes       = 200 // a title slug
	maxArrNameRunes       = 100 // an instance name
	maxArrWordRunes       = 64  // a status, state or label
)

// CleanArrText returns s on one line (control and format characters dropped, runs of white space
// collapsed, trimmed) and at most limit runes long, ending with "…" when shortened.
func CleanArrText(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r), r == utf8.RuneError, unicode.Is(unicode.Cf, r):
			// Format characters (bidi overrides, zero-width joiners) could make a message read
			// differently from what it says.
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
		if b.Len() > 4*limit {
			break // more than limit runes already: the rest is cut anyway
		}
	}
	return truncateRunes(b.String(), limit)
}

// truncateRunes shortens s to at most limit runes, ending with "…" when cut.
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	r := []rune(s)
	if limit <= 1 {
		return "…"
	}
	return strings.TrimRightFunc(string(r[:limit-1]), unicode.IsSpace) + "…"
}

// SanitizeArrItems returns items within the caps above: text cleaned and shortened, items without
// an instance or item id dropped, at most MaxArrItems items of MaxArrQueueEntries entries each.
// It returns nil when nothing is left.
func SanitizeArrItems(items []ArrItemRef) []ArrItemRef {
	var out []ArrItemRef
	for _, it := range items {
		if it.InstanceID <= 0 || it.ItemID <= 0 {
			continue
		}
		if len(out) == MaxArrItems {
			break
		}
		it.InstanceName = CleanArrText(it.InstanceName, maxArrNameRunes)
		it.Kind = ArrKind(CleanArrText(string(it.Kind), maxArrWordRunes))
		it.TitleSlug = CleanArrText(it.TitleSlug, MaxArrSlugRunes)
		var queue []ArrQueueEntry
		for _, e := range it.Queue {
			if len(queue) == MaxArrQueueEntries {
				break
			}
			queue = append(queue, sanitizeArrQueueEntry(e))
		}
		it.Queue = queue
		it.QueueCount = max(it.QueueCount, len(it.Queue))
		out = append(out, it)
	}
	return out
}

func sanitizeArrQueueEntry(e ArrQueueEntry) ArrQueueEntry {
	out := ArrQueueEntry{
		Title:                 CleanArrText(e.Title, MaxArrQueueTitleRunes),
		Status:                CleanArrText(e.Status, maxArrWordRunes),
		TrackedDownloadState:  CleanArrText(e.TrackedDownloadState, maxArrWordRunes),
		TrackedDownloadStatus: CleanArrText(e.TrackedDownloadStatus, maxArrWordRunes),
		Label:                 CleanArrText(e.Label, maxArrWordRunes),
		ErrorMessage:          CleanArrText(e.ErrorMessage, MaxArrQueueTextRunes),
	}
	for _, m := range e.Messages {
		if len(out.Messages) == MaxArrQueueMessages {
			break
		}
		if m = CleanArrText(m, MaxArrQueueTextRunes); m != "" && !slices.Contains(out.Messages, m) {
			out.Messages = append(out.Messages, m)
		}
	}
	return out
}
