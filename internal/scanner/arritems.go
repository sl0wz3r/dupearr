package scanner

import (
	"sort"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// The *arr items of a group (DuplicateGroup.ArrItems): where each Radarr movie / Sonarr series is
// in the *arr's web UI and, while it has download/import queue entries, what they are. Display
// only — the queue deferral itself is decided exactly as before, from the queue's item ids
// (queueState, busyItems) — so a group stuck in "deferred" can say which download holds it.

// arrItemKey identifies an *arr item: instance id + movie id (Radarr) or series id (Sonarr).
type arrItemKey struct{ inst, item int64 }

// arrItemInfo is what the enrichment read about an *arr item.
type arrItemInfo struct {
	name  string
	kind  models.ArrKind
	slug  string
	queue *arr.QueueItem // nil = no queue entries
}

// noteArrItems records the items of the tracked files of one instance, with their queue entries.
func (p *pipeline) noteArrItems(inst models.ArrInstance, files []arr.TrackedFile, queue map[int64]*arr.QueueItem) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range files {
		id := files[i].Info.ItemID
		if id <= 0 {
			continue
		}
		key := arrItemKey{inst: inst.ID, item: id}
		if info := p.arrItems[key]; info != nil {
			if info.slug == "" {
				info.slug = files[i].TitleSlug
			}
			continue
		}
		p.arrItems[key] = &arrItemInfo{name: inst.Name, kind: inst.Kind, slug: files[i].TitleSlug, queue: queue[id]}
	}
}

// groupArrItems returns the *arr items of g (sorted by instance and item id): the items its
// versions are tracked by and the items that made one of its media-server items busy (a title in
// the queue although none of its versions matched a tracked file). Queue summaries are attached to
// busy items only; texts are masked for secrets (logging.Redact: the messages come from the *arr
// and its download client) and capped.
func (p *pipeline) groupArrItems(g *models.DuplicateGroup) []models.ArrItemRef {
	var keys []arrItemKey
	seen := map[arrItemKey]bool{}
	fallback := map[arrItemKey]*models.ArrFileInfo{}
	add := func(k arrItemKey) {
		if k.inst > 0 && k.item > 0 && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for i := range g.Files {
		v := &g.Files[i].Version
		if a := v.Arr; a != nil {
			k := arrItemKey{inst: a.InstanceID, item: a.ItemID}
			add(k)
			if fallback[k] == nil {
				fallback[k] = a
			}
		}
		for _, k := range p.busyItems[refKey{server: v.ServerID, rk: v.RatingKey}] {
			add(k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].inst != keys[j].inst {
			return keys[i].inst < keys[j].inst
		}
		return keys[i].item < keys[j].item
	})
	out := make([]models.ArrItemRef, 0, len(keys))
	for _, k := range keys {
		ref := models.ArrItemRef{InstanceID: k.inst, ItemID: k.item}
		if a := fallback[k]; a != nil {
			ref.InstanceName, ref.Kind = a.InstanceName, a.Kind
		}
		if info := p.arrItems[k]; info != nil {
			ref.InstanceName, ref.Kind, ref.TitleSlug = info.name, info.kind, info.slug
			if q := info.queue; q != nil {
				ref.QueueCount = q.Count
				for _, e := range q.Entries {
					ref.Queue = append(ref.Queue, queueEntryModel(e))
				}
			}
		}
		out = append(out, ref)
	}
	return models.SanitizeArrItems(out)
}

// queueEntryModel converts a queue summary for storage. arr.Queue masked the secrets before it
// capped the texts; masking again here (Redact is idempotent) covers any other ArrClient.
func queueEntryModel(e arr.QueueEntry) models.ArrQueueEntry {
	out := models.ArrQueueEntry{
		Title:                 logging.Redact(e.Title),
		Status:                e.Status,
		TrackedDownloadState:  e.TrackedDownloadState,
		TrackedDownloadStatus: e.TrackedDownloadStatus,
		Label:                 e.Label(),
		ErrorMessage:          logging.Redact(e.ErrorMessage),
	}
	for _, m := range e.Messages {
		out.Messages = append(out.Messages, logging.Redact(m))
	}
	return out
}
