package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/sl0wz3r/dupearr/internal/audit"
	"github.com/sl0wz3r/dupearr/internal/auth"
	"github.com/sl0wz3r/dupearr/internal/backup"
	"github.com/sl0wz3r/dupearr/internal/config"
	"github.com/sl0wz3r/dupearr/internal/database"
	"github.com/sl0wz3r/dupearr/internal/store"
)

// authMethodEnv is the environment variable that overrides config.xml's AuthenticationMethod.
const authMethodEnv = auth.EnvAuthMethod

// resetAuthRequestName is the file `dupearr reset-auth` creates in the data directory while a
// server runs with it (GAP-13): the server keeps its credentials in memory (and writes config.xml
// back on every settings save), so resetting the files behind its back would not lock out a held
// credential. The server looks for the file every resetWatchInterval; when it appears it refuses
// every credential at once, restarts, and resets authentication at start — before it accepts
// requests again — then removes the file.
const resetAuthRequestName = ".reset-auth-requested"

// resetWatchInterval is how often a running server looks for a reset request.
const resetWatchInterval = time.Second

// resetAuthWait is how long `dupearr reset-auth` waits for a running server to apply a request.
var resetAuthWait = 60 * time.Second

// resetAuth is `dupearr reset-auth`, the recovery for a lost password or a suspected compromise:
// it switches authentication to Forms with authentication required, deletes the user (the next
// visit to the web UI shows first-run setup) and replaces every stored credential that a previous
// holder may still have: the API key, the webhook token and the session signing key (every session
// and device cookie is signed out). Settings forced by DUPEARR__ environment variables are left in
// config.xml as they are — the environment would override them anyway — and the message says which
// stay in effect.
//
// With the data directory's lock free (no server runs), the reset is made here, holding the lock
// so that no server starts meanwhile. While a server holds it, the reset is handed to that server
// (resetAuthRequestName) and this command waits for it; when the lock state cannot be determined
// (a filesystem without locks), the request is left for the server to apply at its next start.
func resetAuth(ctx context.Context, dataDir string, stdout io.Writer) error {
	lock, warning, err := acquireAppLock(dataDir)
	switch {
	case errors.Is(err, errAppLocked):
		return handResetToServer(dataDir, stdout)
	case err != nil:
		return err
	case warning != "":
		if err := writeResetRequest(dataDir); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Could not tell whether Dupearr is running (%s).\n", warning)
		fmt.Fprintln(stdout, "The reset was requested: a running Dupearr applies it within seconds (it signs everyone out and restarts); "+
			"otherwise it is applied when Dupearr starts. Restart Dupearr now to be sure.")
		return nil
	}
	defer lock.release()

	cfg, err := config.Load(dataDir)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// A trigger could re-create the deleted user (a backdoor planted through a tampered backup
	// restored by an earlier version): remove any before the database is opened.
	dbPath := filepath.Join(dataDir, dbFileName)
	removed, err := backup.RemoveForeignSchemaObjects(ctx, dataDir, dbPath)
	if err != nil {
		return fmt.Errorf("check the database schema: %w", err)
	}
	for _, obj := range removed {
		fmt.Fprintf(stdout, "Removed %s from the database: Dupearr does not create it, so it was added by someone else "+
			"(for example through a tampered backup). Review the API key, connections, notifications and settings.\n", obj)
	}
	db, err := database.Open(ctx, dbPath, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	res, err := resetAuthState(ctx, cfg, db)
	if err != nil {
		return err
	}
	// A request left for a server is fulfilled by this reset.
	if err := removeResetRequest(dataDir); err != nil {
		fmt.Fprintf(stdout, "Could not remove %s (%v): Dupearr resets authentication again at its next start.\n", resetAuthRequestName, err)
	}
	audit.Record(ctx, db, nil, audit.KindAuthReset, "Authentication reset by dupearr reset-auth", "while", "stopped")
	res.print(stdout, cfg)
	if !res.envMethod || cfg.Get().AuthenticationMethod == config.AuthForms {
		fmt.Fprintln(stdout, "Start Dupearr, then open the web UI to create new credentials (the setup code is printed in Dupearr's log).")
	} else {
		fmt.Fprintln(stdout, "Start Dupearr for the changes to take effect.")
	}
	return nil
}

// handResetToServer asks the server that holds the data directory's lock to reset authentication
// and waits (resetAuthWait) until it has.
func handResetToServer(dataDir string, stdout io.Writer) error {
	if err := writeResetRequest(dataDir); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "Dupearr is running with this data directory. It keeps the current credentials in memory, so the reset "+
		"is carried out by the server itself: it refuses every API key, session and webhook token right away, restarts, and "+
		"resets authentication before it accepts requests again.")
	path := filepath.Join(dataDir, resetAuthRequestName)
	deadline := time.Now().Add(resetAuthWait)
	for time.Now().Before(deadline) {
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(stdout, "Authentication was reset: the API key, the webhook token and the session signing key were replaced, "+
				"every session was signed out and the account was deleted (unless environment variables force the API key or the "+
				"authentication settings). Update the API key in your scripts and the webhook URLs in Radarr, Sonarr and Plex.")
			fmt.Fprintln(stdout, "Open the web UI to create new credentials (the setup code is printed in Dupearr's log).")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(stdout, "Dupearr has not applied the request yet (it may be starting up or stuck). Restart it now (Docker: "+
		"docker restart <container>): the reset is applied when it starts, before any request is accepted. Until then %s stays in %s.\n",
		resetAuthRequestName, dataDir)
	return nil
}

// writeResetRequest creates the reset request file (never through a symbolic link).
func writeResetRequest(dataDir string) error {
	path := filepath.Join(dataDir, resetAuthRequestName)
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode().IsRegular() {
			return nil // already requested
		}
		return fmt.Errorf("%s exists and is not a regular file; remove it", path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("request the reset: %w", err)
	}
	return f.Close()
}

// resetRequested reports whether a reset request is waiting in the data directory.
func resetRequested(dataDir string) bool {
	_, err := os.Lstat(filepath.Join(dataDir, resetAuthRequestName))
	return err == nil
}

// removeResetRequest removes the reset request (a missing one is fine).
func removeResetRequest(dataDir string) error {
	if err := os.Remove(filepath.Join(dataDir, resetAuthRequestName)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// authResetResult says what a reset could not change (environment overrides), for the messages.
type authResetResult struct {
	envMethod, envRequired, envAPIKey bool
}

// resetAuthState resets authentication in config.xml (cfg) and the database (st): Forms,
// authentication required, a new API key (unless environment variables force them), no user,
// and new stored credentials (session signing key, webhook token, session generation).
func resetAuthState(ctx context.Context, cfg *config.Manager, st store.Store) (authResetResult, error) {
	env := map[string]bool{}
	for _, k := range cfg.EnvOverrides() {
		env[k] = true
	}
	res := authResetResult{envMethod: env["authenticationMethod"], envRequired: env["authenticationRequired"], envAPIKey: env["apiKey"]}
	if _, err := cfg.Update(func(c *config.Config) {
		if !res.envMethod {
			c.AuthenticationMethod = config.AuthForms
		}
		if !res.envRequired {
			c.AuthenticationRequired = config.AuthRequiredEnabled
		}
		if !res.envAPIKey {
			c.ApiKey = config.GenerateAPIKey()
		}
	}); err != nil {
		return res, fmt.Errorf("update config: %w", err)
	}
	if err := st.Users().DeleteAll(ctx); err != nil {
		return res, fmt.Errorf("delete user: %w", err)
	}
	if n, err := st.Users().Count(ctx); err != nil {
		return res, fmt.Errorf("delete user: %w", err)
	} else if n != 0 {
		return res, fmt.Errorf("delete user: %d user(s) still exist after deleting them; the database was modified by someone else", n)
	}
	if err := auth.ResetStoredCredentials(ctx, st); err != nil {
		return res, fmt.Errorf("replace the stored credentials: %w", err)
	}
	return res, nil
}

// print writes what the reset did and what environment variables kept.
func (res authResetResult) print(stdout io.Writer, cfg *config.Manager) {
	if !res.envMethod {
		fmt.Fprintf(stdout, "Authentication reset to %s with no user.\n", config.AuthForms)
	} else {
		method := cfg.Get().AuthenticationMethod
		fmt.Fprintf(stdout, "Deleted the user. The authentication method is set to %s by the %s environment variable, so it was not changed in config.xml.\n", method, authMethodEnv)
		if method != config.AuthForms {
			fmt.Fprintf(stdout, "Authentication stays %s while that variable is set. To get the first-run setup, remove the variable (or set it to %s) and restart Dupearr.\n", method, config.AuthForms)
		}
	}
	if res.envRequired {
		fmt.Fprintln(stdout, "Authentication Required is set by the DUPEARR__AUTH__REQUIRED environment variable and was not changed.")
	} else {
		fmt.Fprintln(stdout, "Authentication is required for every address again (Authentication Required: Enabled).")
	}
	if res.envAPIKey {
		fmt.Fprintln(stdout, "The API key is set by the DUPEARR__AUTH__APIKEY environment variable and was NOT replaced: change that variable if the key may be known to someone else.")
	} else {
		fmt.Fprintln(stdout, "Replaced the API key: scripts and tools using the old key stop working (Settings → General shows the new one after you sign in).")
	}
	fmt.Fprintln(stdout, "Replaced the webhook token: update the webhook URLs in Radarr, Sonarr and Plex (Settings → Connections → Webhooks).")
	fmt.Fprintln(stdout, "Every session was signed out (new session signing key).")
}

// applyRequestedAuthReset carries out a reset request at start (see resetAuthRequestName), after
// a staged restore was applied and the database opened, before authentication is set up.
func (a *app) applyRequestedAuthReset(ctx context.Context) error {
	if !resetRequested(a.opts.dataDir) {
		return nil
	}
	res, err := resetAuthState(ctx, a.cfg, a.db)
	if err != nil {
		return fmt.Errorf("reset authentication as requested by dupearr reset-auth: %w", err)
	}
	if err := removeResetRequest(a.opts.dataDir); err != nil {
		return fmt.Errorf("remove the reset request %s: %w", resetAuthRequestName, err)
	}
	// Error: the operator must see it whatever the log level.
	a.log.Error("Authentication was reset by dupearr reset-auth: the API key, the webhook token and the session key were "+
		"replaced, every session was signed out and the account was deleted; open the web UI to create new credentials",
		"apiKeyKept", res.envAPIKey, "methodKept", res.envMethod, "requirementKept", res.envRequired)
	audit.Record(ctx, a.db, a.log, audit.KindAuthReset, "Authentication reset by dupearr reset-auth", "while", "running")
	return nil
}

// watchResetRequest looks for a reset request every interval while the server runs: when one
// appears, every credential is refused at once and the server restarts, which applies the reset
// before any request is accepted again (applyRequestedAuthReset).
func (a *app) watchResetRequest(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !resetRequested(a.opts.dataDir) {
			continue
		}
		if a.auth != nil {
			a.auth.Lockdown()
		}
		a.log.Error("dupearr reset-auth was run: every credential is refused from now on; restarting to reset authentication")
		a.requestRestart()
		return
	}
}
