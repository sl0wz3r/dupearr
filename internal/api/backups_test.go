package api

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sl0wz3r/dupearr/internal/backup"
)

func (ts *testServer) waitRestarts(n int32) {
	ts.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for ts.restarts.Load() < n && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := ts.restarts.Load(); got != n {
		ts.t.Fatalf("restarts = %d, want %d", got, n)
	}
}

func upload(t *testing.T, ts *testServer, field, filename string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("note", "ignored")
	if field != "" {
		fw, err := mw.CreateFormFile(field, filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/backup/restore/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Api-Key", ts.key)
	req.RemoteAddr = testRemote
	rr := httptest.NewRecorder()
	ts.h.ServeHTTP(rr, req)
	return rr
}

func TestBackups(t *testing.T) {
	ts := newTestServer(t)
	rr := ts.do(http.MethodGet, "/api/v1/system/backup", nil)
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("empty list = %d %s", rr.Code, rr.Body.String())
	}
	var b backup.Backup
	expect(t, ts.do(http.MethodPost, "/api/v1/system/backup", nil), http.StatusCreated, &b)
	if b.Type != backup.TypeManual || b.ID == 0 {
		t.Fatalf("created = %+v", b)
	}
	var list []backup.Backup
	expect(t, ts.do(http.MethodGet, "/api/v1/system/backup", nil), http.StatusOK, &list)
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// r2-data-files#6: a restore is staged for review; it only restarts once confirmed.
	var staged restoreStagedResponse
	expect(t, ts.do(http.MethodPost, fmt.Sprintf("/api/v1/system/backup/restore/%d", b.ID), nil), http.StatusOK, &staged)
	if !staged.Staged || staged.RestartRequired || staged.Summary == nil || len(staged.Summary.Changes) != 1 || len(ts.backups.restored) != 1 {
		t.Fatalf("restore = %+v", staged)
	}
	time.Sleep(50 * time.Millisecond)
	if ts.restarts.Load() != 0 {
		t.Fatal("staging a restore restarted Dupearr before the confirmation")
	}
	var restore restoreResponse
	expect(t, ts.do(http.MethodPost, "/api/v1/system/backup/restore/confirm", nil), http.StatusOK, &restore)
	if !restore.RestartRequired || ts.backups.confirmed != 1 {
		t.Fatalf("confirm = %+v", restore)
	}
	ts.waitRestarts(1)
	expect(t, ts.do(http.MethodPost, "/api/v1/system/backup/restore/confirm", nil), http.StatusNotFound, nil)
	expect(t, ts.do(http.MethodPost, "/api/v1/system/backup/restore/999", nil), http.StatusNotFound, nil)

	// Discard, and a stale staging (security settings changed meanwhile) is a conflict.
	expect(t, ts.do(http.MethodPost, fmt.Sprintf("/api/v1/system/backup/restore/%d", b.ID), nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodDelete, "/api/v1/system/backup/restore", nil), http.StatusOK, nil)
	if ts.backups.discarded != 1 || ts.backups.staged {
		t.Fatal("discard did not reach the backup service")
	}
	expect(t, ts.do(http.MethodPost, fmt.Sprintf("/api/v1/system/backup/restore/%d", b.ID), nil), http.StatusOK, nil)
	ts.backups.confirmErr = backup.ErrStaleRestore
	expect(t, ts.do(http.MethodPost, "/api/v1/system/backup/restore/confirm", nil), http.StatusConflict, nil)
	ts.backups.confirmErr = nil
	if ts.restarts.Load() != 1 {
		t.Fatal("a refused confirmation restarted Dupearr")
	}

	expect(t, ts.do(http.MethodDelete, fmt.Sprintf("/api/v1/system/backup/%d", b.ID), nil), http.StatusOK, nil)
	expect(t, ts.do(http.MethodDelete, fmt.Sprintf("/api/v1/system/backup/%d", b.ID), nil), http.StatusNotFound, nil)
}

func TestBackupUpload(t *testing.T) {
	ts := newTestServer(t)
	zipData := append([]byte("PK\x03\x04"), bytes.Repeat([]byte("z"), 100)...)
	var staged restoreStagedResponse
	expect(t, upload(t, ts, "file", "dupearr_backup.zip", zipData), http.StatusOK, &staged)
	if !staged.Staged || staged.RestartRequired || len(ts.backups.uploaded) != 1 || !bytes.Equal(ts.backups.uploaded[0], zipData) {
		t.Fatalf("upload not passed through intact")
	}
	expect(t, ts.do(http.MethodPost, "/api/v1/system/backup/restore/confirm", nil), http.StatusOK, nil)
	ts.waitRestarts(1)

	expect(t, upload(t, ts, "file", "notes.txt", zipData), http.StatusUnsupportedMediaType, nil)
	expect(t, upload(t, ts, "file", "fake.zip", []byte("not a zip at all")), http.StatusBadRequest, nil)
	expect(t, upload(t, ts, "", "", nil), http.StatusBadRequest, nil)
	expect(t, ts.do(http.MethodPost, "/api/v1/system/backup/restore/upload", `{"file":"x"}`), http.StatusBadRequest, nil)

	ts.backups.uploadErr = fmt.Errorf("restore upload: %w: archive lacks dupearr.db", backup.ErrInvalidBackup)
	if msg := message(t, upload(t, ts, "file", "b.zip", zipData), http.StatusBadRequest); msg != "invalid backup: archive lacks dupearr.db" {
		t.Fatalf("message = %q", msg)
	}
	if ts.restarts.Load() != 1 {
		t.Fatal("a failed restore restarted Dupearr")
	}
}

func TestBackupDownload(t *testing.T) {
	ts := newTestServer(t)
	dir := filepath.Join(ts.dir, "Backups", "manual")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "dupearr_backup_1.zip")
	if err := os.WriteFile(p, []byte("PK\x03\x04zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts.backups.file = p

	expect(t, ts.do(http.MethodGet, "/backup/manual/dupearr_backup_1.zip", nil, noKey), http.StatusUnauthorized, nil)
	expect(t, ts.do(http.MethodGet, "/backup/manual/dupearr_backup_1.zip?apikey="+ts.key, nil, noKey), http.StatusUnauthorized, nil)
	rr := ts.do(http.MethodGet, "/backup/manual/dupearr_backup_1.zip", nil)
	if rr.Code != http.StatusOK || rr.Header().Get("Content-Type") != "application/zip" ||
		!strings.Contains(rr.Header().Get("Content-Disposition"), `attachment; filename=dupearr_backup_1.zip`) || rr.Body.String() != "PK\x03\x04zip" {
		t.Fatalf("download: %d %v", rr.Code, rr.Header())
	}
	ts.createUser("admin", "pw")
	// A session alone never downloads a backup (it holds every credential): POST
	// /api/v1/system/backup/download/{id} with the password does (GAP-09, gap_test.go).
	if rr := ts.do(http.MethodGet, "/backup/manual/dupearr_backup_1.zip", nil, noKey, withCookie(ts.login("admin", "pw"))); rr.Code != http.StatusForbidden {
		t.Fatalf("cookie download = %d, want 403", rr.Code)
	}
	expect(t, ts.do(http.MethodGet, "/backup/scheduled/x.zip", nil), http.StatusNotFound, nil)

	// Defense in depth: a path outside the data directory is never served.
	outside := filepath.Join(t.TempDir(), "evil.zip")
	_ = os.WriteFile(outside, []byte("PK"), 0o644)
	ts.backups.file = outside
	expect(t, ts.do(http.MethodGet, "/backup/manual/evil.zip", nil), http.StatusNotFound, nil)
}

func TestWithinDir(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a", "b.zip")
	_ = os.MkdirAll(filepath.Dir(inside), 0o755)
	_ = os.WriteFile(inside, nil, 0o644)
	if !withinDir(inside, root) {
		t.Error("file inside not accepted")
	}
	if withinDir(root, root) {
		t.Error("the directory itself accepted")
	}
	other := t.TempDir()
	link := filepath.Join(root, "link.zip")
	target := filepath.Join(other, "t.zip")
	_ = os.WriteFile(target, nil, 0o644)
	if err := os.Symlink(target, link); err == nil && withinDir(link, root) {
		t.Error("symlink escaping the directory accepted")
	}
	if withinDir(filepath.Join(root, "missing.zip"), root) {
		t.Error("missing file accepted")
	}
}
