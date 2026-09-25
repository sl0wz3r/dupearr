package api

import (
	"context"
	"sort"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// arrWebLink is one Radarr movie or Sonarr series of a duplicate group in the *arr's web UI
// (GET /api/v1/duplicate/{id} "arrLinks"): its page, when the last scan stored the page's slug,
// and the instance's Activity → Queue page. Links are built on every request from the instance's
// current External URL (or URL) and are never requested by Dupearr; they carry no credentials.
type arrWebLink struct {
	InstanceID   int64          `json:"instanceId"`
	InstanceName string         `json:"instanceName"`
	Kind         models.ArrKind `json:"kind"`
	ItemID       int64          `json:"itemId"`
	ItemURL      string         `json:"itemUrl,omitempty"`
	QueueURL     string         `json:"queueUrl"`
}

// arrWebLinks returns the links of g's *arr items (g.ArrItems and the items its versions are
// tracked by), sorted by instance and item id. Items of an instance that no longer exists (or is
// now of another kind), and of an instance whose web address is unusable (arr.WebBase), get none.
func (s *Server) arrWebLinks(ctx context.Context, g *models.DuplicateGroup) ([]arrWebLink, error) {
	type key struct{ inst, item int64 }
	refs := map[key]models.ArrItemRef{}
	var keys []key
	add := func(ref models.ArrItemRef) {
		k := key{ref.InstanceID, ref.ItemID}
		if k.inst <= 0 || k.item <= 0 {
			return
		}
		if prev, ok := refs[k]; ok {
			if prev.TitleSlug == "" {
				refs[k] = ref
			}
			return
		}
		refs[k] = ref
		keys = append(keys, k)
	}
	for _, it := range g.ArrItems {
		add(it)
	}
	for i := range g.Files {
		if a := g.Files[i].Version.Arr; a != nil {
			add(models.ArrItemRef{InstanceID: a.InstanceID, InstanceName: a.InstanceName, Kind: a.Kind, ItemID: a.ItemID})
		}
	}
	out := []arrWebLink{}
	if len(keys) == 0 {
		return out, nil
	}
	insts, err := s.d.Store.ArrInstances().List(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]models.ArrInstance, len(insts))
	for _, a := range insts {
		byID[a.ID] = a
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].inst != keys[j].inst {
			return keys[i].inst < keys[j].inst
		}
		return keys[i].item < keys[j].item
	})
	for _, k := range keys {
		ref := refs[k]
		inst, ok := byID[k.inst]
		if !ok || (ref.Kind != "" && ref.Kind != inst.Kind) {
			continue
		}
		base, ok := arr.WebBase(inst)
		if !ok {
			continue
		}
		out = append(out, arrWebLink{
			InstanceID:   inst.ID,
			InstanceName: inst.Name,
			Kind:         inst.Kind,
			ItemID:       k.item,
			ItemURL:      arr.ItemWebURL(base, inst.Kind, ref.TitleSlug),
			QueueURL:     arr.QueueWebURL(base),
		})
	}
	return out, nil
}
