package auth

// GAP-12 (docs/SECURITY.md): sign-ins and failed sign-ins are recorded in the durable history,
// failures at most once a minute (with their count), so a client cannot fill the database.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// securityEvents returns the data of the security events of kind, oldest first.
func (e *testEnv) securityEvents(kind string) []map[string]any {
	e.t.Helper()
	page, err := e.db.History().List(context.Background(), []string{models.EventSecurity}, 0,
		store.Paging{Page: 1, PageSize: 500, SortKey: "createdAt", SortDirection: "ascending"})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []map[string]any
	for _, ev := range page.Records {
		var d map[string]any
		if json.Unmarshal(ev.Data, &d) == nil && d["kind"] == kind {
			out = append(out, d)
		}
	}
	return out
}

func TestFailedSignInsAreRecordedAtMostOncePerMinute(t *testing.T) {
	e := newTestEnv(t, nil)
	e.createUser()
	// Checked failures (throttled attempts are refused before the password is checked).
	checked := 0
	fail := func() {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = remoteAddr
		switch err := e.svc.Login(httptest.NewRecorder(), req, testUser, "wrong password!", false); {
		case err == nil:
			t.Fatal("a wrong password signed in")
		case errors.Is(err, ErrInvalidCredentials):
			checked++
		}
	}
	for range 20 {
		fail()
	}
	if checked < 2 {
		t.Fatalf("only %d failures were checked (test setup)", checked)
	}
	if got := e.securityEvents(audit.KindLoginFailed); len(got) != 1 {
		t.Fatalf("%d failure records for a burst, want 1", len(got))
	}
	e.advance(time.Hour) // past the throttling and the recording interval
	before := checked
	fail()
	if checked != before+1 {
		t.Fatal("the failure after the pause was not checked (test setup)")
	}
	got := e.securityEvents(audit.KindLoginFailed)
	if want := strconv.Itoa(checked - 1); len(got) != 2 || got[1]["count"] != want {
		t.Fatalf("records after a minute: %v, want the second to count %s failures", got, want)
	}
	e.advance(time.Hour) // past the backoff the last failure started
	e.login(false)
	if len(e.securityEvents(audit.KindLogin)) != 1 {
		t.Fatal("the sign-in was not recorded")
	}
}
