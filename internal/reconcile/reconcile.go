// Package reconcile orchestrates one short-lived reconciliation:
//
//  1. acquire the root-owned lock;
//  2. load and validate the private ledger and the installed configuration;
//  3. observe time, power source, processes, sleep setting, wake schedule;
//  4. evaluate policy without side effects;
//  5. apply the minimum transition, recording intent before each mutation;
//  6. persist the ledger when changed and the redacted status;
//  7. release the lock.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/power"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wake"
	"github.com/nozomemein/schlaflos/internal/policy"
	"github.com/nozomemein/schlaflos/internal/processguard"
	"github.com/nozomemein/schlaflos/internal/safefs"
	"github.com/nozomemein/schlaflos/internal/state"
)

// PowerAdapter is the sleep-setting and power-source boundary.
type PowerAdapter interface {
	Source(ctx context.Context) (power.Source, error)
	SleepDisabled(ctx context.Context) (bool, error)
	SetSleepDisabled(ctx context.Context, disabled bool) error
}

// WakeAdapter is the recurring wake schedule boundary.
type WakeAdapter interface {
	Current(ctx context.Context) (wake.Schedule, error)
	Apply(ctx context.Context, s wake.Schedule) error
}

// Deps are the operating-system boundaries of one reconciliation.
type Deps struct {
	Layout layout.Layout
	FS     safefs.Checker
	Store  state.Store
	Clock  func() time.Time
	Power  PowerAdapter
	Procs  processguard.Inspector
	Wake   WakeAdapter
	Log    *log.Logger
	DryRun bool
}

// Error categories recorded in the ledger and projected to status.
const (
	CategoryConfigInvalid = "config_invalid"
	CategoryPowerRead     = "power_read"
	CategorySleepMutation = "sleep_mutation"
	CategoryWakeRead      = "wake_read"
	CategoryWakeMutation  = "wake_mutation"
	CategoryStatePersist  = "state_persist"
)

// Degraded and conflict codes projected to status.
const (
	DegradedProcessInspection = "process_inspection_failed"
	ConflictSleepDrift        = "sleep_setting_drift"
	ConflictWakeDrift         = "wake_schedule_drift"
)

// Reasons used outside the policy package.
const (
	ReasonInvalidConfiguration = "invalid_configuration"
	ReasonPowerSourceUnknown   = "power_source_unknown"
)

// ErrLocked is returned when another reconciliation holds the lock. The
// caller exits without changing anything.
var ErrLocked = errors.New("reconcile: another reconciliation holds the lock")

// LoadInstalledConfig reads the root-owned installed configuration through
// the safe filesystem layer and validates it strictly.
func LoadInstalledConfig(fs safefs.Checker, lay layout.Layout) (*config.Config, error) {
	if err := fs.VerifyFile(lay.ConfigPath, layout.ModeConfig); err != nil {
		return nil, err
	}
	data, err := fs.ReadFile(lay.ConfigPath, config.MaxSize)
	if err != nil {
		return nil, err
	}
	return config.Parse(data)
}

// Run performs one reconciliation and returns the status projection it
// produced. In dry-run mode nothing is mutated or persisted.
func Run(ctx context.Context, d Deps) (*state.Status, error) {
	if d.Log == nil {
		d.Log = log.New(nopWriter{}, "", 0)
	}
	release, err := d.FS.Lock(d.Layout.LockPath, layout.ModeLock)
	if err != nil {
		if errors.Is(err, safefs.ErrLocked) {
			d.Log.Printf("another reconciliation holds %s; exiting without changes", d.Layout.LockPath)
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("reconcile: lock: %w", err)
	}
	defer release()

	st, err := d.Store.LoadState()
	if err != nil {
		d.Log.Printf("private state unusable; no mutation performed: %v", err)
		return nil, fmt.Errorf("reconcile: %w", err)
	}
	r := &run{Deps: d, st: st, now: d.Clock(), before: state.Encode(st)}
	return r.execute(ctx)
}

type run struct {
	Deps
	st       *state.State
	now      time.Time
	before   []byte
	status   *state.Status
	firstErr error
}

func (r *run) execute(ctx context.Context) (*state.Status, error) {
	mode := state.ModeReconcile
	if r.DryRun {
		mode = state.ModeDryRun
	}
	r.status = &state.Status{GeneratedAt: r.now.UTC(), Mode: mode}

	cfg, cfgErr := LoadInstalledConfig(r.FS, r.Layout)

	observedSleep, sleepErr := r.Power.SleepDisabled(ctx)
	source, srcErr := r.Power.Source(ctx)
	observedWake, wakeErr := r.Wake.Current(ctx)
	procs, procErr := r.Procs.Snapshot(ctx)

	if sleepErr == nil {
		r.resolvePendingSleep(observedSleep)
		v := observedSleep
		r.status.Observed = &v
	}
	if wakeErr == nil {
		r.resolvePendingWake(observedWake)
		s := observedWake.String()
		r.status.WakeObserved = &s
	}
	r.status.PowerSource = string(source)
	if srcErr != nil {
		r.status.PowerSource = string(power.SourceUnknown)
	}

	if cfgErr != nil {
		r.status.Reason = ReasonInvalidConfiguration
		r.record(CategoryConfigInvalid, fmt.Errorf("installed configuration is invalid: %w", cfgErr))
		// Fail safe: drop the schlaflos request and return to the baseline.
		r.applySleep(ctx, r.st.SleepBaseline.Disabled, observedSleep, sleepErr, ReasonInvalidConfiguration)
	} else {
		r.status.PollIntervalSeconds = int(cfg.PollInterval.Seconds())
		var active []string
		if procErr != nil {
			r.status.Degraded = append(r.status.Degraded, DegradedProcessInspection)
			r.Log.Printf("degraded: %v; process guards are treated as inactive", procErr)
		} else {
			active = processguard.Match(cfg.Guards.Process, procs)
		}
		decision := policy.Evaluate(policy.Input{
			Now:          r.now,
			RequireAC:    cfg.Power.RequireAC,
			OnAC:         srcErr == nil && source.AC(),
			Windows:      cfg.Windows,
			ActiveGuards: active,
		})
		r.status.Requested = decision.PreventSleep
		r.status.Reason = string(decision.Reason)
		r.status.InsideWindow = decision.InsideWindow
		r.status.ActiveGuards = decision.ActiveGuards
		if decision.HasNextTransition {
			t := decision.NextTransition
			r.status.NextTransition = &t
		}
		if cfg.Wake.Enabled {
			s := wake.FromConfig(cfg.Wake).String()
			r.status.WakeInstalled = &s
		}

		if srcErr != nil {
			r.status.Reason = ReasonPowerSourceUnknown
			r.status.Requested = false
			r.record(CategoryPowerRead, srcErr)
		} else {
			desired := decision.PreventSleep || r.st.SleepBaseline.Disabled
			r.applySleep(ctx, desired, observedSleep, sleepErr, string(decision.Reason))
		}
		r.applyWake(ctx, cfg, observedWake, wakeErr)
	}

	r.status.LastTransition = r.st.LastTransition
	r.status.LastError = state.PublicError(r.st.LastError)
	r.status.RunOK = r.firstErr == nil

	if !r.DryRun {
		if string(state.Encode(r.st)) != string(r.before) {
			if err := r.Store.SaveState(r.st); err != nil {
				r.fail(fmt.Errorf("persist private state: %w", err))
			}
		}
		if err := r.Store.WriteStatus(r.status); err != nil {
			r.fail(fmt.Errorf("write status: %w", err))
		}
	}
	return r.status, r.firstErr
}

// resolvePendingSleep settles an interrupted sleep write: when the observed
// value equals the pending intent the write is adopted as owned, otherwise
// the intent is discarded.
func (r *run) resolvePendingSleep(observed bool) {
	p := r.st.PendingSleepWrite
	if p == nil {
		return
	}
	if observed == p.Disabled {
		r.st.LastSleepWritten = &state.SleepRecord{Disabled: p.Disabled, At: p.At}
		r.Log.Printf("adopted interrupted disablesleep=%v write as owned", p.Disabled)
	} else {
		r.Log.Printf("discarded interrupted disablesleep=%v write; observed %v", p.Disabled, observed)
	}
	r.st.PendingSleepWrite = nil
}

func (r *run) resolvePendingWake(observed wake.Schedule) {
	p := r.st.PendingWakeWrite
	if p == nil {
		return
	}
	if observed.Equal(p.Schedule) {
		r.st.LastWakeWritten = &state.WakeRecord{Schedule: p.Schedule, At: p.At}
		r.Log.Printf("adopted interrupted wake schedule write %s as owned", p.Schedule)
	} else {
		r.Log.Printf("discarded interrupted wake schedule write %s; observed %s", p.Schedule, observed)
	}
	r.st.PendingWakeWrite = nil
}

func (r *run) applySleep(ctx context.Context, desired, observed bool, observeErr error, reason string) {
	if observeErr != nil {
		r.record(CategoryPowerRead, observeErr)
		return
	}
	expected := r.st.SleepBaseline.Disabled
	if r.st.LastSleepWritten != nil {
		expected = r.st.LastSleepWritten.Disabled
	}
	if observed != expected {
		r.conflict(ConflictSleepDrift)
		r.Log.Printf("conflict: disablesleep is %v but schlaflos expected %v; converging to policy", b2i(observed), b2i(expected))
	}
	if observed == desired {
		return
	}
	if r.DryRun {
		r.Log.Printf("dry-run: would set disablesleep=%d (%s)", b2i(desired), reason)
		return
	}
	r.st.PendingSleepWrite = &state.SleepRecord{Disabled: desired, At: r.now.UTC()}
	if err := r.Store.SaveState(r.st); err != nil {
		r.st.PendingSleepWrite = nil
		r.record(CategoryStatePersist, fmt.Errorf("record intent before disablesleep write: %w", err))
		return
	}
	if err := r.Power.SetSleepDisabled(ctx, desired); err != nil {
		// The intent stays recorded; the next run resolves it against the
		// observed value.
		r.record(CategorySleepMutation, err)
		return
	}
	r.st.PendingSleepWrite = nil
	r.st.LastSleepWritten = &state.SleepRecord{Disabled: desired, At: r.now.UTC()}
	r.st.LastTransition = &state.Transition{At: r.now.UTC(), From: observed, To: desired, Reason: reason}
	v := desired
	r.status.Observed = &v
	r.Log.Printf("set disablesleep=%d (%s)", b2i(desired), reason)
	if err := r.Store.SaveState(r.st); err != nil {
		r.record(CategoryStatePersist, fmt.Errorf("record disablesleep write: %w", err))
	}
}

func (r *run) applyWake(ctx context.Context, cfg *config.Config, observed wake.Schedule, observeErr error) {
	if observeErr != nil {
		r.record(CategoryWakeRead, observeErr)
		return
	}
	owned := r.st.LastWakeWritten != nil
	if owned && !observed.Equal(r.st.LastWakeWritten.Schedule) {
		r.conflict(ConflictWakeDrift)
		r.Log.Printf("conflict: wake schedule is %s but schlaflos last wrote %s", observed, r.st.LastWakeWritten.Schedule)
	}
	var target *wake.Schedule
	switch {
	case cfg.Wake.Enabled:
		t := wake.FromConfig(cfg.Wake)
		target = &t
	case owned && observed.Equal(r.st.LastWakeWritten.Schedule):
		// Wake was disabled in the configuration and the schedule is still
		// ours: hand the pre-install schedule back.
		t := r.st.WakeBaseline.Schedule
		target = &t
	}
	if target == nil || observed.Equal(*target) {
		return
	}
	if !target.Exact() {
		r.record(CategoryWakeMutation, fmt.Errorf("cannot restore pre-install wake schedule %s: its days are not known exactly", target))
		return
	}
	if r.DryRun {
		r.Log.Printf("dry-run: would set wake schedule to %s (currently %s)", target, observed)
		return
	}
	r.st.PendingWakeWrite = &state.WakeRecord{Schedule: *target, At: r.now.UTC()}
	if err := r.Store.SaveState(r.st); err != nil {
		r.st.PendingWakeWrite = nil
		r.record(CategoryStatePersist, fmt.Errorf("record intent before wake schedule write: %w", err))
		return
	}
	if err := r.Wake.Apply(ctx, *target); err != nil {
		r.record(CategoryWakeMutation, err)
		return
	}
	r.st.PendingWakeWrite = nil
	r.st.LastWakeWritten = &state.WakeRecord{Schedule: *target, At: r.now.UTC()}
	s := target.String()
	r.status.WakeObserved = &s
	r.Log.Printf("set wake schedule to %s (was %s)", target, observed)
	if err := r.Store.SaveState(r.st); err != nil {
		r.record(CategoryStatePersist, fmt.Errorf("record wake schedule write: %w", err))
	}
}

func (r *run) record(category string, err error) {
	r.st.LastError = &state.ErrorRecord{At: r.now.UTC(), Category: category, Message: err.Error()}
	r.Log.Printf("error (%s): %v", category, err)
	r.fail(err)
}

func (r *run) fail(err error) {
	if r.firstErr == nil {
		r.firstErr = err
	}
}

func (r *run) conflict(code string) {
	for _, c := range r.status.Conflicts {
		if c == code {
			return
		}
	}
	r.status.Conflicts = append(r.status.Conflicts, code)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }
