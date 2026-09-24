package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/backup"
	"github.com/sl0wz3r/dupearr/internal/logging"
	"github.com/sl0wz3r/dupearr/internal/models"
	"github.com/sl0wz3r/dupearr/internal/version"
)

const (
	// maxBackupUpload caps a restore upload (Servarr: 500 000 000 bytes).
	maxBackupUpload = 512 << 20
	// backupOpTimeout bounds backup creation/restore staging.
	backupOpTimeout = 10 * time.Minute
	// uploadIdleTimeout is how long a backup upload may stall before it is aborted.
	uploadIdleTimeout = time.Minute
)

// dockerMarkers are files container runtimes create: Docker's /.dockerenv, Podman's
// /run/.containerenv.
var dockerMarkers = []string{"/.dockerenv", "/run/.containerenv"}

// RunningInDocker reports whether Dupearr runs in a container: DUPEARR_DOCKER=1 (set by the
// official image, so Podman and Kubernetes — where /.dockerenv does not exist — count too), or a
// container runtime's marker file.
func RunningInDocker() bool {
	if v := strings.TrimSpace(os.Getenv("DUPEARR_DOCKER")); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	for _, f := range dockerMarkers {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return false
}

func runningInDocker() bool { return RunningInDocker() }

// ---------------------------------------------------------------------------
// GET /api/v1/system/status
// ---------------------------------------------------------------------------

type systemStatus struct {
	AppName        string    `json:"appName"`
	InstanceName   string    `json:"instanceName"`
	Version        string    `json:"version"`
	Commit         string    `json:"commit"`
	BuildDate      string    `json:"buildDate"`
	StartTime      time.Time `json:"startTime"`
	UptimeSeconds  int64     `json:"uptimeSeconds"`
	OsName         string    `json:"osName"`
	OsArch         string    `json:"osArch"`
	GoVersion      string    `json:"goVersion"`
	IsDocker       bool      `json:"isDocker"`
	DataDirectory  string    `json:"dataDirectory"`
	ConfigFile     string    `json:"configFile"`
	DatabaseFile   string    `json:"databaseFile"`
	DatabaseSize   int64     `json:"databaseSize"`
	URLBase        string    `json:"urlBase"`
	Authentication string    `json:"authentication"`
	DryRun         bool      `json:"dryRun"`
	Mode           string    `json:"mode"`
}

func (s *Server) handleSystemStatus(w http.ResponseWriter, r *http.Request) {
	c := s.d.Config.Get()
	st := systemStatus{
		AppName:        "Dupearr",
		InstanceName:   c.InstanceName,
		Version:        version.Version,
		Commit:         version.Commit,
		BuildDate:      version.BuildDate,
		StartTime:      s.d.StartTime.UTC(),
		UptimeSeconds:  int64(s.now().Sub(s.d.StartTime) / time.Second),
		OsName:         runtime.GOOS,
		OsArch:         runtime.GOARCH,
		GoVersion:      runtime.Version(),
		IsDocker:       s.isDocker(),
		DataDirectory:  s.d.Config.DataDir(),
		ConfigFile:     s.d.Config.Path(),
		URLBase:        c.UrlBase,
		Authentication: s.authMethod(),
	}
	if s.d.Store != nil {
		st.DatabaseFile = s.d.Store.Path()
		if fi, err := os.Stat(st.DatabaseFile); err == nil {
			st.DatabaseSize = fi.Size()
		}
		settings, err := s.d.Store.Settings().Get(r.Context())
		if err != nil {
			s.writeErr(w, r, fmt.Errorf("read settings: %w", err))
			return
		}
		st.DryRun, st.Mode = settings.DryRun, settings.Mode
	}
	s.writeJSON(w, http.StatusOK, st)
}

// ---------------------------------------------------------------------------
// Health and tasks
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	var list []models.HealthCheck
	if s.d.Health != nil {
		list = s.d.Health.Results()
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	cmd, err := s.enqueue(r.Context(), models.CmdCheckHealth, struct{}{}, models.TriggerManual)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeCommandCreated(w, cmd)
}

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if s.d.Commands == nil {
		s.writeJSON(w, http.StatusOK, []models.ScheduledTask{})
		return
	}
	tasks, err := s.d.Commands.Tasks(r.Context())
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, tasks)
}

// ---------------------------------------------------------------------------
// POST /api/v1/system/restart
// ---------------------------------------------------------------------------

func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if s.d.Restart == nil {
		s.writeErr(w, r, errUnavailable("Restart"))
		return
	}
	s.log.Info("Restart requested through the API")
	s.writeJSON(w, http.StatusAccepted, empty)
	s.afterResponse(w, s.d.Restart)
}

// afterResponse flushes the response and then runs fn (asynchronously, so a restart that waits
// for in-flight requests does not wait for this one).
func (s *Server) afterResponse(w http.ResponseWriter, fn func()) {
	_ = http.NewResponseController(w).Flush()
	go fn()
}

// ---------------------------------------------------------------------------
// Logs
// ---------------------------------------------------------------------------

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	p, err := parsePaging(r, "time", "descending")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	level := strings.TrimSpace(r.URL.Query().Get("level"))
	if level != "" {
		if _, err := logging.ParseLevel(level); err != nil {
			s.writeErr(w, r, errValidation(invalid("level", "Must be one of: trace, debug, info, warn, error")))
			return
		}
	}
	if s.d.Logs == nil {
		s.writeJSON(w, http.StatusOK, pageOf[logging.Entry](p))
		return
	}
	s.writeJSON(w, http.StatusOK, s.d.Logs.Recent(p, level))
}

func (s *Server) handleLogFiles(w http.ResponseWriter, r *http.Request) {
	if s.d.Logs == nil {
		s.writeJSON(w, http.StatusOK, []logging.LogFile{})
		return
	}
	files, err := s.d.Logs.Files()
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, files)
}

// handleLogFile serves one log file as text/plain. logging.Manager.FilePath validates the name
// (no traversal; must be a listed dupearr*.txt file).
func (s *Server) handleLogFile(w http.ResponseWriter, r *http.Request) {
	if s.d.Logs == nil {
		s.writeErr(w, r, errNotFound("Log file"))
		return
	}
	p, err := s.d.Logs.FilePath(r.PathValue("filename"))
	if err != nil {
		if errors.Is(err, logging.ErrFileNotFound) {
			err = errNotFound("Log file")
		}
		s.writeErr(w, r, err)
		return
	}
	// docs/DECISIONS.md D8: serve only files that stay inside the log folder once symlinks are
	// resolved (a symlinked "dupearr.txt" must not expose another file).
	if !withinDir(p, filepath.Dir(p)) {
		s.log.Warn("Refusing to serve a log file that resolves outside the log folder", "name", filepath.Base(p))
		s.writeErr(w, r, errNotFound("Log file"))
		return
	}
	serveFile(w, r, p, "text/plain; charset=utf-8", "")
}

// serveFile serves a regular file (Range/HEAD aware). A missing file is a 404. disposition, when
// set, is sent as Content-Disposition.
func serveFile(w http.ResponseWriter, r *http.Request, path, contentType, disposition string) {
	f, err := os.Open(path)
	if err != nil {
		writeMessage(w, http.StatusNotFound, "File not found")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeMessage(w, http.StatusNotFound, "File not found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	if disposition != "" {
		w.Header().Set("Content-Disposition", disposition)
	}
	http.ServeContent(w, r, filepath.Base(path), fi.ModTime(), f)
}

// ---------------------------------------------------------------------------
// Backups
// ---------------------------------------------------------------------------

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	list, err := s.backups.List()
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleBackupCreate(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backupOpTimeout)
	defer cancel()
	b, err := s.backups.Create(ctx, backup.TypeManual)
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, b)
}

func (s *Server) handleBackupDelete(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if err := s.backups.Delete(id); err != nil {
		if fsNotFound(err) {
			err = errNotFound("Backup")
		}
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, empty)
}

type restoreResponse struct {
	RestartRequired bool `json:"restartRequired"`
}

// restoreStagedResponse answers a restore staged for review: nothing is applied until
// POST /system/backup/restore/confirm (DELETE /system/backup/restore discards it).
type restoreStagedResponse struct {
	Staged          bool                   `json:"staged"`
	RestartRequired bool                   `json:"restartRequired"`
	Summary         *backup.RestoreSummary `json:"summary"`
}

// handleBackupRestore stages a stored backup for review. The running instance's security
// settings (authentication, API key, user, webhook token, listener) are always kept.
func (s *Server) handleBackupRestore(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), backupOpTimeout)
	defer cancel()
	sum, err := s.backups.StageRestore(ctx, id, backup.RestoreOptions{})
	if err != nil {
		if fsNotFound(err) {
			err = errNotFound("Backup")
		}
		s.writeErr(w, r, restoreError(err))
		return
	}
	s.restoreStaged(w, sum)
}

// restoreStaged answers a successful restore staging with the summary to review.
func (s *Server) restoreStaged(w http.ResponseWriter, sum *backup.RestoreSummary) {
	s.log.Warn("Backup staged for restore; waiting for the confirmation (it is discarded at the next start otherwise)")
	s.writeJSON(w, http.StatusOK, restoreStagedResponse{Staged: true, Summary: sum})
}

// handleBackupRestoreConfirm confirms the staged restore and restarts to apply it.
func (s *Server) handleBackupRestoreConfirm(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	if err := s.backups.ConfirmRestore(r.Context()); err != nil {
		switch {
		case errors.Is(err, backup.ErrStaleRestore):
			err = errConflict("%s", backup.ErrStaleRestore.Error())
		case errors.Is(err, backup.ErrNoStagedRestore):
			err = errStatus(http.StatusNotFound, "No restore is waiting for confirmation; stage the backup again")
		}
		s.writeErr(w, r, err)
		return
	}
	s.log.Warn("Backup restore confirmed; restarting to apply it")
	s.audit(r, audit.KindRestoreConfirmed, "Backup restore confirmed")
	s.writeJSON(w, http.StatusOK, restoreResponse{RestartRequired: true})
	if s.d.Restart != nil {
		s.afterResponse(w, s.d.Restart)
	}
}

// handleBackupRestoreDiscard discards a staged restore (a no-op when nothing is staged).
func (s *Server) handleBackupRestoreDiscard(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	if err := s.backups.DiscardRestore(); err != nil {
		s.writeErr(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, empty)
}

// restoreError reports validation problems of a restore (bad archive) as 400 without exposing
// paths; other errors pass through.
func restoreError(err error) error {
	var ae *apiError
	if err == nil || errors.As(err, &ae) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, backup.ErrInvalidBackup) {
		return errBadRequest("%s", backupProblem(err))
	}
	return err
}

// backupProblem is a client-safe description of an invalid backup archive.
func backupProblem(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, backup.ErrInvalidBackup.Error()); i >= 0 {
		return strings.TrimSpace(msg[i:])
	}
	return backup.ErrInvalidBackup.Error()
}

// handleBackupRestoreUpload accepts a multipart "file" (a Dupearr backup zip, ≤ 512 MiB) and
// streams it to the backup service, which spools, validates and stages it.
func (s *Server) handleBackupRestoreUpload(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	// A large upload may take longer than the fixed body deadline: extend it as long as data flows.
	extendBodyReadDeadline(w, r, uploadIdleTimeout)
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUpload+1<<20) // + multipart framing
	mr, err := r.MultipartReader()
	if err != nil {
		s.writeErr(w, r, errBadRequest("Expected a multipart/form-data upload with a \"file\" field"))
		return
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			s.writeErr(w, r, errBadRequest("File must be provided (multipart field \"file\")"))
			return
		}
		if err != nil {
			s.writeErr(w, r, uploadError(err))
			return
		}
		if part.FormName() != "file" {
			_ = part.Close()
			continue
		}
		sum, err := s.restoreUploadPart(r.Context(), part)
		_ = part.Close()
		if err != nil {
			s.writeErr(w, r, err)
			return
		}
		s.restoreStaged(w, sum)
		return
	}
}

var zipMagic = []byte("PK\x03\x04")

// restoreUploadPart checks the part looks like a zip and hands it to the backup service.
func (s *Server) restoreUploadPart(ctx context.Context, part *multipart.Part) (*backup.RestoreSummary, error) {
	if name := part.FileName(); name != "" && !strings.EqualFold(filepath.Ext(name), ".zip") {
		return nil, errStatus(http.StatusUnsupportedMediaType, "Only Dupearr backup .zip files can be restored")
	}
	head := make([]byte, len(zipMagic))
	n, err := io.ReadFull(part, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, uploadError(err)
	}
	if !bytes.Equal(head[:n], zipMagic) {
		return nil, errBadRequest("The uploaded file is not a zip archive")
	}
	ctx, cancel := context.WithTimeout(ctx, backupOpTimeout)
	defer cancel()
	sum, err := s.backups.StageRestoreUpload(ctx, io.MultiReader(bytes.NewReader(head), part), -1, backup.RestoreOptions{})
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, uploadError(err)
		}
		return nil, restoreError(err)
	}
	return sum, nil
}

func uploadError(err error) error {
	var mbe *http.MaxBytesError
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		return err
	case errors.As(err, &mbe):
		return errStatus(http.StatusRequestEntityTooLarge, "Backup uploads are limited to %d MiB", maxBackupUpload>>20)
	}
	return errBadRequest("Invalid upload")
}

// handleBackupDownload serves /backup/{type}/{name}: the backup service validates type and name;
// the resolved file must additionally be a regular .zip file inside the data directory.
//
// A backup holds every secret: the master API key, the webhook token, the password hash and the
// Plex and *arr credentials. Like revealing the API key, downloading one therefore needs the
// current password whenever a Forms account exists (GAP-09): this GET only serves callers that
// sent the API key or that no password protects; a browser session downloads through POST
// /api/v1/system/backup/download/{id} with {"currentPassword"} (handleBackupDownloadPassword).
func (s *Server) handleBackupDownload(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	switch ok, err := s.mayDownloadBackupDirectly(r); {
	case err != nil:
		s.writeErr(w, r, err)
		return
	case !ok:
		s.writeErr(w, r, errStatus(http.StatusForbidden, "A backup holds every credential: download it from System → Backup, "+
			"which asks for your password, or send the API key"))
		return
	}
	s.serveBackup(w, r, r.PathValue("type"), r.PathValue("name"))
}

// mayDownloadBackupDirectly reports whether the caller may download a backup without entering
// the password: it sent the API key, or no password protects the instance (and first-run setup
// is not pending).
func (s *Server) mayDownloadBackupDirectly(r *http.Request) (bool, error) {
	if auth.FromContext(r.Context()).Via == auth.ViaAPIKey {
		return true, nil
	}
	if s.setupPendingForCaller(r) {
		return false, nil
	}
	u, err := s.currentUser(r.Context())
	if err != nil {
		return false, err
	}
	return !s.passwordProtected(u), nil
}

// handleBackupDownloadPassword serves backup {id} after checking the current password (when a
// Forms account exists; throttled like the login): POST /api/v1/system/backup/download/{id} with
// {"currentPassword"}.
func (s *Server) handleBackupDownloadPassword(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		s.writeErr(w, r, errUnavailable("Backup"))
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	if _, ok := s.checkCurrentPasswordBody(w, r); !ok {
		return
	}
	list, err := s.backups.List()
	if err != nil {
		s.writeErr(w, r, err)
		return
	}
	for _, b := range list {
		if b.ID == id {
			s.serveBackup(w, r, b.Type, b.Name)
			return
		}
	}
	s.writeErr(w, r, errNotFound("Backup"))
}

// serveBackup serves the backup kind/name as an attachment (never cached) and records the
// download in the history.
func (s *Server) serveBackup(w http.ResponseWriter, r *http.Request, kind, name string) {
	p, err := s.backups.File(kind, name)
	if err != nil {
		if fsNotFound(err) || errors.Is(err, backup.ErrInvalidBackup) {
			err = errNotFound("Backup")
		}
		s.writeErr(w, r, err)
		return
	}
	if !strings.EqualFold(filepath.Ext(p), ".zip") || !withinDir(p, s.d.Config.DataDir()) {
		s.log.Warn("Refusing to serve a backup outside the data directory", "type", kind, "name", name)
		s.writeErr(w, r, errNotFound("Backup"))
		return
	}
	disp := mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(p)})
	w.Header().Set("Cache-Control", "no-store")
	s.log.Info("Backup downloaded", "name", filepath.Base(p), "via", auth.FromContext(r.Context()).Via)
	s.audit(r, audit.KindBackupDownloaded, "Backup downloaded", "name", filepath.Base(p))
	serveFile(w, r, p, "application/zip", disp)
}

// withinDir reports whether p (after resolving symlinks) is inside dir (also resolved).
func withinDir(p, dir string) bool {
	rp, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	rd, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rd, rp)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
