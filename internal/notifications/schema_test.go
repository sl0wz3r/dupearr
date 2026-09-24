package notifications

import (
	"encoding/json"
	"regexp"
	"slices"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/models"
)

func TestSchemaProviders(t *testing.T) {
	want := []string{KindDiscord, KindSlack, KindTelegram, KindPushover, KindGotify, KindNtfy, KindApprise, KindWebhook, KindEmail}
	if got := knownKinds(); !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	camel := regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)
	validTypes := []string{fieldText, fieldPassword, fieldURL, fieldNumber, fieldCheckbox, fieldSelect, fieldTextarea}
	for _, p := range Schema() {
		t.Run(p.Kind, func(t *testing.T) {
			if p.Name == "" || len(p.Fields) == 0 {
				t.Fatalf("provider %q missing name or fields", p.Kind)
			}
			seen := map[string]bool{}
			for _, f := range p.Fields {
				if !camel.MatchString(f.Name) {
					t.Errorf("field %q is not camelCase", f.Name)
				}
				if seen[f.Name] {
					t.Errorf("duplicate field %q", f.Name)
				}
				seen[f.Name] = true
				if f.Label == "" {
					t.Errorf("field %q has no label", f.Name)
				}
				if !slices.Contains(validTypes, f.Type) {
					t.Errorf("field %q has invalid type %q", f.Name, f.Type)
				}
				// Secret URLs (webhook URLs) and names (ntfy topic, Apprise config key) are bearer
				// credentials too; the UI renders every secret field as a hidden input.
				if f.Secret && !slices.Contains([]string{fieldPassword, fieldTextarea, fieldURL, fieldText}, f.Type) {
					t.Errorf("secret field %q has type %q", f.Name, f.Type)
				}
				if f.Type == fieldPassword && !f.Secret {
					t.Errorf("password field %q must be secret", f.Name)
				}
				if (f.Type == fieldSelect) != (len(f.Options) > 0) {
					t.Errorf("field %q: options only (and always) for selects", f.Name)
				}
				switch d := f.Default.(type) {
				case nil:
				case string:
					if f.Type == fieldSelect && !slices.Contains(f.Options, d) {
						t.Errorf("select %q default %q not in options %v", f.Name, d, f.Options)
					}
					if f.Type == fieldNumber || f.Type == fieldCheckbox {
						t.Errorf("field %q: string default for %s", f.Name, f.Type)
					}
				case int:
					if f.Type != fieldNumber {
						t.Errorf("field %q: int default for %s", f.Name, f.Type)
					}
				case bool:
					if f.Type != fieldCheckbox {
						t.Errorf("field %q: bool default for %s", f.Name, f.Type)
					}
				default:
					t.Errorf("field %q: unexpected default type %T", f.Name, d)
				}
			}
		})
	}
}

func TestSchemaJSON(t *testing.T) {
	b, err := json.Marshal(Schema())
	if err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 9 {
		t.Fatalf("got %d providers", len(got))
	}
	first := got[0]
	for _, k := range []string{"kind", "name", "infoUrl", "fields"} {
		if _, ok := first[k]; !ok {
			t.Errorf("provider JSON missing %q: %v", k, first)
		}
	}
	field := first["fields"].([]any)[0].(map[string]any)
	for _, k := range []string{"name", "label", "type", "required", "advanced", "secret"} {
		if _, ok := field[k]; !ok {
			t.Errorf("field JSON missing %q: %v", k, field)
		}
	}
	// Defaults of 0/false must still be emitted (omitempty only drops nil interfaces).
	for _, p := range got {
		for _, f := range p["fields"].([]any) {
			fm := f.(map[string]any)
			if fm["name"] == "sendSilently" {
				if v, ok := fm["default"]; !ok || v != false {
					t.Errorf("sendSilently default = %v (present %v), want false", v, ok)
				}
			}
		}
	}
}

func TestSchemaFreshCopy(t *testing.T) {
	a := Schema()
	a[0].Fields[0].Name = "mutated"
	if Schema()[0].Fields[0].Name == "mutated" {
		t.Fatal("Schema() returned shared state")
	}
}

func TestTriggerOptions(t *testing.T) {
	opts := TriggerOptions()
	want := []string{models.OnDuplicatesFound, models.OnFileDeleted, models.OnDeleteFailed,
		models.OnScanCompleted, models.OnHealthIssue, models.OnHealthRestored}
	var got []string
	for _, o := range opts {
		if o.Label == "" {
			t.Errorf("trigger %q has no label", o.Value)
		}
		got = append(got, o.Value)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("triggers = %v, want %v", got, want)
	}
	b, _ := json.Marshal(opts[0])
	if string(b) != `{"value":"onDuplicatesFound","label":"On Duplicates Found"}` {
		t.Fatalf("JSON = %s", b)
	}
	if isKnownTrigger("onGrab") || !isKnownTrigger(models.OnHealthIssue) {
		t.Fatal("isKnownTrigger misbehaves")
	}
}
