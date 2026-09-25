package models

import (
	"strings"
	"testing"
)

func TestCleanArrText(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"  Not an upgrade\n\tfor existing  file ", 100, "Not an upgrade for existing file"},
		{"bell\a and\x00nul", 100, "bell andnul"},
		{"bidi \u202eoverride\u200b", 100, "bidi override"},
		{"abcdef", 4, "abc…"},
		{"abcd", 4, "abcd"},
		{"ab   cd", 4, "ab…"},
		{"x", 0, ""},
		{"abc", 1, "…"},
		{"", 10, ""},
	}
	for _, tc := range cases {
		if got := CleanArrText(tc.in, tc.limit); got != tc.want {
			t.Errorf("CleanArrText(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}
	if got := CleanArrText(strings.Repeat("é", 10_000), 300); len([]rune(got)) != 300 {
		t.Errorf("long text: %d runes", len([]rune(got)))
	}
}

// SanitizeArrItems is applied to every stored group that is read: a restored backup may hold
// anything in duplicate_groups.arr_items.
func TestSanitizeArrItems(t *testing.T) {
	if got := SanitizeArrItems(nil); got != nil {
		t.Fatalf("nil = %v", got)
	}
	if got := SanitizeArrItems([]ArrItemRef{{InstanceID: 0, ItemID: 1}, {InstanceID: 1, ItemID: -2}}); got != nil {
		t.Fatalf("items without ids = %v, want nil", got)
	}
	long := strings.Repeat("y", 5000)
	entry := ArrQueueEntry{Title: long, Status: long, Label: "Downloaded\n- Waiting to Import", ErrorMessage: long,
		Messages: []string{"a", "a", "", "b", "c", "d", "e", "f", long}}
	var items []ArrItemRef
	for i := 1; i <= 30; i++ {
		it := ArrItemRef{InstanceID: 1, ItemID: int64(i), InstanceName: "Radarr\x1b[31m", Kind: ArrRadarr, TitleSlug: long, QueueCount: -5}
		for j := 0; j < 15; j++ {
			it.Queue = append(it.Queue, entry)
		}
		items = append(items, it)
	}
	got := SanitizeArrItems(items)
	if len(got) != MaxArrItems {
		t.Fatalf("items = %d, want %d", len(got), MaxArrItems)
	}
	it := got[0]
	if it.InstanceName != "Radarr[31m" || len([]rune(it.TitleSlug)) != MaxArrSlugRunes || len(it.Queue) != MaxArrQueueEntries || it.QueueCount != MaxArrQueueEntries {
		t.Fatalf("item = name %q slug %d runes, %d entries, count %d", it.InstanceName, len([]rune(it.TitleSlug)), len(it.Queue), it.QueueCount)
	}
	e := it.Queue[0]
	if len([]rune(e.Title)) != MaxArrQueueTitleRunes || len([]rune(e.Status)) != 64 || len([]rune(e.ErrorMessage)) != MaxArrQueueTextRunes ||
		e.Label != "Downloaded - Waiting to Import" {
		t.Fatalf("entry = title %d, status %d, error %d runes, label %q", len([]rune(e.Title)), len([]rune(e.Status)), len([]rune(e.ErrorMessage)), e.Label)
	}
	if want := []string{"a", "b", "c", "d", "e"}; strings.Join(e.Messages, ",") != strings.Join(want, ",") {
		t.Fatalf("messages = %q, want %q (distinct, non-empty, at most %d)", e.Messages, want, MaxArrQueueMessages)
	}
	// Input is not modified.
	if len(items[0].Queue) != 15 || items[0].Queue[0].Title != long {
		t.Fatal("SanitizeArrItems changed its input")
	}
}
