package models

import (
	"encoding/json"
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
