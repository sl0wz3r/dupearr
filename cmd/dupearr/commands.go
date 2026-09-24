package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sl0wz3r/dupearr/internal/backup"
	"github.com/sl0wz3r/dupearr/internal/commands"
	"github.com/sl0wz3r/dupearr/internal/models"
)

const (
	minutesPerDay = 24 * 60
	// commandRetention is how long finished commands are kept (Housekeeping).
	commandRetention = 30 * 24 * time.Hour
	// settingsReadTimeout bounds the settings read behind a dynamic task interval.
	settingsReadTimeout = 5 * time.Second
)

// syncLibrariesBody is the body of SyncLibraries.
type syncLibrariesBody struct {
	ServerID int64 `json:"serverId,omitempty"`
}

// backupBody is the body of Backup ("" = scheduled when run by the scheduler, else manual).
type backupBody struct {
	Type string `json:"type,omitempty"`
}

// registerCommands registers a handler for every command of docs/ARCHITECTURE.md §7 and the
// scheduled tasks that enqueue them. Every command is exclusive: none benefits from running
// concurrently with itself, and scans/queue processing/backups must not.
func (a *app) registerCommands() {
	m := a.commands
	m.Register(models.CmdDuplicateScan, a.cmdDuplicateScan, true)
	m.Register(models.CmdTargetedScan, a.cmdTargetedScan, true)
	m.Register(models.CmdProcessQueue, a.cmdProcessQueue, true)
	m.Register(models.CmdSyncLibraries, a.cmdSyncLibraries, true)
	m.Register(models.CmdCheckHealth, a.cmdCheckHealth, true)
	m.Register(models.CmdBackup, a.cmdBackup, true)
	m.Register(models.CmdHousekeeping, a.cmdHousekeeping, true)
	m.Register(models.CmdCleanRecycleBin, a.cmdCleanRecycleBin, true)

	m.RegisterTask(commands.TaskDef{
		Name: "Duplicate Scan", TaskName: models.CmdDuplicateScan, DefaultInterval: 360,
		IntervalFunc: a.settingsInterval(360, func(s models.Settings) int { return s.ScanIntervalMinutes }),
	})
	m.RegisterTask(commands.TaskDef{Name: "Process Queue", TaskName: models.CmdProcessQueue, DefaultInterval: 5})
	m.RegisterTask(commands.TaskDef{Name: "Sync Libraries", TaskName: models.CmdSyncLibraries, DefaultInterval: 720})
	m.RegisterTask(commands.TaskDef{Name: "Check Health", TaskName: models.CmdCheckHealth, DefaultInterval: 360})
	m.RegisterTask(commands.TaskDef{
		Name: "Backup", TaskName: models.CmdBackup, DefaultInterval: 7 * minutesPerDay,
		IntervalFunc: a.settingsInterval(7*minutesPerDay, func(s models.Settings) int { return s.BackupIntervalDays * minutesPerDay }),
	})
	m.RegisterTask(commands.TaskDef{Name: "Housekeeping", TaskName: models.CmdHousekeeping, DefaultInterval: minutesPerDay})
	m.RegisterTask(commands.TaskDef{Name: "Clean Recycle Bin", TaskName: models.CmdCleanRecycleBin, DefaultInterval: minutesPerDay})
}

// settingsInterval returns a TaskDef.IntervalFunc reading the interval (minutes, 0 = disabled)
// from the DB settings, falling back to def when they cannot be read.
func (a *app) settingsInterval(def int, pick func(models.Settings) int) func() int {
	return func() int {
		ctx, cancel := context.WithTimeout(context.Background(), settingsReadTimeout)
		defer cancel()
		s, err := a.db.Settings().Get(ctx)
		if err != nil {
			return def
		}
		if v := pick(s); v >= 0 {
			return v
		}
		return def
	}
}

// decodeBody unmarshals a command body; an empty or null body yields the zero value.
func decodeBody[T any](cmd *models.Command) (T, error) {
	var v T
	if len(cmd.Body) == 0 || string(cmd.Body) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(cmd.Body, &v); err != nil {
		return v, fmt.Errorf("invalid %s body: %w", cmd.Name, err)
	}
	return v, nil
}

func (a *app) cmdDuplicateScan(ctx context.Context, cmd *models.Command, progress func(string)) (string, error) {
	body, err := decodeBody[models.DuplicateScanBody](cmd)
	if err != nil {
		return "", err
	}
	run, err := a.scanner.FullScan(ctx, body, cmd.Trigger, progress)
	if err != nil {
		return "", err
	}
	a.processAutoApproved(ctx, run, cmd.Trigger)
	return scanSummary(run), nil
}

func (a *app) cmdTargetedScan(ctx context.Context, cmd *models.Command, progress func(string)) (string, error) {
	body, err := decodeBody[models.TargetedScanBody](cmd)
	if err != nil {
		return "", err
	}
	run, err := a.scanner.TargetedScan(ctx, body, cmd.Trigger)
	if err != nil {
		return "", err
	}
	a.processAutoApproved(ctx, run, cmd.Trigger)
	return scanSummary(run), nil
}

// processAutoApproved queues ProcessQueue right away when a scan auto-approved groups (auto mode)
// instead of waiting for the next scheduled run.
func (a *app) processAutoApproved(ctx context.Context, run *models.ScanRun, trigger string) {
	if run == nil || run.Stats.AutoApproved == 0 {
		return
	}
	if _, err := a.commands.Enqueue(ctx, models.CmdProcessQueue, struct{}{}, trigger); err != nil {
		a.log.Warn("Failed to queue ProcessQueue after auto-approval", "error", err)
	}
}

func scanSummary(run *models.ScanRun) string {
	if run == nil {
		return "Scan completed"
	}
	s := run.Stats
	msg := fmt.Sprintf("%d duplicate groups found (%d new, %d resolved)", s.GroupsFound, s.NewGroups, s.ResolvedGroups)
	if s.AutoApproved > 0 {
		msg += fmt.Sprintf(", %d auto-approved", s.AutoApproved)
	}
	if s.Errors > 0 {
		msg += fmt.Sprintf(", %d errors", s.Errors)
	}
	return msg
}

func (a *app) cmdProcessQueue(ctx context.Context, _ *models.Command, progress func(string)) (string, error) {
	sum, err := a.executor.ProcessQueue(ctx, progress)
	if err != nil {
		return "", err
	}
	if sum.Message != "" {
		return sum.Message, nil
	}
	return fmt.Sprintf("Processed %d actions: %d succeeded, %d dry run, %d skipped, %d failed",
		sum.Processed, sum.Succeeded, sum.DryRun, sum.Skipped, sum.Failed), nil
}

func (a *app) cmdSyncLibraries(ctx context.Context, cmd *models.Command, _ func(string)) (string, error) {
	body, err := decodeBody[syncLibrariesBody](cmd)
	if err != nil {
		return "", err
	}
	if err := a.scanner.SyncLibraries(ctx, body.ServerID); err != nil {
		return "", err
	}
	return "Libraries synced", nil
}

func (a *app) cmdCheckHealth(ctx context.Context, _ *models.Command, _ func(string)) (string, error) {
	issues := 0
	for _, r := range a.health.Run(ctx) {
		if r.Type != models.HealthOK {
			issues++
		}
	}
	if issues == 0 {
		return "No health issues", nil
	}
	return fmt.Sprintf("%d health issue(s)", issues), nil
}

func (a *app) cmdBackup(ctx context.Context, cmd *models.Command, _ func(string)) (string, error) {
	body, err := decodeBody[backupBody](cmd)
	if err != nil {
		return "", err
	}
	kind := body.Type
	if kind == "" {
		kind = backup.TypeManual
		if cmd.Trigger == models.TriggerScheduled {
			kind = backup.TypeScheduled
		}
	}
	switch kind {
	case backup.TypeScheduled, backup.TypeManual, backup.TypeUpdate:
	default:
		return "", fmt.Errorf("invalid backup type %q", kind)
	}

	b, err := a.backups.Create(ctx, kind)
	if err != nil {
		return "", err
	}
	msg := "Created backup " + b.Name
	if kind == backup.TypeScheduled {
		s, err := a.db.Settings().Get(ctx)
		if err != nil {
			a.log.Warn("Skipping backup cleanup: cannot read settings", "error", err)
		} else if s.BackupRetentionDays > 0 {
			n, err := a.backups.Cleanup(s.BackupRetentionDays)
			if err != nil {
				a.log.Warn("Backup cleanup failed", "error", err)
			} else if n > 0 {
				msg += fmt.Sprintf("; removed %d old backup(s)", n)
			}
		}
	}
	return msg, nil
}

// cmdHousekeeping prunes history and finished commands past their retention and compacts the DB.
func (a *app) cmdHousekeeping(ctx context.Context, _ *models.Command, progress func(string)) (string, error) {
	now := time.Now()
	s, err := a.db.Settings().Get(ctx)
	if err != nil {
		return "", fmt.Errorf("read settings: %w", err)
	}

	var removed []string
	if s.HistoryRetentionDays > 0 {
		progress("Pruning history")
		n, err := a.db.History().DeleteOlderThan(ctx, now.AddDate(0, 0, -s.HistoryRetentionDays))
		if err != nil {
			return "", fmt.Errorf("prune history: %w", err)
		}
		// Security events stay at least a year (docs/SECURITY.md GAP-12): shortening the
		// retention must not erase the record of who changed what.
		keep := max(s.HistoryRetentionDays, models.SecurityHistoryRetentionDays)
		ns, err := a.db.History().DeleteSecurityOlderThan(ctx, now.AddDate(0, 0, -keep))
		if err != nil {
			return "", fmt.Errorf("prune history: %w", err)
		}
		removed = append(removed, fmt.Sprintf("%d history event(s)", n+ns))
	}
	progress("Pruning finished commands")
	n, err := a.db.Commands().DeleteOlderThan(ctx, now.Add(-commandRetention))
	if err != nil {
		return "", fmt.Errorf("prune commands: %w", err)
	}
	removed = append(removed, fmt.Sprintf("%d command(s)", n))

	progress("Compacting database")
	if err := a.db.Vacuum(ctx); err != nil {
		return "", fmt.Errorf("vacuum database: %w", err)
	}
	return "Removed " + strings.Join(removed, " and ") + "; database compacted", nil
}

func (a *app) cmdCleanRecycleBin(ctx context.Context, _ *models.Command, _ func(string)) (string, error) {
	n, err := a.executor.CleanRecycleBin(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed %d recycle bin item(s)", n), nil
}
