// Package install implements the privileged lifecycle commands: install,
// config apply, uninstall, and emergency-off. Every step follows the
// invariants in docs/security.md: ancestors are verified and never repaired,
// baselines are persisted before the first mutation, and a baseline is
// restored only while the observed value is still the one schlaflos wrote.
package install

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/launchd"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wake"
	"github.com/nozomemein/schlaflos/internal/reconcile"
	"github.com/nozomemein/schlaflos/internal/safefs"
	"github.com/nozomemein/schlaflos/internal/state"
)

// LaunchdManager is the service-management boundary.
type LaunchdManager interface {
	Loaded(ctx context.Context) (bool, error)
	Bootstrap(ctx context.Context, plistPath string) error
	Bootout(ctx context.Context) error
	Kickstart(ctx context.Context) error
}

// Installer holds the boundaries used by the lifecycle commands.
type Installer struct {
	Layout      layout.Layout
	FS          safefs.Checker
	Store       state.Store
	Clock       func() time.Time
	Power       reconcile.PowerAdapter
	Wake        reconcile.WakeAdapter
	Launchd     LaunchdManager
	Log         *log.Logger
	ToolVersion string
	// SourceBinary is the executable copied into the installation directory.
	SourceBinary string
}

// ErrWakeConflict is returned when an unrelated recurring pmset schedule
// exists and --replace-wake-schedule was not supplied.
var ErrWakeConflict = errors.New("an unrelated recurring pmset schedule exists")

// ErrNotInstalled is returned by apply when no installation exists.
var ErrNotInstalled = errors.New("schlaflos is not installed")

// MaxBinarySize bounds the executable copied at install time.
const MaxBinarySize = 256 << 20

func (i *Installer) logf(format string, args ...any) {
	if i.Log != nil {
		i.Log.Printf(format, args...)
	}
}

func (i *Installer) now() time.Time { return i.Clock().UTC() }

// Install validates the configuration, records baselines, installs the
// artifacts, applies the guarded wake schedule, and loads the LaunchDaemon.
func (i *Installer) Install(ctx context.Context, cfgPath string, replaceWake bool) error {
	data, cfg, err := loadOperatorConfig(cfgPath)
	if err != nil {
		return err
	}

	// Observe before anything is written so a failure leaves nothing behind.
	observedSleep, err := i.Power.SleepDisabled(ctx)
	if err != nil {
		return err
	}
	observedWake, err := i.Wake.Current(ctx)
	if err != nil {
		return err
	}

	st, err := i.Store.LoadState()
	fresh := false
	switch {
	case err == nil:
		i.logf("existing installation found (install id %s); keeping recorded baselines", st.InstallID)
	case errors.Is(err, state.ErrNoState):
		fresh = true
	default:
		return fmt.Errorf("existing private state is unusable: %w (run `schlaflos emergency-off` and remove %s to recover)", err, i.Layout.StatePath)
	}

	if err := i.checkWakeConflict(cfg, st, observedWake, replaceWake); err != nil {
		return err
	}

	if err := i.prepareDirectories(); err != nil {
		return err
	}

	if fresh {
		st, err = state.New(i.now(), i.ToolVersion)
		if err != nil {
			return err
		}
		st.SleepBaseline = &state.SleepRecord{Disabled: observedSleep, At: i.now()}
		st.WakeBaseline = &state.WakeRecord{Schedule: observedWake, At: i.now()}
		i.logf("recorded baselines: disablesleep=%d, wake schedule %s", b2i(observedSleep), observedWake)
		if !observedWake.Exact() {
			i.logf("warning: the pre-install wake schedule days are not known exactly; uninstall will cancel it instead of restoring it")
		}
	} else {
		st.ToolVersion = i.ToolVersion
	}
	// The baseline must be durable before the first mutation.
	if err := i.Store.SaveState(st); err != nil {
		return fmt.Errorf("persist private state: %w", err)
	}

	if err := i.installBinary(); err != nil {
		return err
	}
	if err := i.FS.WriteFile(i.Layout.ConfigPath, data, layout.ModeConfig); err != nil {
		return fmt.Errorf("install configuration: %w", err)
	}
	i.logf("installed configuration to %s", i.Layout.ConfigPath)
	if _, err := i.writePlist(cfg); err != nil {
		return err
	}
	i.installConvenienceLink()

	if err := i.applyWake(ctx, cfg, st, observedWake); err != nil {
		return err
	}

	if err := i.Launchd.Bootout(ctx); err != nil {
		return err
	}
	if err := i.Launchd.Bootstrap(ctx, i.Layout.PlistPath); err != nil {
		return err
	}
	i.logf("loaded LaunchDaemon %s; the first reconciliation runs now", layout.Label)

	v := observedSleep
	ws := observedWake.String()
	status := &state.Status{
		GeneratedAt: i.now(), Mode: state.ModeInstall, Observed: &v, Reason: "installed",
		PollIntervalSeconds: int(cfg.PollInterval.Seconds()), WakeObserved: &ws, RunOK: true,
	}
	if cfg.Wake.Enabled {
		s := wake.FromConfig(cfg.Wake).String()
		status.WakeInstalled = &s
	}
	return i.Store.WriteStatus(status)
}

// Apply replaces the installed configuration of an existing installation and
// triggers an immediate reconciliation.
func (i *Installer) Apply(ctx context.Context, cfgPath string, replaceWake bool) error {
	data, cfg, err := loadOperatorConfig(cfgPath)
	if err != nil {
		return err
	}
	st, err := i.Store.LoadState()
	if err != nil {
		if errors.Is(err, state.ErrNoState) {
			return ErrNotInstalled
		}
		return err
	}
	if err := i.FS.VerifyFile(i.Layout.Binary, layout.ModeBinary); err != nil {
		return fmt.Errorf("installed binary: %w", err)
	}
	observedWake, err := i.Wake.Current(ctx)
	if err != nil {
		return err
	}
	if err := i.checkWakeConflict(cfg, st, observedWake, replaceWake); err != nil {
		return err
	}
	if err := i.FS.WriteFile(i.Layout.ConfigPath, data, layout.ModeConfig); err != nil {
		return fmt.Errorf("install configuration: %w", err)
	}
	i.logf("installed configuration to %s", i.Layout.ConfigPath)
	changed, err := i.writePlist(cfg)
	if err != nil {
		return err
	}
	if err := i.applyWake(ctx, cfg, st, observedWake); err != nil {
		return err
	}
	loaded, err := i.Launchd.Loaded(ctx)
	if err != nil {
		return err
	}
	if changed || !loaded {
		if err := i.Launchd.Bootout(ctx); err != nil {
			return err
		}
		if err := i.Launchd.Bootstrap(ctx, i.Layout.PlistPath); err != nil {
			return err
		}
		i.logf("reloaded LaunchDaemon; the first reconciliation runs now")
		return nil
	}
	if err := i.Launchd.Kickstart(ctx); err != nil {
		return err
	}
	i.logf("triggered an immediate reconciliation")
	return nil
}

// Uninstall unloads the service, conditionally restores baselines, and
// removes only the artifacts owned by schlaflos.
func (i *Installer) Uninstall(ctx context.Context) error {
	var errs []error
	if err := i.Launchd.Bootout(ctx); err != nil {
		errs = append(errs, err)
	} else {
		i.logf("unloaded LaunchDaemon")
	}

	st, err := i.Store.LoadState()
	switch {
	case err == nil:
		errs = append(errs, i.restoreBaselines(ctx, st)...)
	case errors.Is(err, state.ErrNoState):
		i.logf("warning: no private state found; power settings are left unchanged")
	default:
		i.logf("warning: private state is unusable (%v); power settings are left unchanged", err)
	}

	for _, p := range []string{i.Layout.PlistPath, i.Layout.ConfigPath, i.Layout.Binary, i.Layout.StatusPath, i.Layout.StatePath, i.Layout.LockPath} {
		if err := safefs.RemoveFile(p); err != nil {
			errs = append(errs, err)
		}
	}
	i.removeConvenienceLink()
	for _, d := range []string{i.Layout.BinDir, i.Layout.SupportDir, i.Layout.StateDir} {
		if err := safefs.RemoveEmptyDir(d); err != nil {
			i.logf("warning: %v", err)
		}
	}
	i.logf("removed schlaflos artifacts")
	return errors.Join(errs...)
}

// EmergencyOff forces disablesleep=0, releases the wake schedule when it is
// still owned, and unloads the LaunchDaemon. Configuration is kept.
func (i *Installer) EmergencyOff(ctx context.Context) error {
	var errs []error
	if err := i.Power.SetSleepDisabled(ctx, false); err != nil {
		errs = append(errs, err)
	} else {
		i.logf("forced disablesleep=0")
	}
	if err := i.Launchd.Bootout(ctx); err != nil {
		errs = append(errs, err)
	} else {
		i.logf("unloaded LaunchDaemon; configuration is kept")
	}

	st, err := i.Store.LoadState()
	if err != nil {
		i.logf("warning: private state is unavailable (%v); the wake schedule is left unchanged", err)
		return errors.Join(errs...)
	}
	now := i.now()
	st.LastSleepWritten = &state.SleepRecord{Disabled: false, At: now}
	st.PendingSleepWrite = nil
	st.EmergencyOffAt = &now
	if st.LastWakeWritten != nil {
		observed, err := i.Wake.Current(ctx)
		if err != nil {
			errs = append(errs, err)
		} else if observed.Equal(st.LastWakeWritten.Schedule) {
			target := st.WakeBaseline.Schedule
			if !target.Exact() {
				i.logf("warning: pre-install wake schedule days unknown; cancelling recurring events instead")
				target = wake.Schedule{}
			}
			if !observed.Equal(target) {
				if err := i.Wake.Apply(ctx, target); err != nil {
					errs = append(errs, err)
				} else {
					st.LastWakeWritten = &state.WakeRecord{Schedule: target, At: now}
					i.logf("restored wake schedule to %s", target)
				}
			}
		} else {
			i.logf("wake schedule %s is not the one schlaflos wrote (%s); leaving it unchanged", observed, st.LastWakeWritten.Schedule)
		}
	}
	if err := i.Store.SaveState(st); err != nil {
		errs = append(errs, err)
	}
	off := false
	status := &state.Status{GeneratedAt: now, Mode: state.ModeEmergencyOff, Observed: &off, Reason: "emergency_off", RunOK: len(errs) == 0}
	if err := i.Store.WriteStatus(status); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func loadOperatorConfig(path string) ([]byte, *config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	// Parse the exact bytes that will be installed.
	if _, err := config.Parse(data); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return data, cfg, nil
}

func (i *Installer) checkWakeConflict(cfg *config.Config, st *state.State, observed wake.Schedule, replace bool) error {
	if !cfg.Wake.Enabled || observed.Empty() {
		return nil
	}
	desired := wake.FromConfig(cfg.Wake)
	if observed.Equal(desired) {
		return nil
	}
	if st != nil && st.LastWakeWritten != nil && observed.Equal(st.LastWakeWritten.Schedule) {
		return nil
	}
	if replace {
		i.logf("replacing existing recurring pmset schedule %s (--replace-wake-schedule)", observed)
		return nil
	}
	return fmt.Errorf("%w: %s; re-run with --replace-wake-schedule to replace it", ErrWakeConflict, observed)
}

func (i *Installer) prepareDirectories() error {
	for _, d := range []string{filepath.Dir(i.Layout.SupportDir), i.Layout.LaunchDaemonsDir, filepath.Dir(i.Layout.StateDir), i.Layout.LogDir} {
		if err := i.FS.VerifyDir(d, 0); err != nil {
			return fmt.Errorf("unsafe privileged ancestor: %w", err)
		}
	}
	for _, d := range []string{i.Layout.SupportDir, i.Layout.BinDir, i.Layout.StateDir} {
		if err := i.FS.EnsureDir(d, layout.ModeDir); err != nil {
			return err
		}
	}
	return nil
}

func (i *Installer) installBinary() error {
	src := i.SourceBinary
	fi, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("source binary: %w", err)
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxBinarySize {
		return fmt.Errorf("source binary %s: not a regular file or too large", src)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("source binary: %w", err)
	}
	if err := i.FS.WriteFile(i.Layout.Binary, data, layout.ModeBinary); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}
	i.logf("installed binary to %s", i.Layout.Binary)
	return nil
}

// writePlist renders the plist and writes it when it differs from the
// installed one. It reports whether the file changed.
func (i *Installer) writePlist(cfg *config.Config) (bool, error) {
	rendered, err := launchd.RenderPlist(launchd.Params{
		Label:                layout.Label,
		ProgramArguments:     []string{i.Layout.Binary, "reconcile"},
		StartIntervalSeconds: int(cfg.PollInterval.Seconds()),
		LogPath:              i.Layout.LogPath,
	})
	if err != nil {
		return false, err
	}
	if err := i.FS.VerifyFile(i.Layout.PlistPath, layout.ModePlist); err == nil {
		if current, err := i.FS.ReadFile(i.Layout.PlistPath, safefs.MaxFileSize); err == nil && string(current) == string(rendered) {
			return false, nil
		}
	}
	if err := i.FS.WriteFile(i.Layout.PlistPath, rendered, layout.ModePlist); err != nil {
		return false, fmt.Errorf("install LaunchDaemon plist: %w", err)
	}
	i.logf("installed LaunchDaemon plist to %s", i.Layout.PlistPath)
	return true, nil
}

func (i *Installer) installConvenienceLink() {
	link := i.Layout.ConvenienceBinary
	if _, err := os.Lstat(filepath.Dir(link)); err != nil {
		return
	}
	fi, err := os.Lstat(link)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Symlink(i.Layout.Binary, link); err != nil {
			i.logf("warning: could not create %s: %v", link, err)
			return
		}
		i.logf("linked %s -> %s", link, i.Layout.Binary)
		return
	}
	if err != nil {
		return
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		if target, err := os.Readlink(link); err == nil && target == i.Layout.Binary {
			return
		}
	}
	i.logf("notice: %s exists and is not managed by schlaflos; leaving it unchanged", link)
}

func (i *Installer) removeConvenienceLink() {
	link := i.Layout.ConvenienceBinary
	fi, err := os.Lstat(link)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return
	}
	if target, err := os.Readlink(link); err == nil && target == i.Layout.Binary {
		os.Remove(link)
	}
}

func (i *Installer) applyWake(ctx context.Context, cfg *config.Config, st *state.State, observed wake.Schedule) error {
	var target *wake.Schedule
	switch {
	case cfg.Wake.Enabled:
		t := wake.FromConfig(cfg.Wake)
		target = &t
	case st.LastWakeWritten != nil && observed.Equal(st.LastWakeWritten.Schedule):
		t := st.WakeBaseline.Schedule
		if !t.Exact() {
			i.logf("warning: pre-install wake schedule days unknown; cancelling recurring events instead")
			t = wake.Schedule{}
		}
		target = &t
	}
	if target == nil || observed.Equal(*target) {
		return nil
	}
	now := i.now()
	st.PendingWakeWrite = &state.WakeRecord{Schedule: *target, At: now}
	if err := i.Store.SaveState(st); err != nil {
		return err
	}
	if err := i.Wake.Apply(ctx, *target); err != nil {
		return err
	}
	st.PendingWakeWrite = nil
	st.LastWakeWritten = &state.WakeRecord{Schedule: *target, At: now}
	i.logf("set wake schedule to %s (was %s)", target, observed)
	return i.Store.SaveState(st)
}

func (i *Installer) restoreBaselines(ctx context.Context, st *state.State) []error {
	var errs []error
	if st.LastSleepWritten != nil {
		observed, err := i.Power.SleepDisabled(ctx)
		switch {
		case err != nil:
			errs = append(errs, err)
		case observed != st.LastSleepWritten.Disabled:
			i.logf("conflict: disablesleep is %d but schlaflos last wrote %d; leaving it unchanged", b2i(observed), b2i(st.LastSleepWritten.Disabled))
		case observed != st.SleepBaseline.Disabled:
			if err := i.Power.SetSleepDisabled(ctx, st.SleepBaseline.Disabled); err != nil {
				errs = append(errs, err)
			} else {
				i.logf("restored disablesleep=%d", b2i(st.SleepBaseline.Disabled))
			}
		}
	}
	if st.LastWakeWritten != nil {
		observed, err := i.Wake.Current(ctx)
		switch {
		case err != nil:
			errs = append(errs, err)
		case !observed.Equal(st.LastWakeWritten.Schedule):
			i.logf("conflict: wake schedule is %s but schlaflos last wrote %s; leaving it unchanged", observed, st.LastWakeWritten.Schedule)
		default:
			target := st.WakeBaseline.Schedule
			if !target.Exact() {
				i.logf("warning: pre-install wake schedule days unknown; cancelling recurring events instead")
				target = wake.Schedule{}
			}
			if !observed.Equal(target) {
				if err := i.Wake.Apply(ctx, target); err != nil {
					errs = append(errs, err)
				} else {
					i.logf("restored wake schedule to %s", target)
				}
			}
		}
	}
	return errs
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
