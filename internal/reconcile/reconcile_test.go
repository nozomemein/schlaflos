package reconcile

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/power"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wake"
	"github.com/nozomemein/schlaflos/internal/processguard"
	"github.com/nozomemein/schlaflos/internal/safefs"
	"github.com/nozomemein/schlaflos/internal/state"
)

type fakePower struct {
	source   power.Source
	srcErr   error
	disabled bool
	readErr  error
	setErr   error
	sets     []bool
}

func (f *fakePower) Source(context.Context) (power.Source, error) { return f.source, f.srcErr }
func (f *fakePower) SleepDisabled(context.Context) (bool, error) {
	return f.disabled, f.readErr
}
func (f *fakePower) SetSleepDisabled(_ context.Context, v bool) error {
	f.sets = append(f.sets, v)
	if f.setErr != nil {
		return f.setErr
	}
	f.disabled = v
	return nil
}

type fakeWake struct {
	current  wake.Schedule
	readErr  error
	applyErr error
	applied  []wake.Schedule
}

func (f *fakeWake) Current(context.Context) (wake.Schedule, error) { return f.current, f.readErr }
func (f *fakeWake) Apply(_ context.Context, s wake.Schedule) error {
	f.applied = append(f.applied, s)
	if f.applyErr != nil {
		return f.applyErr
	}
	f.current = s
	return nil
}

type fakeProcs struct {
	procs []processguard.Process
	err   error
}

func (f *fakeProcs) Snapshot(context.Context) ([]processguard.Process, error) {
	return f.procs, f.err
}

const cfgWindow = `version = 1
poll_interval = "30s"
[power]
require_ac = true
[[windows]]
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]
start = "08:00"
end = "20:00"
[[guards.process]]
name = "ci"
executable = "/opt/runner/bin/Runner.Worker"
`

const cfgWake = cfgWindow + `[wake]
enabled = true
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]
time = "08:00"
`

var (
	noon     = time.Date(2026, 9, 14, 12, 0, 0, 0, time.Local)
	midnight = time.Date(2026, 9, 14, 0, 30, 0, 0, time.Local)
	ourWake  = wake.Schedule{On: &wake.Event{Type: wake.TypeWakeOrPowerOn, Minutes: 480, Days: wake.EveryDay, DaysExact: true}}
)

type harness struct {
	deps  Deps
	power *fakePower
	wake  *fakeWake
	procs *fakeProcs
	store state.Store
	logs  *strings.Builder
}

func newHarness(t *testing.T, cfgTOML string, baselineDisabled bool, wakeBaseline wake.Schedule) *harness {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0o755)
	lay := layout.Rooted(root)
	fs := safefs.Checker{UID: os.Getuid(), GID: os.Getgid(), Root: root}
	for _, d := range []string{lay.SupportDir, lay.StateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if cfgTOML != "" {
		if err := fs.WriteFile(lay.ConfigPath, []byte(cfgTOML), layout.ModeConfig); err != nil {
			t.Fatal(err)
		}
	}
	store := state.Store{Layout: lay, FS: fs}
	st, err := state.New(noon, "test")
	if err != nil {
		t.Fatal(err)
	}
	st.SleepBaseline = &state.SleepRecord{Disabled: baselineDisabled, At: noon}
	st.WakeBaseline = &state.WakeRecord{Schedule: wakeBaseline, At: noon}
	if err := store.SaveState(st); err != nil {
		t.Fatal(err)
	}
	h := &harness{
		power: &fakePower{source: power.SourceAC, disabled: baselineDisabled},
		wake:  &fakeWake{current: wakeBaseline},
		procs: &fakeProcs{},
		store: store,
		logs:  &strings.Builder{},
	}
	h.deps = Deps{
		Layout: lay, FS: fs, Store: store,
		Clock: func() time.Time { return noon },
		Power: h.power, Procs: h.procs, Wake: h.wake,
		Log: log.New(h.logs, "", 0),
	}
	return h
}

func (h *harness) state(t *testing.T) *state.State {
	t.Helper()
	st, err := h.store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (h *harness) status(t *testing.T) *state.Status {
	t.Helper()
	st, err := h.store.ReadStatus()
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestInsideWindowEnablesAndOutsideRestoresBaseline(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, h.logs)
	}
	if len(h.power.sets) != 1 || !h.power.sets[0] {
		t.Fatalf("sets = %v", h.power.sets)
	}
	if !status.Requested || *status.Observed != true || status.Reason != "scheduled_window" || !status.RunOK {
		t.Fatalf("status = %+v", status)
	}
	st := h.state(t)
	if st.LastSleepWritten == nil || !st.LastSleepWritten.Disabled || st.PendingSleepWrite != nil {
		t.Fatalf("ledger = %+v", st)
	}
	if st.LastTransition == nil || st.LastTransition.From || !st.LastTransition.To || st.LastTransition.Reason != "scheduled_window" {
		t.Fatalf("transition = %+v", st.LastTransition)
	}
	if fi, _ := os.Lstat(h.deps.Layout.StatusPath); fi.Mode().Perm() != 0o644 {
		t.Fatal("status not world-readable")
	}
	if h.status(t).NextTransition == nil {
		t.Fatal("next transition missing")
	}

	// Second run at the same time is a no-op.
	if _, err := Run(context.Background(), h.deps); err != nil || len(h.power.sets) != 1 {
		t.Fatalf("second run: err=%v sets=%v", err, h.power.sets)
	}

	// After the window ends the baseline (0) is restored.
	h.deps.Clock = func() time.Time { return midnight }
	status, err = Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.power.sets) != 2 || h.power.sets[1] {
		t.Fatalf("sets = %v", h.power.sets)
	}
	if status.Requested || status.Reason != "outside_window" || *status.Observed {
		t.Fatalf("status = %+v", status)
	}
}

func TestBaselineOnePersistsOutsideWindow(t *testing.T) {
	h := newHarness(t, cfgWindow, true, wake.Schedule{})
	h.deps.Clock = func() time.Time { return midnight }
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.power.sets) != 0 {
		t.Fatalf("baseline 1 was overwritten: %v", h.power.sets)
	}
	if status.Requested || *status.Observed != true {
		t.Fatalf("status must distinguish request from effective setting: %+v", status)
	}
}

func TestBatteryPowerNeverInhibits(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	h.power.source = power.SourceBattery
	h.procs.procs = []processguard.Process{{PID: 1, Executable: "/opt/runner/bin/Runner.Worker"}}
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.power.sets) != 0 || status.Requested || status.Reason != "battery_power" {
		t.Fatalf("sets=%v status=%+v", h.power.sets, status)
	}
	if len(status.ActiveGuards) != 1 || status.ActiveGuards[0] != "ci" {
		t.Fatalf("active guards = %v", status.ActiveGuards)
	}
}

func TestGuardKeepsAwakeAfterWindow(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	h.deps.Clock = func() time.Time { return midnight }
	h.procs.procs = []processguard.Process{{PID: 1, Executable: "/opt/runner/bin/Runner.Worker"}}
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.power.sets) != 1 || !h.power.sets[0] || status.Reason != "active_process_guard" {
		t.Fatalf("sets=%v status=%+v", h.power.sets, status)
	}
}

func TestInvalidConfigFailsSafe(t *testing.T) {
	h := newHarness(t, "version = 1\nbogus = true\n", false, wake.Schedule{})
	h.power.disabled = true // schlaflos had previously enabled it
	status, err := Run(context.Background(), h.deps)
	if err == nil || !strings.Contains(err.Error(), "unknown configuration keys") {
		t.Fatalf("err = %v", err)
	}
	if len(h.power.sets) != 1 || h.power.sets[0] {
		t.Fatalf("baseline not restored: %v", h.power.sets)
	}
	if status.Reason != ReasonInvalidConfiguration || status.LastError == nil || status.LastError.Category != CategoryConfigInvalid || status.RunOK {
		t.Fatalf("status = %+v", status)
	}
	if h.state(t).LastError.Message == "" {
		t.Fatal("ledger must keep the error message")
	}
	if strings.Contains(string(mustRead(t, h.deps.Layout.StatusPath)), "bogus") {
		t.Fatal("status leaks the error message")
	}
}

func TestMissingConfigFailsSafe(t *testing.T) {
	h := newHarness(t, "", false, wake.Schedule{})
	if _, err := Run(context.Background(), h.deps); err == nil {
		t.Fatal("missing config accepted")
	}
	if len(h.power.sets) != 0 {
		t.Fatalf("sets = %v", h.power.sets)
	}
}

func TestPowerSourceReadFailureMakesNoMutation(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	h.power.srcErr = errors.New("pmset unavailable")
	status, err := Run(context.Background(), h.deps)
	if err == nil {
		t.Fatal("expected error")
	}
	if len(h.power.sets) != 0 || status.Reason != ReasonPowerSourceUnknown || status.PowerSource != "unknown" {
		t.Fatalf("sets=%v status=%+v", h.power.sets, status)
	}
	if status.LastError.Category != CategoryPowerRead {
		t.Fatalf("category = %s", status.LastError.Category)
	}
}

func TestSleepReadFailureMakesNoMutation(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	h.power.readErr = errors.New("pmset -g failed")
	status, err := Run(context.Background(), h.deps)
	if err == nil || len(h.power.sets) != 0 || status.Observed != nil {
		t.Fatalf("err=%v sets=%v status=%+v", err, h.power.sets, status)
	}
}

func TestProcessInspectionFailureIsDegraded(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	h.deps.Clock = func() time.Time { return midnight }
	h.procs.err = errors.New("ps failed")
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatalf("degraded run must not fail: %v", err)
	}
	if len(status.Degraded) != 1 || status.Degraded[0] != DegradedProcessInspection || len(h.power.sets) != 0 {
		t.Fatalf("status=%+v sets=%v", status, h.power.sets)
	}
}

func TestDriftIsReportedAndConverged(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	st := h.state(t)
	st.LastSleepWritten = &state.SleepRecord{Disabled: true, At: noon}
	h.store.SaveState(st)
	h.power.disabled = false // someone ran pmset -a disablesleep 0
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Conflicts) != 1 || status.Conflicts[0] != ConflictSleepDrift {
		t.Fatalf("conflicts = %v", status.Conflicts)
	}
	if len(h.power.sets) != 1 || !h.power.sets[0] {
		t.Fatalf("drift not converged: %v", h.power.sets)
	}
	// External change before the first write is drift against the baseline.
	h2 := newHarness(t, cfgWindow, false, wake.Schedule{})
	h2.power.disabled = true
	status, _ = Run(context.Background(), h2.deps)
	if len(status.Conflicts) != 1 || len(h2.power.sets) != 0 {
		t.Fatalf("conflicts=%v sets=%v", status.Conflicts, h2.power.sets)
	}
}

func TestPendingWriteResolution(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	h.power.setErr = errors.New("write failed")
	if _, err := Run(context.Background(), h.deps); err == nil {
		t.Fatal("expected mutation error")
	}
	st := h.state(t)
	if st.PendingSleepWrite == nil || !st.PendingSleepWrite.Disabled || st.LastSleepWritten != nil {
		t.Fatalf("pending not recorded: %+v", st)
	}
	if h.status(t).LastError.Category != CategorySleepMutation {
		t.Fatal("mutation error category missing")
	}
	// The write actually took effect after the process died: adopt it.
	h.power.setErr = nil
	h.power.disabled = true
	if _, err := Run(context.Background(), h.deps); err != nil {
		t.Fatal(err)
	}
	st = h.state(t)
	if st.PendingSleepWrite != nil || st.LastSleepWritten == nil || !st.LastSleepWritten.Disabled {
		t.Fatalf("pending not adopted: %+v", st)
	}
	if len(h.power.sets) != 1 {
		t.Fatalf("unexpected extra write: %v", h.power.sets)
	}
	if !strings.Contains(h.logs.String(), "adopted interrupted disablesleep=true") {
		t.Fatalf("logs:\n%s", h.logs)
	}

	// A pending write that never took effect is discarded.
	h3 := newHarness(t, cfgWindow, false, wake.Schedule{})
	st = h3.state(t)
	st.PendingSleepWrite = &state.SleepRecord{Disabled: true, At: noon}
	h3.store.SaveState(st)
	h3.deps.Clock = func() time.Time { return midnight }
	Run(context.Background(), h3.deps)
	st = h3.state(t)
	if st.PendingSleepWrite != nil || st.LastSleepWritten != nil {
		t.Fatalf("stale pending not discarded: %+v", st)
	}
}

func TestLockBusy(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	release, err := h.deps.FS.Lock(h.deps.Layout.LockPath, layout.ModeLock)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Run(context.Background(), h.deps)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v", err)
	}
	if len(h.power.sets) != 0 {
		t.Fatal("mutation while locked")
	}
	if _, err := os.Lstat(h.deps.Layout.StatusPath); err == nil {
		t.Fatal("status written while locked")
	}
}

func TestMissingStateMakesNoMutation(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	os.Remove(h.deps.Layout.StatePath)
	if _, err := Run(context.Background(), h.deps); !errors.Is(err, state.ErrNoState) {
		t.Fatalf("err = %v", err)
	}
	if len(h.power.sets) != 0 || len(h.wake.applied) != 0 {
		t.Fatal("mutation without private state")
	}
	os.WriteFile(h.deps.Layout.StatePath, []byte("corrupt"), 0o600)
	if _, err := Run(context.Background(), h.deps); err == nil || len(h.power.sets) != 0 {
		t.Fatalf("corrupt state: err=%v sets=%v", err, h.power.sets)
	}
}

func TestDryRunMutatesNothing(t *testing.T) {
	h := newHarness(t, cfgWake, false, wake.Schedule{})
	h.deps.DryRun = true
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Requested || status.Mode != state.ModeDryRun {
		t.Fatalf("status = %+v", status)
	}
	if len(h.power.sets) != 0 || len(h.wake.applied) != 0 {
		t.Fatal("dry run mutated")
	}
	if _, err := os.Lstat(h.deps.Layout.StatusPath); err == nil {
		t.Fatal("dry run wrote status")
	}
	if !strings.Contains(h.logs.String(), "would set disablesleep=1") || !strings.Contains(h.logs.String(), "would set wake schedule") {
		t.Fatalf("logs:\n%s", h.logs)
	}
}

func TestWakeScheduleLifecycle(t *testing.T) {
	external := wake.Schedule{Off: &wake.Event{Type: wake.TypeShutdown, Minutes: 22 * 60, Days: wake.Weekdays, DaysExact: true}}
	h := newHarness(t, cfgWake, false, external)
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.wake.applied) != 1 || !h.wake.applied[0].Equal(ourWake) {
		t.Fatalf("applied = %v", h.wake.applied)
	}
	if *status.WakeInstalled != "wakeorpoweron MTWRFSU 08:00:00" || *status.WakeObserved != "wakeorpoweron MTWRFSU 08:00:00" {
		t.Fatalf("status wake = %v / %v", *status.WakeInstalled, *status.WakeObserved)
	}
	st := h.state(t)
	if st.LastWakeWritten == nil || !st.LastWakeWritten.Schedule.Equal(ourWake) || st.PendingWakeWrite != nil {
		t.Fatalf("ledger = %+v", st)
	}

	// Idempotent.
	Run(context.Background(), h.deps)
	if len(h.wake.applied) != 1 {
		t.Fatalf("re-applied unchanged schedule: %v", h.wake.applied)
	}

	// External drift is reported and repaired.
	h.wake.current = wake.Schedule{On: &wake.Event{Type: wake.TypeWake, Minutes: 540, Days: wake.EveryDay, DaysExact: true}}
	status, _ = Run(context.Background(), h.deps)
	if len(status.Conflicts) != 1 || status.Conflicts[0] != ConflictWakeDrift || len(h.wake.applied) != 2 {
		t.Fatalf("conflicts=%v applied=%v", status.Conflicts, h.wake.applied)
	}

	// Disabling wake in the configuration hands the pre-install schedule back.
	h.deps.FS.WriteFile(h.deps.Layout.ConfigPath, []byte(cfgWindow), layout.ModeConfig)
	status, err = Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.wake.applied) != 3 || !h.wake.applied[2].Equal(external) || status.WakeInstalled != nil {
		t.Fatalf("baseline not restored: %v", h.wake.applied)
	}
	Run(context.Background(), h.deps)
	if len(h.wake.applied) != 3 {
		t.Fatal("baseline re-applied")
	}
}

func TestWakeDisabledNeverTouchesUnownedSchedule(t *testing.T) {
	external := wake.Schedule{On: &wake.Event{Type: wake.TypeWake, Minutes: 540, Days: wake.EveryDay, DaysExact: true}}
	h := newHarness(t, cfgWindow, false, external)
	if _, err := Run(context.Background(), h.deps); err != nil {
		t.Fatal(err)
	}
	if len(h.wake.applied) != 0 {
		t.Fatalf("touched an unowned schedule: %v", h.wake.applied)
	}
}

func TestWakeDisabledLeavesDriftedScheduleAlone(t *testing.T) {
	h := newHarness(t, cfgWindow, false, wake.Schedule{})
	st := h.state(t)
	st.LastWakeWritten = &state.WakeRecord{Schedule: ourWake, At: noon}
	h.store.SaveState(st)
	external := wake.Schedule{On: &wake.Event{Type: wake.TypeWake, Minutes: 540, Days: wake.EveryDay, DaysExact: true}}
	h.wake.current = external
	status, err := Run(context.Background(), h.deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.wake.applied) != 0 || len(status.Conflicts) != 1 || status.Conflicts[0] != ConflictWakeDrift {
		t.Fatalf("applied=%v conflicts=%v", h.wake.applied, status.Conflicts)
	}
}

func TestWakeMutationFailureKeepsIntent(t *testing.T) {
	h := newHarness(t, cfgWake, false, wake.Schedule{})
	h.wake.applyErr = errors.New("pmset repeat failed")
	if _, err := Run(context.Background(), h.deps); err == nil {
		t.Fatal("expected error")
	}
	st := h.state(t)
	if st.PendingWakeWrite == nil || st.LastWakeWritten != nil {
		t.Fatalf("ledger = %+v", st)
	}
	h.wake.applyErr = nil
	h.wake.current = ourWake
	if _, err := Run(context.Background(), h.deps); err != nil {
		t.Fatal(err)
	}
	st = h.state(t)
	if st.PendingWakeWrite != nil || st.LastWakeWritten == nil || len(h.wake.applied) != 1 {
		t.Fatalf("pending wake not adopted: %+v applied=%v", st, h.wake.applied)
	}
}

func TestWakeReadFailureIsRecorded(t *testing.T) {
	h := newHarness(t, cfgWake, false, wake.Schedule{})
	h.wake.readErr = errors.New("pmset -g sched failed")
	status, err := Run(context.Background(), h.deps)
	if err == nil || status.WakeObserved != nil || len(h.wake.applied) != 0 {
		t.Fatalf("err=%v status=%+v", err, status)
	}
	// The sleep setting is still managed.
	if len(h.power.sets) != 1 {
		t.Fatalf("sets = %v", h.power.sets)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
