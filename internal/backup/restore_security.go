package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/models"
)

// RestoreOptions controls what a restore takes from the backup.
type RestoreOptions struct {
	// RestoreSecuritySettings restores the backup's security settings instead of keeping the
	// running instance's: the authentication method and requirement, the API key, the webhook
	// token, the user account and the listener (bind address, port, SSL port, certificate and key,
	// URL base).
	// Even then, a switch to authentication None or External is refused (it can only be made in
	// Settings, config.xml or DUPEARR__AUTH__METHOD), and so is anything that would leave Forms
	// authentication without a user (it would reopen the first-run setup to the network).
	RestoreSecuritySettings bool `json:"restoreSecuritySettings"`
}

// RestoreSummary describes a staged restore for the admin to review before it is applied: the
// security-relevant differences between the backup and the running instance, whether each one
// is restored, and what the restore changed in its copy of the database. Every session is
// signed out when the restore is applied (the session signing key is never restored).
type RestoreSummary struct {
	SecuritySettingsRestored bool            `json:"securitySettingsRestored"`
	Changes                  []RestoreChange `json:"changes"`
	CancelledRemovals        int64           `json:"cancelledRemovals"`
	InterruptedRemovals      int64           `json:"interruptedRemovals"`
	ReopenedGroups           int64           `json:"reopenedGroups"`
}

// RestoreChange is one setting that differs between the backup and the running instance.
// Setting is a HostConfig or Settings JSON property name, "users" or "webhookToken". Secret
// values (the API key, the webhook token) are never included: Current and Backup are then empty
// and Message says what differs.
type RestoreChange struct {
	Setting string `json:"setting"`
	Current string `json:"current"`
	Backup  string `json:"backup"`
	// Applied is true when the restore uses the backup's value, false when the current value is
	// kept.
	Applied bool   `json:"applied"`
	Message string `json:"message,omitempty"`
}

// securityField is a config.xml setting a restore keeps unless the security settings are restored.
type securityField struct {
	name   string // HostConfig JSON property name
	secret bool
	get    func(*config.Config) string
	set    func(dst, src *config.Config)
}

var securityFields = []securityField{
	{name: "authenticationMethod", get: func(c *config.Config) string { return c.AuthenticationMethod },
		set: func(d, s *config.Config) { d.AuthenticationMethod = s.AuthenticationMethod }},
	{name: "authenticationRequired", get: func(c *config.Config) string { return c.AuthenticationRequired },
		set: func(d, s *config.Config) { d.AuthenticationRequired = s.AuthenticationRequired }},
	{name: "apiKey", secret: true, get: func(c *config.Config) string { return c.ApiKey },
		set: func(d, s *config.Config) { d.ApiKey = s.ApiKey }},
	{name: "bindAddress", get: func(c *config.Config) string { return c.BindAddress },
		set: func(d, s *config.Config) { d.BindAddress = s.BindAddress }},
	{name: "port", get: func(c *config.Config) string { return strconv.Itoa(c.Port) },
		set: func(d, s *config.Config) { d.Port = s.Port }},
	{name: "urlBase", get: func(c *config.Config) string { return c.UrlBase },
		set: func(d, s *config.Config) { d.UrlBase = s.UrlBase }},
	{name: "enableSsl", get: func(c *config.Config) string { return strconv.FormatBool(c.EnableSsl) },
		set: func(d, s *config.Config) { d.EnableSsl = s.EnableSsl }},
	{name: "sslPort", get: func(c *config.Config) string { return strconv.Itoa(c.SslPort) },
		set: func(d, s *config.Config) { d.SslPort = s.SslPort }},
	{name: "sslCertPath", get: func(c *config.Config) string { return c.SslCertPath },
		set: func(d, s *config.Config) { d.SslCertPath = s.SslCertPath }},
	{name: "sslKeyPath", get: func(c *config.Config) string { return c.SslKeyPath },
		set: func(d, s *config.Config) { d.SslKeyPath = s.SslKeyPath }},
}

// currentConfig returns what the live config.xml holds (without environment overrides).
func (s *Service) currentConfig() (config.Config, error) {
	if s.cfg != nil {
		return s.cfg.File(), nil
	}
	data, err := readLimited(s.configPath(), maxConfigSize)
	if err != nil {
		return config.Config{}, fmt.Errorf("read the current %s: %w", configFileName, err)
	}
	return config.ParseFile(data)
}

// restorePlan is what a restore takes from the backup, decided before anything is written.
type restorePlan struct {
	config       []byte // the config.xml to stage
	restoreUsers bool   // users come from the backup (else from the running instance)
}

// planRestore validates the backup's config.xml and decides, per security setting, whether the
// backup's value or the current one is staged, recording the differences in sum.
//
// The backup's config.xml must be valid on its own (without DUPEARR__ environment overrides),
// apart from problems the current config.xml already has, and the file that is staged must pass
// config.Manager.CheckFile: valid with the current overrides applied, and on its own. So an
// override cannot mask a value that would stop Dupearr from starting once it is removed.
func (s *Service) planRestore(data []byte, backupUsers, liveUsers []userRow, opts RestoreOptions, sum *RestoreSummary) (*restorePlan, error) {
	if len(bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")))) == 0 {
		return nil, fmt.Errorf("%w: %s is empty", ErrInvalidBackup, configFileName)
	}
	backupCfg, err := config.ParseFile(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBackup, err)
	}
	current, err := s.currentConfig()
	if err != nil {
		return nil, err
	}
	if errs := config.NewProblems(current, backupCfg); len(errs) > 0 {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidBackup, configFileName, config.ValidationErrors(errs))
	}

	plan := &restorePlan{restoreUsers: opts.RestoreSecuritySettings}
	if opts.RestoreSecuritySettings && len(backupUsers) == 0 && len(liveUsers) > 0 {
		plan.restoreUsers = false
	}
	finalUsers := liveUsers
	if plan.restoreUsers {
		finalUsers = backupUsers
	}
	if c := usersChange(liveUsers, backupUsers, plan.restoreUsers, opts); c != nil {
		sum.Changes = append(sum.Changes, *c)
	}

	_, final, err := config.RewriteFile(data, func(c *config.Config) {
		for _, f := range securityFields {
			cur, bak := f.get(&current), f.get(c)
			if cur == bak {
				continue
			}
			change := RestoreChange{Setting: f.name, Current: cur, Backup: bak, Applied: opts.RestoreSecuritySettings}
			if f.secret {
				change.Current, change.Backup = "", ""
				change.Message = "The backup has a different API key."
			}
			if f.name == "authenticationMethod" && change.Applied {
				switch {
				case bak == config.AuthNone || bak == config.AuthExternal:
					change.Applied = false
					change.Message = "Authentication " + bak + " is never restored from a backup; choose it in Settings → General, " +
						"config.xml or DUPEARR__AUTH__METHOD if you really want it."
				case bak == config.AuthForms && len(finalUsers) == 0:
					change.Applied = false
					change.Message = "The backup has no user: switching to Forms would reopen the first-run setup to anyone on the network."
				}
			}
			if !change.Applied {
				f.set(c, &current)
			}
			sum.Changes = append(sum.Changes, change)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBackup, err)
	}
	if err := s.checkStagedConfig(current, final); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidBackup, configFileName, err)
	}
	// Logging decides what the operator can see afterwards (GAP-04): a backup must not quietly turn
	// the log level down. Security events are recorded in the history whatever the level.
	for _, f := range []struct{ name, cur, bak string }{
		{"logLevel", current.LogLevel, final.LogLevel},
		{"logSizeLimit", strconv.Itoa(current.LogSizeLimit), strconv.Itoa(final.LogSizeLimit)},
	} {
		if f.cur != f.bak {
			sum.Changes = append(sum.Changes, RestoreChange{Setting: f.name, Current: f.cur, Backup: f.bak, Applied: true})
		}
	}
	// Only the known settings come from the archive: the staged file is the live config.xml (its
	// unknown elements and comments included) with those values. An element this build does not
	// know is never adopted from a backup — a later build may read it (a trust list, for example)
	// without the review securityFields gives the settings it knows.
	out, err := config.RenderOnto(s.liveConfigFile(), final)
	if err != nil {
		return nil, fmt.Errorf("render the staged %s: %w", configFileName, err)
	}
	if staged, err := config.ParseFile(out); err != nil || staged != final {
		return nil, fmt.Errorf("render the staged %s: the rendered file does not hold the planned settings", configFileName)
	}
	plan.config = out
	return plan, nil
}

// liveConfigFile returns the contents of the live config.xml, or nil when it cannot be read or is
// not a valid config.xml (the staged file is then rendered from scratch).
func (s *Service) liveConfigFile() []byte {
	data, err := readLimited(s.configPath(), maxConfigSize)
	if err != nil {
		return nil
	}
	if _, err := config.ParseFile(data); err != nil {
		return nil
	}
	return data
}

// checkStagedConfig validates the config.xml that will be staged the way config.Manager.Update
// validates a save.
func (s *Service) checkStagedConfig(current, final config.Config) error {
	if s.cfg != nil {
		return s.cfg.CheckFile(final)
	}
	if errs := config.NewProblems(current, final); len(errs) > 0 {
		return config.ValidationErrors(errs)
	}
	return nil
}

// usersChange describes how the backup's users differ from the running instance's (nil when
// they are the same). Password hashes are compared, never shown.
func usersChange(live, backup []userRow, restored bool, opts RestoreOptions) *RestoreChange {
	names := func(us []userRow) string {
		n := make([]string, len(us))
		for i, u := range us {
			n[i] = u.username
		}
		return strings.Join(n, ", ")
	}
	same := len(live) == len(backup)
	passwordOnly := same
	for i := 0; same && i < len(live); i++ {
		if !strings.EqualFold(live[i].username, backup[i].username) {
			same, passwordOnly = false, false
		} else if live[i].hash != backup[i].hash {
			same = false
		}
	}
	if same {
		return nil
	}
	c := &RestoreChange{Setting: "users", Current: names(live), Backup: names(backup), Applied: restored}
	switch {
	case opts.RestoreSecuritySettings && !restored:
		c.Message = "The backup has no user; the current one is kept so that the first-run setup is not reopened."
	case passwordOnly:
		c.Message = "The backup has a different password."
	}
	return c
}

// webhookTokenChange decides whether the backup's webhook token is restored (only with
// RestoreSecuritySettings, and only when the backup has one: otherwise the running instance's is
// kept) and records a difference in sum without either value.
func webhookTokenChange(ctx context.Context, r *rebuild, opts RestoreOptions, sum *RestoreSummary) (restore bool, err error) {
	bak, _, err := r.settingValue(ctx, stagedSchema, webhookTokenSetting)
	if err != nil {
		return false, invalidDB("cannot read the webhook token: %v", err)
	}
	cur := ""
	if r.hasLive {
		if cur, _, err = r.settingValue(ctx, liveSchema, webhookTokenSetting); err != nil {
			return false, fmt.Errorf("read the current webhook token: %w", err)
		}
	}
	bak, cur = strings.TrimSpace(bak), strings.TrimSpace(cur)
	restore = opts.RestoreSecuritySettings && bak != ""
	if bak != cur {
		c := RestoreChange{Setting: "webhookToken", Applied: restore,
			Message: "The backup has a different webhook token (webhook URLs in Radarr, Sonarr and Plex carry it)."}
		if bak == "" {
			c.Message = "The backup has no webhook token; the current one is kept."
		}
		sum.Changes = append(sum.Changes, c)
	}
	return restore, nil
}

// settingsChanges records the differences of the settings and connections that decide which
// files are removed and how (dry run, automatic mode, deletion methods, limits, the recycle bin,
// media servers, applications, path mappings, notification connections), all of which the restore
// applies — except dry run, full-disc removal and "always keep a playable copy", which are always
// safe after a restore (forceSafeSettings) until the admin has reviewed the rest. Notification
// connections are listed by name and kind only (their URLs carry credentials); one whose settings
// changed under the same name is flagged without them (notificationDestinationChanges).
func settingsChanges(ctx context.Context, r *rebuild, sum *RestoreSummary) error {
	bak, _, err := r.settingsDoc(ctx, stagedSchema)
	if err != nil {
		return invalidDB("%v", err)
	}
	cur, curOK := models.DefaultSettings(), false
	if r.hasLive {
		var err error
		if cur, _, err = r.settingsDoc(ctx, liveSchema); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
		} else {
			curOK = true
		}
	}
	current := func(v string) string {
		if !curOK {
			return ""
		}
		return v
	}
	if !bak.DryRun {
		sum.Changes = append(sum.Changes, RestoreChange{Setting: "dryRun", Current: current(strconv.FormatBool(cur.DryRun)),
			Backup: "false", Applied: false,
			Message: "Dry run is on after a restore: review the restored settings, connections and path mappings, " +
				"then turn it off in Settings → Media Management."})
	} else if curOK && !cur.DryRun {
		sum.Changes = append(sum.Changes, RestoreChange{Setting: "dryRun", Current: "false", Backup: "true", Applied: true})
	}
	// Like dry run, full-disc removal is off and "always keep a Plex-playable copy" on after a
	// restore (forceSafeSettings) until the admin turns them back (GAP-04).
	switch {
	case bak.AllowDiscRemoval:
		sum.Changes = append(sum.Changes, RestoreChange{Setting: "allowDiscRemoval", Current: current(strconv.FormatBool(cur.AllowDiscRemoval)),
			Backup: "true", Applied: false,
			Message: "Removing full discs is off after a restore: turn it on again in Settings → Media Management once you have reviewed the restored settings."})
	case curOK && cur.AllowDiscRemoval:
		sum.Changes = append(sum.Changes, RestoreChange{Setting: "allowDiscRemoval", Current: "true", Backup: "false", Applied: true})
	}
	switch {
	case !bak.KeepPlayableCopy:
		sum.Changes = append(sum.Changes, RestoreChange{Setting: "keepPlayableCopy", Current: current(strconv.FormatBool(cur.KeepPlayableCopy)),
			Backup: "false", Applied: false,
			Message: "Always keeping a Plex-playable copy is on after a restore: turn it off again in Settings → Media Management if you really want to."})
	case curOK && !cur.KeepPlayableCopy:
		sum.Changes = append(sum.Changes, RestoreChange{Setting: "keepPlayableCopy", Current: "false", Backup: "true", Applied: true})
	}
	for _, f := range []struct {
		name     string
		cur, bak string
	}{
		{"mode", cur.Mode, bak.Mode},
		{"deletionMethods", strings.Join(cur.DeletionMethods, ", "), strings.Join(bak.DeletionMethods, ", ")},
		{"recycleBinPath", cur.RecycleBinPath, bak.RecycleBinPath},
		{"recycleBinCleanupDays", strconv.Itoa(cur.RecycleBinCleanupDays), strconv.Itoa(bak.RecycleBinCleanupDays)},
		{"minAgeHours", strconv.Itoa(cur.MinAgeHours), strconv.Itoa(bak.MinAgeHours)},
		{"maxDeletionsPerRun", strconv.Itoa(cur.MaxDeletionsPerRun), strconv.Itoa(bak.MaxDeletionsPerRun)},
		{"maxBytesPerRunGb", strconv.FormatFloat(float64(cur.MaxBytesPerRunGB), 'f', -1, 64), strconv.FormatFloat(float64(bak.MaxBytesPerRunGB), 'f', -1, 64)},
		{"stableScansRequired", strconv.Itoa(cur.StableScansRequired), strconv.Itoa(bak.StableScansRequired)},
		{"detectDiscs", strconv.FormatBool(cur.DetectDiscs), strconv.FormatBool(bak.DetectDiscs)},
		{"historyRetentionDays", strconv.Itoa(cur.HistoryRetentionDays), strconv.Itoa(bak.HistoryRetentionDays)},
	} {
		if !curOK || f.cur != f.bak {
			sum.Changes = append(sum.Changes, RestoreChange{Setting: f.name, Current: current(f.cur), Backup: f.bak, Applied: true})
		}
	}
	for _, l := range []struct{ name, query string }{
		{"mediaServers", `SELECT name || ' → ' || url FROM %s.media_servers ORDER BY 1`},
		{"arrInstances", `SELECT kind || ' ' || name || ' → ' || url FROM %s.arr_instances ORDER BY 1`},
		{"pathMappings", `SELECT source_type || ' ' || source_id || ': ' || remote_path || ' → ' || local_path FROM %s.path_mappings ORDER BY 1`},
		{"notifications", `SELECT name || ' (' || kind || ')' FROM %s.notifications ORDER BY 1`},
	} {
		bakList, err := r.list(ctx, fmt.Sprintf(l.query, stagedSchema))
		if err != nil {
			return invalidDB("cannot read %s: %v", l.name, err)
		}
		curList := ""
		if r.hasLive {
			if curList, err = r.list(ctx, fmt.Sprintf(l.query, liveSchema)); err != nil {
				curList = ""
			}
		}
		if curList != bakList {
			sum.Changes = append(sum.Changes, RestoreChange{Setting: l.name, Current: curList, Backup: bakList, Applied: true})
		}
	}
	return notificationDestinationChanges(ctx, r, sum)
}

// notificationDestinationChanges records the notification connections whose destination or
// credentials (their settings) differ between the backup and the running instance under the same
// name and kind (GAP-04): the name/kind list above does not show a connection redirected to
// another endpoint, and the URLs and tokens themselves are secrets that are never shown.
func notificationDestinationChanges(ctx context.Context, r *rebuild, sum *RestoreSummary) error {
	if !r.hasLive {
		return nil
	}
	read := func(schema string) (map[string][32]byte, error) {
		rows, err := r.conn.QueryContext(ctx, `SELECT name, kind, CAST(settings AS TEXT) FROM `+schema+`.notifications`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string][32]byte{}
		for rows.Next() {
			var name, kind, settings sql.NullString
			if err := rows.Scan(&name, &kind, &settings); err != nil {
				return nil, err
			}
			out[name.String+" ("+kind.String+")"] = sha256.Sum256([]byte(canonicalJSON(settings.String)))
		}
		return out, rows.Err()
	}
	bak, err := read(stagedSchema)
	if err != nil {
		return invalidDB("cannot read notifications: %v", err)
	}
	cur, err := read(liveSchema)
	if err != nil {
		return nil // the current connections cannot be read: the name/kind list shows them all
	}
	var changed []string
	for name, h := range bak {
		if c, ok := cur[name]; ok && c != h {
			changed = append(changed, name)
		}
	}
	if len(changed) == 0 {
		return nil
	}
	slices.Sort(changed)
	list := strings.Join(changed, "; ")
	if len(list) > maxListText {
		list = list[:maxListText] + "…"
	}
	sum.Changes = append(sum.Changes, RestoreChange{Setting: "notificationDestinations", Backup: list, Applied: true,
		Message: "These notification connections send to a different destination or with different credentials in the backup " +
			"(not shown: they are secrets). Check them in Settings → Connections before you rely on them."})
	return nil
}

// canonicalJSON re-encodes a JSON document with sorted keys (so formatting never counts as a
// change); invalid JSON is returned as it is.
func canonicalJSON(s string) string {
	var v any
	if json.Unmarshal([]byte(s), &v) != nil {
		return s
	}
	out, err := json.Marshal(v)
	if err != nil {
		return s
	}
	return string(out)
}

// maxListText bounds a list shown in a RestoreChange.
const maxListText = 2000

// list joins the single-column rows of query with "; " (at most maxListText bytes).
func (r *rebuild) list(ctx context.Context, query string) (string, error) {
	rows, err := r.conn.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return "", err
		}
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, v.String))
		if b.Len() > maxListText {
			return b.String()[:maxListText] + "…", rows.Err()
		}
	}
	return b.String(), rows.Err()
}

// securityFingerprint summarises the running instance's security state — the security settings of
// config.xml, the account and the webhook token — as a SHA-256 hex digest (see securityStateFile).
func (s *Service) securityFingerprint(ctx context.Context) (string, error) {
	cur, err := s.currentConfig()
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, f := range securityFields {
		fmt.Fprintf(h, "%s=%q\n", f.name, f.get(&cur))
	}
	if s.st != nil {
		n, err := s.st.Users().Count(ctx)
		if err != nil {
			return "", fmt.Errorf("read the current user: %w", err)
		}
		fmt.Fprintf(h, "users=%d\n", n)
		if v, ok, err := s.st.Settings().GetValue(ctx, userIDSetting); err != nil {
			return "", fmt.Errorf("read the current user: %w", err)
		} else if ok {
			if id, perr := strconv.ParseInt(strings.TrimSpace(v), 10, 64); perr == nil {
				if u, err := s.st.Users().GetByID(ctx, id); err == nil {
					fmt.Fprintf(h, "user=%d|%q|%q\n", u.ID, u.Username, u.PasswordHash)
				}
			}
		}
		tok, _, err := s.st.Settings().GetValue(ctx, webhookTokenSetting)
		if err != nil {
			return "", fmt.Errorf("read the webhook token: %w", err)
		}
		fmt.Fprintf(h, "webhookToken=%q\n", tok)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
