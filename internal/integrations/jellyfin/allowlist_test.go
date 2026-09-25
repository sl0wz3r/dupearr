package jellyfin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

// TestAllowlistRefusesBeforeSending: every request outside the allowlist (research §4's never-call
// list, Emby's POST …/Delete aliases and DeleteInfo, method and case variants, escaped slashes and
// dots, doubled slashes, the /emby prefix, a credential in the query, ids that are not 32 hex)
// fails with ErrRefused and never reaches the server.
func TestAllowlistRefusesBeforeSending(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.client()
	id := "10000000000000000000000000000a01"
	cases := []struct {
		name   string
		method string
		path   string
		query  url.Values
		body   []byte
	}{
		{"item delete", http.MethodDelete, "/Items/" + id, nil, nil},
		{"bulk delete", http.MethodDelete, "/Items", url.Values{"ids": {id}}, nil},
		{"merge versions", http.MethodPost, "/Videos/MergeVersions", url.Values{"ids": {id + "," + id}}, nil},
		{"unlink versions", http.MethodDelete, "/Videos/" + id + "/AlternateSources", nil, nil},
		{"subtitle delete", http.MethodDelete, "/Videos/" + id + "/Subtitles/0", nil, nil},
		{"image delete", http.MethodDelete, "/Items/" + id + "/Images/Primary", nil, nil},
		{"image delete index", http.MethodDelete, "/Items/" + id + "/Images/Backdrop/0", nil, nil},
		{"library delete", http.MethodDelete, "/Library/VirtualFolders", url.Values{"name": {"Movies"}}, nil},
		{"library path delete", http.MethodDelete, "/Library/VirtualFolders/Paths", nil, nil},
		{"recording delete", http.MethodDelete, "/LiveTv/Recordings/" + id, nil, nil},
		{"collection items", http.MethodDelete, "/Collections/" + id + "/Items", nil, nil},
		{"playlist items", http.MethodDelete, "/Playlists/" + id + "/Items", nil, nil},
		{"lyrics", http.MethodDelete, "/Audio/" + id + "/Lyrics", nil, nil},
		{"library refresh", http.MethodPost, "/Library/Refresh", nil, nil},
		{"emby item delete alias", http.MethodPost, "/Items/" + id + "/Delete", nil, nil},
		{"emby bulk delete alias", http.MethodPost, "/Items/Delete", url.Values{"Ids": {id}}, nil},
		{"emby unlink alias", http.MethodPost, "/Videos/" + id + "/AlternateSources/Delete", nil, nil},
		{"emby delete info", http.MethodGet, "/Items/" + id + "/DeleteInfo", nil, nil},
		{"single item (needs a user)", http.MethodGet, "/Items/" + id, nil, nil},
		{"emby prefix", http.MethodGet, "/emby/System/Info", nil, nil},
		{"lower-case path", http.MethodGet, "/items", nil, nil},
		{"lower-case method", "get", "/Sessions", nil, nil},
		{"delete on an allowed path", http.MethodDelete, "/Sessions", nil, nil},
		{"post on a read path", http.MethodPost, "/Items", nil, nil},
		{"put", http.MethodPut, "/System/Configuration", nil, nil},
		{"escaped slash", http.MethodGet, "/Videos/" + id + "%2FAdditionalParts", nil, nil},
		{"escaped dots", http.MethodGet, "/Items/%2e%2e/Sessions", nil, nil},
		{"escaped dots upper", http.MethodGet, "/Library/%2E%2E/Items", nil, nil},
		{"dot segments", http.MethodGet, "/Library/../Items", nil, nil},
		{"dot segment", http.MethodGet, "/./Sessions", nil, nil},
		{"doubled slash", http.MethodGet, "//Sessions", nil, nil},
		{"trailing slash", http.MethodGet, "/Sessions/", nil, nil},
		{"backslash", http.MethodGet, `/Items\..\Sessions`, nil, nil},
		{"ApiKey in query", http.MethodGet, "/Sessions", url.Values{"ApiKey": {testKey}}, nil},
		{"api_key in query", http.MethodGet, "/System/Info", url.Values{"api_key": {testKey}}, nil},
		{"apikey in an items query", http.MethodGet, "/Items", url.Values{"apikey": {testKey}, "Ids": {id}}, nil},
		{"unknown items key", http.MethodGet, "/Items", url.Values{"userId": {id}}, nil},
		{"short id", http.MethodGet, "/Videos/abc/AdditionalParts", nil, nil},
		{"dashed id", http.MethodGet, "/Videos/10000000-0000-0000-0000-000000000a01/AdditionalParts", nil, nil},
		{"upper-case id", http.MethodGet, "/Videos/10000000000000000000000000000A01/AdditionalParts", nil, nil},
		{"id list with a bad id", http.MethodGet, "/Items", url.Values{"Ids": {id + ",../x"}}, nil},
		{"bad parent id", http.MethodGet, "/Items", url.Values{"ParentId": {"root"}}, nil},
		{"unknown field", http.MethodGet, "/Items", url.Values{"Fields": {"MediaSources,Chapters"}}, nil},
		{"unknown item type", http.MethodGet, "/Items", url.Values{"IncludeItemTypes": {"Audio"}}, nil},
		{"body on a GET", http.MethodGet, "/Sessions", nil, []byte(`{}`)},
		{"notify without a body", http.MethodPost, "/Library/Media/Updated", nil, nil},
		{"notify with a query", http.MethodPost, "/Library/Media/Updated", url.Values{"Path": {"/x"}}, []byte(`{}`)},
		{"repeated query key", http.MethodGet, "/Items", url.Values{"Ids": {id, id}}, nil},
		{"empty path", http.MethodGet, "", nil, nil},
		{"root", http.MethodGet, "/", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.do(context.Background(), tc.method, tc.path, tc.query, tc.body)
			if !errors.Is(err, ErrRefused) {
				t.Fatalf("%s %s: err = %v, want ErrRefused", tc.method, tc.path, err)
			}
		})
	}
	if n := f.count(); n != 0 {
		t.Fatalf("the server received %d requests; a refused request must never be sent", n)
	}
}

// TestAllowlistAcceptsTheClientsRequests: the requests the client builds pass (and a few reach
// the fixture).
func TestAllowlistAcceptsTheClientsRequests(t *testing.T) {
	id := "10000000000000000000000000000a01"
	ok := []struct {
		method, path string
		query        url.Values
		body         bool
	}{
		{http.MethodGet, "/System/Info/Public", nil, false},
		{http.MethodGet, "/System/Info", nil, false},
		{http.MethodGet, "/System/Configuration", nil, false},
		{http.MethodGet, "/Library/VirtualFolders", nil, false},
		{http.MethodGet, "/Items", url.Values{"ParentId": {id}, "Recursive": {"true"}, "IncludeItemTypes": {"Movie"},
			"Fields": {listFields}, "SortBy": {"SortName,DateCreated"}, "SortOrder": {"Ascending"}, "StartIndex": {"0"},
			"Limit": {"200"}, "EnableTotalRecordCount": {"true"}}, false},
		{http.MethodGet, "/Items", url.Values{"Ids": {id}, "Fields": {"ProviderIds"}}, false},
		{http.MethodGet, "/Videos/" + id + "/AdditionalParts", nil, false},
		{http.MethodGet, "/Sessions", nil, false},
		{http.MethodGet, "/ScheduledTasks", nil, false},
		{http.MethodPost, "/Library/Media/Updated", nil, true},
	}
	for _, tc := range ok {
		if _, err := allowedRoute(tc.method, tc.path, tc.query, tc.body); err != nil {
			t.Errorf("%s %s: %v", tc.method, tc.path, err)
		}
	}
}

// TestPublicInfoSendsNoCredential: the connection test's first read carries no Authorization
// header (the key is only sent once the URL is known to be Jellyfin ≥ 12.1).
func TestPublicInfoSendsNoCredential(t *testing.T) {
	f := newFixture(t)
	f.standard()
	c := f.client()
	if _, err := c.PublicInfo(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := f.all()
	if len(reqs) != 1 || reqs[0].Header.Get("Authorization") != "" || reqs[0].URL.RawQuery != "" {
		t.Fatalf("public info request = %+v", reqs)
	}
}
