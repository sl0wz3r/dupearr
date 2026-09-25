package plex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/sl0wz3r/dupearr/internal/mediaserver"
)

// TestErrNotFoundMatchesNeutralSentinel: the kind-neutral callers test for
// mediaserver.ErrNotFound; a Plex "not found" must match it while keeping its text and identity.
func TestErrNotFoundMatchesNeutralSentinel(t *testing.T) {
	if got := ErrNotFound.Error(); got != "plex: not found" {
		t.Errorf("ErrNotFound text = %q, want the unchanged %q", got, "plex: not found")
	}
	wrapped := fmt.Errorf("item 5: %w", ErrNotFound)
	for name, err := range map[string]error{"bare": ErrNotFound, "wrapped": wrapped} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: errors.Is(err, plex.ErrNotFound) = false", name)
		}
		if !errors.Is(err, mediaserver.ErrNotFound) {
			t.Errorf("%s: errors.Is(err, mediaserver.ErrNotFound) = false", name)
		}
	}
	// A generic "not found" of another server is not a Plex one.
	if errors.Is(mediaserver.ErrNotFound, ErrNotFound) {
		t.Error("mediaserver.ErrNotFound matches plex.ErrNotFound")
	}
	// No other Plex sentinel is a "not found".
	for _, e := range []error{ErrUnauthorized, ErrForbidden, ErrDeletionNotAllowed, ErrInvalidArgument, ErrRedirect} {
		if errors.Is(e, mediaserver.ErrNotFound) {
			t.Errorf("%v matches mediaserver.ErrNotFound", e)
		}
	}

	// A 404 of the server matches both sentinels.
	f := newFakeServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := newTestClient(f, "").Item(context.Background(), "123")
	if !errors.Is(err, ErrNotFound) || !errors.Is(err, mediaserver.ErrNotFound) {
		t.Errorf("404: err = %v; want it to match plex.ErrNotFound and mediaserver.ErrNotFound", err)
	}
}

// TestClientImplementsTheNeutralContract checks at run time what mediaserver.go asserts at compile
// time, through the interface values the executor and the health checks type-assert.
func TestClientImplementsTheNeutralContract(t *testing.T) {
	var c mediaserver.Client = New("http://127.0.0.1:1", "t", Options{})
	if _, ok := c.(mediaserver.VersionDeleter); !ok {
		t.Error("*plex.Client lost mediaserver.VersionDeleter: the plex method would be unavailable")
	}
	if _, ok := c.(mediaserver.ItemRefresher); !ok {
		t.Error("*plex.Client lost mediaserver.ItemRefresher")
	}
	if _, ok := c.(mediaserver.FolderScanner); !ok {
		t.Error("*plex.Client lost mediaserver.FolderScanner")
	}
	if _, ok := c.(mediaserver.ChangeNotifier); ok {
		t.Error("*plex.Client implements mediaserver.ChangeNotifier; Plex is notified by folder scans")
	}
}
