package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTotalSizeOfADisc(t *testing.T) {
	v := MediaVersion{Parts: []MediaPart{{Size: 10}, {Size: 5}}}
	if got := v.TotalSize(); got != 15 {
		t.Fatalf("regular version: TotalSize = %d, want 15", got)
	}
	// A disc found on disk: one synthetic part per root, TotalBytes = every file of the disc.
	v.Disc = &DiscInfo{Type: DiscBluray, TotalBytes: 100}
	if got := v.TotalSize(); got != 100 {
		t.Fatalf("measured disc: TotalSize = %d, want TotalBytes 100", got)
	}
	// A disc a custom Plex scanner lists but Dupearr could not measure: the parts are all it knows.
	v.Disc.TotalBytes = 0
	if got := v.TotalSize(); got != 15 {
		t.Fatalf("unmeasured disc: TotalSize = %d, want the parts' 15", got)
	}
	if !v.IsDisc() || (&MediaVersion{}).IsDisc() {
		t.Fatal("IsDisc")
	}
	var nilVersion *MediaVersion
	if nilVersion.IsDisc() {
		t.Fatal("nil IsDisc")
	}
	if !(&DiscInfo{Type: DiscISO}).IsImage() || (&DiscInfo{Type: DiscDVD}).IsImage() {
		t.Fatal("IsImage")
	}
}

func TestDiscJSON(t *testing.T) {
	// A regular version has no "disc" member at all (older clients, stored rows).
	b, err := json.Marshal(MediaVersion{Key: "plex:1:2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"disc"`) {
		t.Fatalf("regular version serializes a disc: %s", b)
	}
	b, err = json.Marshal(MediaVersion{Key: "disc:1:ab", Disc: &DiscInfo{
		Type: DiscUHDBluray, Root: "/movies/M", LocalRoot: "/data/movies/M", Discs: 1, FileCount: 312,
		MainFeature: "BDMV/PLAYLIST/00800.mpls", Readable: true, Origin: DiscOriginFilesystem,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"uhd_bluray"`, `"root":"/movies/M"`, `"localRoot":"/data/movies/M"`,
		`"discs":1`, `"fileCount":312`, `"mainFeature":"BDMV/PLAYLIST/00800.mpls"`, `"readable":true`,
		`"origin":"filesystem"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("disc JSON lacks %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), "newestModTime") {
		t.Errorf("a zero modification time is serialized: %s", b)
	}
}

func TestDefaultSettingsDiscs(t *testing.T) {
	s := DefaultSettings()
	if !s.DetectDiscs || s.AllowDiscRemoval || !s.KeepPlayableCopy {
		t.Fatalf("disc defaults: detect=%v allowRemoval=%v keepPlayable=%v, want true/false/true",
			s.DetectDiscs, s.AllowDiscRemoval, s.KeepPlayableCopy)
	}
	// Documents stored by older versions lack the fields: decoding over the defaults keeps them.
	if err := json.Unmarshal([]byte(`{"dryRun":false}`), &s); err != nil {
		t.Fatal(err)
	}
	if !s.DetectDiscs || s.AllowDiscRemoval || !s.KeepPlayableCopy {
		t.Fatal("decoding an older document lost the disc defaults")
	}
}

// Several Plex servers (docs/DECISIONS.md D11): the new fields are absent from the JSON of a
// one-server installation, and a listing round-trips with nil and set pointers.
func TestMultiServerJSONOmittedWhenAbsent(t *testing.T) {
	v, err := json.Marshal(MediaVersion{Key: "plex:1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(v), "otherServers") {
		t.Fatalf("version JSON has otherServers: %s", v)
	}
	g, err := json.Marshal(DuplicateGroup{Key: "movie:tmdb:1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(g), "crossServer") {
		t.Fatalf("group JSON has crossServer: %s", g)
	}
	st, err := json.Marshal(ScanStats{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(st), "separateNameMatches") {
		t.Fatalf("scan stats JSON has separateNameMatches: %s", st)
	}
}

func TestOtherListingRoundTrip(t *testing.T) {
	yes := true
	for _, l := range []OtherListing{
		{ServerID: 2, VersionKey: "plex:2:9", Match: OtherSameFile},
		{ServerID: 2, VersionKey: "plex:2:9", Match: OtherPossiblySame, ItemKeepsAnother: &yes, KeptByGroup: &yes,
			Others: []OtherMedia{{MediaID: 10, VersionKey: "plex:2:10", Same: []string{"plex:1:1"}, Distinct: []string{"plex:1:2"}}}, Hint: "map it"},
	} {
		data, err := json.Marshal(MediaVersion{Key: "plex:1:1", OtherServers: []OtherListing{l}})
		if err != nil {
			t.Fatal(err)
		}
		var back MediaVersion
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back.OtherServers, []OtherListing{l}) {
			t.Fatalf("round trip %+v, want %+v", back.OtherServers, l)
		}
		// Unknown stays unknown (null), never false.
		if l.ItemKeepsAnother == nil && !strings.Contains(string(data), `"itemKeepsAnother":null`) {
			t.Fatalf("nil itemKeepsAnother encoded as %s", data)
		}
	}
}
