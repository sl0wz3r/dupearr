package scanner

import (
	"testing"

	"github.com/sl0wz3r/dupearr/internal/integrations/arr"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/pathmap"
)

// The research's throwaway tests (docs/research/multi-server.md §9.1) turned into regression tests
// with the opposite expectation: with two servers, a version of server 2 (no mapping) never matches
// the mapped Radarr file by raw path or by name and size (scenario D).

const heatPath = "/data/media/movies/Heat (1995)/Heat (1995).mkv"

func multiMatcher(pol MatchPolicy) *Matcher {
	m := NewMatcher(pathmap.New([]models.PathMapping{
		{SourceType: models.PathSourceServer, SourceID: 1, RemotePath: "/data/media", LocalPath: "/data/media"},
		{SourceType: models.PathSourceArr, SourceID: 10, RemotePath: "/data/media", LocalPath: "/data/media"},
	}))
	m.Add(10, []arr.TrackedFile{{Path: heatPath, Size: 100, Info: models.ArrFileInfo{InstanceID: 10, FileID: 7}}})
	m.SetServers(pol)
	return m
}

func server2(path string) *models.MediaVersion {
	return &models.MediaVersion{Key: "plex:2:1", ServerID: 2, Parts: []models.MediaPart{{Path: path, Size: 100}}}
}

func TestMatcherMultiServerMappedArrUnmappedServer(t *testing.T) {
	policies := map[string]MatchPolicy{
		"links not confirmed": {Multi: true},
		"linked and confirmed": {Multi: true, Confirmed: map[int64]bool{10: true},
			Links: map[int64]map[int64]bool{10: {1: true, 2: true}}},
		"server 2 separate": {Multi: true, Confirmed: map[int64]bool{10: true},
			Links: map[int64]map[int64]bool{10: {1: true, 2: true}}, Separate: map[int64]bool{2: true}},
	}
	for name, pol := range policies {
		t.Run(name, func(t *testing.T) {
			m := multiMatcher(pol)
			for _, path := range []string{heatPath, "/srv/films/Heat (1995)/Heat (1995).mkv"} {
				tf, kind, unknown := m.match(2, server2(path))
				if tf != nil || kind != matchNone || unknown != unknownUnmapped {
					t.Fatalf("%s: match %+v kind %d unknown %q", path, tf, kind, unknown)
				}
			}
			// Server 1 is mapped: rule 1 works for any server.
			v := &models.MediaVersion{Key: "plex:1:1", ServerID: 1, Parts: []models.MediaPart{{Path: heatPath, Size: 100}}}
			if tf, kind, _ := m.match(1, v); tf == nil || kind != matchLocal {
				t.Fatalf("rule 1: %+v %d", tf, kind)
			}
		})
	}
}

func TestMatcherOneServerUnchanged(t *testing.T) {
	// NewMatcher alone (one server): today's rules 2 and 3.
	m := NewMatcher(pathmap.New([]models.PathMapping{
		{SourceType: models.PathSourceArr, SourceID: 10, RemotePath: "/data/media", LocalPath: "/data/media"},
	}))
	m.Add(10, []arr.TrackedFile{{Path: heatPath, Size: 100, Info: models.ArrFileInfo{InstanceID: 10, FileID: 7}}})
	if tf, kind, unknown := m.match(2, server2(heatPath)); tf == nil || kind != matchRaw || unknown != "" {
		t.Fatalf("raw path: %+v %d %q", tf, kind, unknown)
	}
	if tf, kind, _ := m.match(2, server2("/srv/films/Heat (1995)/Heat (1995).mkv")); tf == nil || kind != matchName {
		t.Fatalf("name and size: %+v %d", tf, kind)
	}
}

// Neither side mapped: rules 2 and 3 only between a confirmed, linked instance and a server that
// is not separate.
func TestMatcherMultiServerLinks(t *testing.T) {
	unmapped := func(pol MatchPolicy) *Matcher {
		m := NewMatcher(pathmap.New(nil))
		m.Add(10, []arr.TrackedFile{{Path: heatPath, Size: 100, Info: models.ArrFileInfo{InstanceID: 10, FileID: 7}}})
		m.SetServers(pol)
		return m
	}
	cases := []struct {
		name string
		pol  MatchPolicy
		want matchKind
	}{
		{"not confirmed", MatchPolicy{Multi: true, Links: map[int64]map[int64]bool{10: {2: true}}}, matchNone},
		{"confirmed, linked", MatchPolicy{Multi: true, Confirmed: map[int64]bool{10: true}, Links: map[int64]map[int64]bool{10: {2: true}}}, matchRaw},
		{"confirmed, not linked", MatchPolicy{Multi: true, Confirmed: map[int64]bool{10: true}, Links: map[int64]map[int64]bool{10: {1: true}}}, matchNone},
		{"confirmed, linked, separate", MatchPolicy{Multi: true, Confirmed: map[int64]bool{10: true},
			Links: map[int64]map[int64]bool{10: {2: true}}, Separate: map[int64]bool{2: true}}, matchNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := unmapped(c.pol)
			tf, kind, unknown := m.match(2, server2(heatPath))
			if kind != c.want || (c.want == matchNone && tf != nil) || unknown != "" {
				t.Fatalf("raw: %+v %d %q", tf, kind, unknown)
			}
			wantName := matchNone
			if c.want != matchNone {
				wantName = matchName
			}
			if _, kind, _ := m.match(2, server2("/other/Heat (1995).mkv")); kind != wantName {
				t.Fatalf("name: %d", kind)
			}
		})
	}
}

// Full-disc versions (docs/DECISIONS.md D11): a disc image or a disc folder of server 2 (no
// mapping) whose raw path holds the mapped Radarr file is unknown too, never untracked.
func TestMatcherMultiServerDiscUnknown(t *testing.T) {
	const discRoot = "/data/media/movies/Heat (1995)"
	m := NewMatcher(pathmap.New([]models.PathMapping{
		{SourceType: models.PathSourceArr, SourceID: 10, RemotePath: "/data/media", LocalPath: "/data/media"},
	}))
	m.Add(10, []arr.TrackedFile{
		{Path: discRoot + "/Heat.iso", Size: 100, Info: models.ArrFileInfo{InstanceID: 10, FileID: 7}},
		{Path: discRoot + "/BDMV/STREAM/00800.m2ts", Size: 50, Info: models.ArrFileInfo{InstanceID: 10, FileID: 8}},
	})
	image := &models.MediaVersion{Key: "plex:2:1", ServerID: 2, Parts: []models.MediaPart{{Path: discRoot + "/Heat.iso", Size: 100}},
		Disc: &models.DiscInfo{Type: models.DiscISO, Root: discRoot + "/Heat.iso"}}
	folder := &models.MediaVersion{Key: "plex:2:2", ServerID: 2, Parts: []models.MediaPart{{Path: discRoot + "/BDMV/index.bdmv", Size: 1}},
		Disc: &models.DiscInfo{Type: models.DiscBluray, Root: discRoot}}
	for _, pol := range []MatchPolicy{{Multi: true}, {Multi: true, Confirmed: map[int64]bool{10: true}, Links: map[int64]map[int64]bool{10: {2: true}}}} {
		m.SetServers(pol)
		for _, v := range []*models.MediaVersion{image, folder} {
			if tf, _, unknown := m.matchDisc(2, v); tf != nil || unknown != unknownUnmapped {
				t.Fatalf("%s (%+v): %+v unknown %q", v.Key, pol, tf, unknown)
			}
		}
	}
	// One server: today's raw-path matches.
	m.SetServers(MatchPolicy{})
	for _, v := range []*models.MediaVersion{image, folder} {
		if tf, kind, unknown := m.matchDisc(2, v); tf == nil || kind != matchRaw || unknown != "" {
			t.Fatalf("one server %s: %+v %d %q", v.Key, tf, kind, unknown)
		}
	}
}
