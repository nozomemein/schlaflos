// Package doctor inspects an installation and reports each check as ok,
// warn, fail, or skip. It performs no mutation. Checks that need root are
// skipped for unprivileged users instead of failing.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/reconcile"
	"github.com/nozomemein/schlaflos/internal/safefs"
	"github.com/nozomemein/schlaflos/internal/state"
)

// Level classifies a check result.
type Level string

const (
	OK   Level = "ok"
	Warn Level = "warn"
	Fail Level = "fail"
	Skip Level = "skip"
)

// Check is one result line.
type Check struct {
	Level  Level
	Name   string
	Detail string
}

// LaunchdQuerier is the read-only launchd boundary.
type LaunchdQuerier interface {
	Loaded(ctx context.Context) (bool, error)
}

// Doctor holds the read-only boundaries.
type Doctor struct {
	Layout  layout.Layout
	FS      safefs.Checker
	Store   state.Store
	Power   reconcile.PowerAdapter
	Wake    reconcile.WakeAdapter
	Launchd LaunchdQuerier
	IsRoot  bool
	Clock   func() time.Time
}

// Run executes every check. It returns the checks and whether any failed.
func (d Doctor) Run(ctx context.Context) ([]Check, bool) {
	var checks []Check
	add := func(level Level, name, format string, args ...any) {
		checks = append(checks, Check{Level: level, Name: name, Detail: fmt.Sprintf(format, args...)})
	}

	installed, _ := safefs.Exists(d.Layout.PlistPath)
	if !installed {
		if exists, _ := safefs.Exists(d.Layout.SupportDir); !exists {
			add(OK, "installation", "schlaflos is not installed")
			return checks, false
		}
		add(Warn, "installation", "%s exists but the LaunchDaemon plist is missing", d.Layout.SupportDir)
	}

	// Filesystem invariants. Lstat works for unprivileged users.
	for _, p := range []struct {
		path string
		mode os.FileMode
		dir  bool
	}{
		{d.Layout.SupportDir, layout.ModeDir, true},
		{d.Layout.BinDir, layout.ModeDir, true},
		{d.Layout.StateDir, layout.ModeDir, true},
		{d.Layout.Binary, layout.ModeBinary, false},
		{d.Layout.ConfigPath, layout.ModeConfig, false},
		{d.Layout.PlistPath, layout.ModePlist, false},
		{d.Layout.StatePath, layout.ModeState, false},
		{d.Layout.StatusPath, layout.ModeStatus, false},
	} {
		var err error
		if p.dir {
			err = d.FS.VerifyDir(p.path, p.mode)
		} else {
			err = d.FS.VerifyFile(p.path, p.mode)
		}
		if err != nil {
			add(Fail, "path "+p.path, "%v", err)
		} else {
			add(OK, "path "+p.path, "root-owned, mode %04o", p.mode)
		}
	}

	var cfgPoll time.Duration
	if d.IsRoot {
		cfg, err := reconcile.LoadInstalledConfig(d.FS, d.Layout)
		if err != nil {
			add(Fail, "configuration", "%v", err)
		} else {
			cfgPoll = cfg.PollInterval
			add(OK, "configuration", "valid (poll interval %s, %d window(s), %d process guard(s), wake enabled=%v)", cfg.PollInterval, len(cfg.Windows), len(cfg.Guards.Process), cfg.Wake.Enabled)
		}
		loaded, err := d.Launchd.Loaded(ctx)
		switch {
		case err != nil:
			add(Fail, "launchd", "%v", err)
		case loaded:
			add(OK, "launchd", "%s is loaded", layout.Label)
		default:
			add(Fail, "launchd", "%s is not loaded (run `sudo schlaflos config apply %s` or reinstall)", layout.Label, d.Layout.ConfigPath)
		}
	} else {
		add(Skip, "configuration", "requires root to read %s", d.Layout.ConfigPath)
		add(Skip, "launchd", "requires root to query the system domain")
	}

	status, err := d.Store.ReadStatus()
	if err != nil {
		add(Fail, "status", "%v", err)
	} else {
		poll := cfgPoll
		if poll == 0 && status.PollIntervalSeconds > 0 {
			poll = time.Duration(status.PollIntervalSeconds) * time.Second
		}
		age := d.Clock().Sub(status.GeneratedAt)
		switch {
		case status.Mode == state.ModeEmergencyOff:
			add(Warn, "status", "emergency-off is in effect since %s; the service is unloaded", status.GeneratedAt.Local().Format(time.RFC3339))
		case poll > 0 && age > 3*poll+time.Minute:
			add(Warn, "status", "last reconciliation was %s ago (poll interval %s); the service may not be running", age.Round(time.Second), poll)
		default:
			add(OK, "status", "last reconciliation %s ago (%s)", age.Round(time.Second), status.Reason)
		}
		if !status.RunOK && status.LastError != nil {
			add(Warn, "last run", "failed with %s at %s", status.LastError.Category, status.LastError.At.Local().Format(time.RFC3339))
		}
		for _, c := range status.Conflicts {
			add(Warn, "conflict", "%s reported by the last reconciliation", c)
		}
		for _, g := range status.Degraded {
			add(Warn, "degraded", "%s reported by the last reconciliation", g)
		}
	}

	// pmset is readable without privileges.
	observedSleep, err := d.Power.SleepDisabled(ctx)
	if err != nil {
		add(Fail, "disablesleep", "%v", err)
	} else if status != nil && status.Observed != nil && *status.Observed != observedSleep {
		add(Warn, "disablesleep", "pmset reports %d but the last reconciliation observed %d; an external change is pending reconciliation", b2i(observedSleep), b2i(*status.Observed))
	} else {
		add(OK, "disablesleep", "pmset reports %d", b2i(observedSleep))
	}
	observedWake, err := d.Wake.Current(ctx)
	if err != nil {
		add(Fail, "wake schedule", "%v", err)
	} else if status != nil && status.WakeInstalled != nil && observedWake.String() != *status.WakeInstalled {
		add(Warn, "wake schedule", "pmset reports %s but schlaflos installs %s", observedWake, *status.WakeInstalled)
	} else {
		add(OK, "wake schedule", "pmset reports %s", observedWake)
	}

	if d.IsRoot {
		st, err := d.Store.LoadState()
		switch {
		case errors.Is(err, state.ErrNoState):
			add(Fail, "private state", "missing; reconciliation performs no mutation until it is recovered (emergency-off, then reinstall)")
		case err != nil:
			add(Fail, "private state", "%v", err)
		default:
			add(OK, "private state", "install id %s, baseline disablesleep=%d, baseline wake %s", st.InstallID, b2i(st.SleepBaseline.Disabled), st.WakeBaseline.Schedule)
			if st.LastSleepWritten != nil && observedSleep != st.LastSleepWritten.Disabled {
				add(Warn, "ownership", "disablesleep=%d differs from the last value written by schlaflos (%d); uninstall will preserve it", b2i(observedSleep), b2i(st.LastSleepWritten.Disabled))
			}
			if st.LastWakeWritten != nil && !observedWake.Equal(st.LastWakeWritten.Schedule) {
				add(Warn, "ownership", "wake schedule %s differs from the last value written by schlaflos (%s); uninstall will preserve it", observedWake, st.LastWakeWritten.Schedule)
			}
			if st.PendingSleepWrite != nil || st.PendingWakeWrite != nil {
				add(Warn, "ownership", "an interrupted write is pending resolution by the next reconciliation")
			}
		}
	} else {
		add(Skip, "private state", "requires root to read %s", d.Layout.StatePath)
	}

	failed := false
	for _, c := range checks {
		if c.Level == Fail {
			failed = true
		}
	}
	return checks, failed
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
