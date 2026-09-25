package fakemedia

import (
	"net/http"
	"reflect"
	"testing"
)

// TestArrQueueStatusMessages covers the queue fields that explain an entry: status, tracked
// download status, status messages and the download client's error (defaults unchanged).
func TestArrQueueStatusMessages(t *testing.T) {
	e := Start(t, Default())
	msgs := []QueueStatusMessage{{Title: "Blade.Runner.2049.2017.2160p.WEB-DL", Messages: []string{"Not an upgrade for existing movie file."}}}
	if _, err := e.AddQueueItem(InstanceRadarr, QueueItem{TmdbID: 335984, Title: "Blade.Runner.2049.2017.2160p.WEB-DL",
		State: "importPending", TrackedStatus: "warning", StatusMessages: msgs}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddQueueItem(InstanceRadarr, QueueItem{TmdbID: 603, Status: "warning", ErrorMessage: "The download is stalled"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddQueueItem(InstanceRadarr, QueueItem{TmdbID: 238}); err != nil {
		t.Fatal(err)
	}
	type record struct {
		Status                string `json:"status"`
		TrackedDownloadStatus string `json:"trackedDownloadStatus"`
		TrackedDownloadState  string `json:"trackedDownloadState"`
		StatusMessages        []struct {
			Title    string   `json:"title"`
			Messages []string `json:"messages"`
		} `json:"statusMessages"`
		ErrorMessage *string `json:"errorMessage"`
	}
	var p tPage[record]
	arrJSON(t, e.Radarr, http.MethodGet, "/api/v3/queue?page=1&pageSize=200&includeUnknownMovieItems=false", nil, http.StatusOK, &p)
	if len(p.Records) != 3 {
		t.Fatalf("records = %+v", p.Records)
	}
	stuck, warn, plain := p.Records[0], p.Records[1], p.Records[2]
	if stuck.Status != "completed" || stuck.TrackedDownloadStatus != "warning" || stuck.TrackedDownloadState != "importPending" ||
		len(stuck.StatusMessages) != 1 || stuck.StatusMessages[0].Title != msgs[0].Title ||
		!reflect.DeepEqual(stuck.StatusMessages[0].Messages, msgs[0].Messages) || stuck.ErrorMessage != nil {
		t.Errorf("stuck import = %+v", stuck)
	}
	if warn.Status != "warning" || warn.ErrorMessage == nil || *warn.ErrorMessage != "The download is stalled" {
		t.Errorf("warning = %+v", warn)
	}
	// Defaults as before: status ok, no messages (an empty list), no error message.
	if plain.Status != "downloading" || plain.TrackedDownloadStatus != "ok" || plain.StatusMessages == nil ||
		len(plain.StatusMessages) != 0 || plain.ErrorMessage != nil {
		t.Errorf("default entry = %+v", plain)
	}
}
