package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/notifications"
)

func TestNotificationSchemaAndTriggers(t *testing.T) {
	ts := newTestServer(t)
	var schema []notifications.ProviderSchema
	expect(t, ts.do(http.MethodGet, "/api/v1/notification/schema", nil), http.StatusOK, &schema)
	kinds := map[string]bool{}
	for _, p := range schema {
		kinds[p.Kind] = true
	}
	for _, k := range []string{"discord", "gotify", "webhook", "email"} {
		if !kinds[k] {
			t.Errorf("schema lacks %s", k)
		}
	}
	var triggers []notifications.TriggerOption
	expect(t, ts.do(http.MethodGet, "/api/v1/notification/triggers", nil), http.StatusOK, &triggers)
	if len(triggers) != 6 || triggers[0].Value != models.OnDuplicatesFound || triggers[0].Label == "" {
		t.Fatalf("triggers = %+v", triggers)
	}
}

func TestNotificationCRUD(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	body := map[string]any{
		"name": "Gotify", "kind": "gotify", "enabled": true, "triggers": []string{models.OnFileDeleted},
		"settings": map[string]any{"serverUrl": "http://gotify.local", "appToken": "super-secret", "priority": 5},
	}
	var n models.NotificationConfig
	rr := ts.do(http.MethodPost, "/api/v1/notification", body)
	expect(t, rr, http.StatusCreated, &n)
	if n.ID == 0 || strings.Contains(rr.Body.String(), "super-secret") || !strings.Contains(string(n.Settings), maskedSecret) {
		t.Fatalf("created = %s", rr.Body.String())
	}
	stored, _ := ts.db.Notifications().Get(ctx, n.ID)
	if !strings.Contains(string(stored.Settings), "super-secret") {
		t.Fatalf("stored settings = %s", stored.Settings)
	}
	id := itoa64(n.ID)
	rr = ts.do(http.MethodGet, "/api/v1/notification", nil)
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "super-secret") {
		t.Fatalf("list leaked the secret: %s", rr.Body.String())
	}

	// Sending the mask back keeps the secret.
	var settings map[string]any
	_ = json.Unmarshal(n.Settings, &settings)
	settings["priority"] = 8
	n.Name = "Gotify Home"
	n.Settings, _ = json.Marshal(settings)
	expect(t, ts.do(http.MethodPut, "/api/v1/notification/"+id, n), http.StatusAccepted, &n)
	stored, _ = ts.db.Notifications().Get(ctx, n.ID)
	if stored.Name != "Gotify Home" || !strings.Contains(string(stored.Settings), "super-secret") || !strings.Contains(string(stored.Settings), `"priority":8`) {
		t.Fatalf("stored after update = %+v %s", stored, stored.Settings)
	}
	// ... but not to another server.
	settings["serverUrl"] = "http://attacker.example"
	n.Settings, _ = json.Marshal(settings)
	if props := validationProps(t, ts.do(http.MethodPut, "/api/v1/notification/"+id, n)); len(props) == 0 {
		t.Fatal("masked secret accepted for a new destination")
	}

	for _, bad := range []map[string]any{
		{"name": "", "kind": "gotify", "settings": map[string]any{"serverUrl": "http://g", "appToken": "t"}},
		{"name": "x", "kind": "carrier-pigeon", "settings": map[string]any{}},
		{"name": "x", "kind": "gotify", "settings": map[string]any{"appToken": "t"}},
		{"name": "x", "kind": "gotify", "triggers": []string{"onEverything"}, "settings": map[string]any{"serverUrl": "http://g", "appToken": "t"}},
	} {
		if rr := ts.do(http.MethodPost, "/api/v1/notification", bad); rr.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", bad, rr.Code)
		}
	}
	// Test with an invalid configuration: validation failures, nothing sent.
	if props := validationProps(t, ts.do(http.MethodPost, "/api/v1/notification/test", map[string]any{"name": "x", "kind": "gotify", "settings": map[string]any{}})); len(props) == 0 {
		t.Fatal("no validation errors")
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/notification/test", map[string]any{"id": 999, "name": "x", "kind": "gotify"}), http.StatusNotFound, nil)

	expect(t, ts.do(http.MethodGet, "/api/v1/notification/"+id, nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodDelete, "/api/v1/notification/"+id, nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodGet, "/api/v1/notification/"+id, nil), http.StatusNotFound, nil)
}
