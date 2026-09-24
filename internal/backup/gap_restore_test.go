package backup

// GAP-04 (docs/SECURITY.md): the restore review lists every setting that decides what is removed
// and what the operator can see (the disc-safety settings, the history retention, the log level),
// flags a notification whose destination changed under the same name, and a restore never turns
// full-disc removal on or "always keep a playable copy" off by itself.

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestRestoreSummaryCoversDiscLogAndNotificationChanges(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	webhook := func(url string) *models.NotificationConfig {
		return &models.NotificationConfig{Name: "Alerts", Kind: "webhook", Settings: json.RawMessage(`{"url":"` + url + `"}`),
			Triggers: []string{models.OnFileDeleted}, Enabled: true}
	}

	// The backup: disc removal on, playable copy off, detection off, 1-day history, log level
	// error, the "Alerts" webhook pointing at the attacker.
	st := models.DefaultSettings()
	st.AllowDiscRemoval, st.KeepPlayableCopy, st.DetectDiscs, st.HistoryRetentionDays = true, false, false, 1
	st.DeletionMethods, st.RecycleBinPath = []string{"filesystem"}, "/data/.bin"
	if err := f.db.Settings().Save(ctx, st); err != nil {
		t.Fatal(err)
	}
	n := webhook("https://attacker.example/hook")
	if err := f.db.Notifications().Create(ctx, n); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cfg.Update(func(c *config.Config) { c.LogLevel = "error" }); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)

	// The live instance: defaults, info logging, the same webhook name to the owner's endpoint.
	live := models.DefaultSettings()
	live.DeletionMethods, live.RecycleBinPath = []string{"filesystem"}, "/data/.bin"
	if err := f.db.Settings().Save(ctx, live); err != nil {
		t.Fatal(err)
	}
	n.Settings = json.RawMessage(`{"url":"https://owner.example/hook"}`)
	if err := f.db.Notifications().Update(ctx, n); err != nil {
		t.Fatal(err)
	}
	if _, err := f.cfg.Update(func(c *config.Config) { c.LogLevel = "info" }); err != nil {
		t.Fatal(err)
	}

	sum := stageArchive(t, f, cfg, db)
	for _, name := range []string{"allowDiscRemoval", "keepPlayableCopy", "detectDiscs", "historyRetentionDays", "logLevel", "notificationDestinations"} {
		if changeOf(sum, name) == nil {
			t.Errorf("the summary does not list %s: %+v", name, sum.Changes)
		}
	}
	for _, name := range []string{"allowDiscRemoval", "keepPlayableCopy"} {
		if c := changeOf(sum, name); c != nil && c.Applied {
			t.Errorf("%s is applied from the backup: %+v", name, c)
		}
	}
	if c := changeOf(sum, "notificationDestinations"); c != nil {
		all := c.Current + c.Backup + c.Message
		if !strings.Contains(all, "Alerts") || strings.Contains(all, "attacker.example") || strings.Contains(all, "owner.example") {
			t.Errorf("notification destination change = %+v (must name the connection, never its URL)", c)
		}
	}
	if c := changeOf(sum, "logLevel"); c != nil && (c.Current != "info" || c.Backup != "error") {
		t.Errorf("logLevel change = %+v", c)
	}

	// The staged database keeps full-disc removal off and the playable copy on.
	var stagedDoc string
	if err := openTestDB(t, stagedPath(f, dbFileName)).QueryRow(`SELECT value FROM settings WHERE key = 'settings'`).Scan(&stagedDoc); err != nil {
		t.Fatal(err)
	}
	var staged models.Settings
	if err := json.Unmarshal([]byte(stagedDoc), &staged); err != nil {
		t.Fatal(err)
	}
	if staged.AllowDiscRemoval || !staged.KeepPlayableCopy || !staged.DryRun {
		t.Fatalf("staged settings: allowDiscRemoval %v, keepPlayableCopy %v, dryRun %v", staged.AllowDiscRemoval, staged.KeepPlayableCopy, staged.DryRun)
	}
	if staged.HistoryRetentionDays != 1 || staged.DetectDiscs {
		t.Fatalf("the other listed settings are restored as reviewed: %+v", staged)
	}
}

// An unchanged notification is not reported.
func TestRestoreSummaryIgnoresUnchangedNotifications(t *testing.T) {
	f := newFixture(t)
	n := &models.NotificationConfig{Name: "Alerts", Kind: "webhook", Settings: json.RawMessage(`{"url":"https://owner.example/hook"}`), Enabled: true}
	if err := f.db.Notifications().Create(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	cfg, db := validParts(t, f)
	archive := makeZip(t, zipEntry{name: "config.xml", data: cfg}, zipEntry{name: "dupearr.db", data: db})
	sum, err := f.svc.StageRestoreUpload(context.Background(), bytes.NewReader(archive), int64(len(archive)), RestoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if c := changeOf(sum, "notificationDestinations"); c != nil {
		t.Fatalf("unchanged notification reported: %+v", c)
	}
}
