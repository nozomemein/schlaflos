package install

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/power"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wake"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wakeevents"
	"github.com/nozomemein/schlaflos/internal/safefs"
	"github.com/nozomemein/schlaflos/internal/state"
)

type fakePower struct {
	disabled bool
	sets     []bool
}

func (f *fakePower) Source(context.Context) (power.Source, error) { return power.SourceAC, nil }
func (f *fakePower) SleepDisabled(context.Context) (bool, error)  { return f.disabled, nil }
func (f *fakePower) SetSleepDisabled(_ context.Context, v bool) error {
	f.sets = append(f.sets, v)
	f.disabled = v
	return nil
}

type fakeWake struct {
	current wake.Schedule
	applied []wake.Schedule
}

func (f *fakeWake) Current(context.Context) (wake.Schedule, error) { return f.current, nil }
func (f *fakeWake) Apply(_ context.Context, s wake.Schedule) error {
	f.applied = append(f.applied, s)
	f.current = s
	return nil
}

type fakeLaunchd struct {
	loaded bool
	calls  []string
}

func (f *fakeLaunchd) Loaded(context.Context) (bool, error) { return f.loaded, nil }
func (f *fakeLaunchd) Bootstrap(_ context.Context, p string) error {
	f.calls = append(f.calls, "bootstrap "+filepath.Base(p))
	f.loaded = true
	return nil
}
func (f *fakeLaunchd) Bootout(context.Context) error {
	if f.loaded {
		f.calls = append(f.calls, "bootout")
	}
	f.loaded = false
	return nil
}
func (f *fakeLaunchd) Kickstart(context.Context) error {
	f.calls = append(f.calls, "kickstart")
	return nil
}

const cfgTOML = `version = 1
poll_interval = "30s"
[[windows]]
days = ["mon"]
start = "08:00"
end = "20:00"
[wake]
enabled = true
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]
time = "08:00"
`

const cfgNoWake = `version = 1
poll_interval = "45s"
[[windows]]
days = ["mon"]
start = "08:00"
end = "20:00"
`

var ourWake = wake.Schedule{On: &wake.Event{Type: wake.TypeWakeOrPowerOn, Minutes: 480, Days: wake.EveryDay, DaysExact: true}}

type env struct {
	inst    *Installer
	root    string
	lay     layout.Layout
	power   *fakePower
	wake    *fakeWake
	launchd *fakeLaunchd
	events  *wakeevents.Fake
	logs    *strings.Builder
	cfgPath string
}

func newEnv(t *testing.T, cfg string) *env {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0o755)
	lay := layout.Rooted(root)
	// Ancestors that exist on a real system.
	for _, d := range []string{filepath.Dir(lay.SupportDir), lay.LaunchDaemonsDir, filepath.Dir(lay.StateDir), lay.LogDir, filepath.Dir(lay.ConvenienceBinary)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(root, "schlaflos-src")
	os.WriteFile(src, []byte("#!/bin/sh\necho fake binary\n"), 0o755)
	cfgPath := filepath.Join(root, "schlaflos.toml")
	os.WriteFile(cfgPath, []byte(cfg), 0o644)
	fs := safefs.Checker{UID: os.Getuid(), GID: os.Getgid(), Root: root}
	e := &env{root: root, lay: lay, power: &fakePower{}, wake: &fakeWake{}, launchd: &fakeLaunchd{}, events: &wakeevents.Fake{}, logs: &strings.Builder{}, cfgPath: cfgPath}
	e.inst = &Installer{
		Layout: lay, FS: fs, Store: state.Store{Layout: lay, FS: fs},
		Clock: func() time.Time { return time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC) },
		Power: e.power, Wake: e.wake, Events: e.events, Launchd: e.launchd,
		Log: log.New(e.logs, "", 0), ToolVersion: "test", SourceBinary: src,
	}
	return e
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func TestFreshInstall(t *testing.T) {
	e := newEnv(t, cfgTOML)
	e.power.disabled = true // pre-existing disablesleep=1
	if err := e.inst.Install(context.Background(), e.cfgPath, false); err != nil {
		t.Fatalf("Install: %v\n%s", err, e.logs)
	}
	for path, want := range map[string]os.FileMode{
		e.lay.SupportDir: 0o755, e.lay.BinDir: 0o755, e.lay.StateDir: 0o755,
		e.lay.Binary: 0o755, e.lay.ConfigPath: 0o600, e.lay.PlistPath: 0o600,
		e.lay.StatePath: 0o600, e.lay.StatusPath: 0o644,
	} {
		if got := mode(t, path); got != want {
			t.Fatalf("%s mode = %04o, want %04o", path, got, want)
		}
	}
	if data, _ := os.ReadFile(e.lay.Binary); !strings.Contains(string(data), "fake binary") {
		t.Fatal("binary not copied")
	}
	if data, _ := os.ReadFile(e.lay.ConfigPath); string(data) != cfgTOML {
		t.Fatal("configuration not installed verbatim")
	}
	plist, _ := os.ReadFile(e.lay.PlistPath)
	if !strings.Contains(string(plist), "<string>"+e.lay.Binary+"</string>") || !strings.Contains(string(plist), "<integer>30</integer>") {
		t.Fatalf("plist:\n%s", plist)
	}
	if target, err := os.Readlink(e.lay.ConvenienceBinary); err != nil || target != e.lay.Binary {
		t.Fatalf("convenience link = %q, %v", target, err)
	}
	st, err := e.inst.Store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if !st.SleepBaseline.Disabled || !st.WakeBaseline.Schedule.Empty() || st.LastSleepWritten != nil {
		t.Fatalf("baselines = %+v", st)
	}
	if st.LastWakeWritten == nil || !st.LastWakeWritten.Schedule.Equal(ourWake) || len(e.wake.applied) != 1 {
		t.Fatalf("wake not applied: %+v %v", st.LastWakeWritten, e.wake.applied)
	}
	if strings.Join(e.launchd.calls, ",") != "bootstrap "+layout.Label+".plist" {
		t.Fatalf("launchd calls = %v", e.launchd.calls)
	}
	if len(e.power.sets) != 0 {
		t.Fatal("install must not change disablesleep; the reconciler does")
	}
	status, err := e.inst.Store.ReadStatus()
	if err != nil || status.Mode != state.ModeInstall || *status.WakeInstalled != ourWake.String() {
		t.Fatalf("status = %+v, %v", status, err)
	}

	// Re-install (upgrade) keeps the baselines and the install id.
	e.wake.applied = nil
	if err := e.inst.Install(context.Background(), e.cfgPath, false); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	st2, _ := e.inst.Store.LoadState()
	if st2.InstallID != st.InstallID || !st2.SleepBaseline.Disabled || len(e.wake.applied) != 0 {
		t.Fatalf("upgrade changed ownership: %+v applied=%v", st2, e.wake.applied)
	}
	if strings.Join(e.launchd.calls, ",") != "bootstrap "+layout.Label+".plist,bootout,bootstrap "+layout.Label+".plist" {
		t.Fatalf("launchd calls = %v", e.launchd.calls)
	}
}

func TestInstallRefusesUnrelatedWakeSchedule(t *testing.T) {
	e := newEnv(t, cfgTOML)
	external := wake.Schedule{On: &wake.Event{Type: wake.TypeWake, Minutes: 540, Days: wake.Weekdays, DaysExact: true}}
	e.wake.current = external
	err := e.inst.Install(context.Background(), e.cfgPath, false)
	if !errors.Is(err, ErrWakeConflict) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Lstat(e.lay.SupportDir); err == nil {
		t.Fatal("refused install left artifacts behind")
	}
	if len(e.wake.applied) != 0 || len(e.launchd.calls) != 0 {
		t.Fatal("refused install mutated the system")
	}

	if err := e.inst.Install(context.Background(), e.cfgPath, true); err != nil {
		t.Fatalf("Install with --replace-wake-schedule: %v", err)
	}
	st, _ := e.inst.Store.LoadState()
	if !st.WakeBaseline.Schedule.Equal(external) || !e.wake.current.Equal(ourWake) {
		t.Fatalf("baseline/current = %s / %s", st.WakeBaseline.Schedule, e.wake.current)
	}

	// Uninstall hands the external schedule back.
	if err := e.inst.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !e.wake.current.Equal(external) {
		t.Fatalf("uninstall did not restore %s, current %s", external, e.wake.current)
	}
}

func TestInstallFailsOnUnsafeAncestor(t *testing.T) {
	e := newEnv(t, cfgTOML)
	os.Chmod(e.lay.LaunchDaemonsDir, 0o777)
	err := e.inst.Install(context.Background(), e.cfgPath, false)
	if err == nil || !strings.Contains(err.Error(), "unsafe privileged ancestor") {
		t.Fatalf("err = %v", err)
	}
	if got := mode(t, e.lay.LaunchDaemonsDir); got != 0o777 {
		t.Fatal("installer repaired an unsafe ancestor")
	}
	if _, err := os.Lstat(e.lay.SupportDir); err == nil {
		t.Fatal("artifacts created despite unsafe ancestor")
	}
}

func TestInstallRejectsInvalidConfig(t *testing.T) {
	e := newEnv(t, "version = 1\nnope = 1\n")
	if err := e.inst.Install(context.Background(), e.cfgPath, false); err == nil {
		t.Fatal("invalid config installed")
	}
	if _, err := os.Lstat(e.lay.SupportDir); err == nil {
		t.Fatal("artifacts created for invalid config")
	}
}

func TestUninstallRestoresOnlyOwnedValues(t *testing.T) {
	e := newEnv(t, cfgTOML)
	if err := e.inst.Install(context.Background(), e.cfgPath, false); err != nil {
		t.Fatal(err)
	}
	// Simulate the reconciler having written disablesleep=1.
	st, _ := e.inst.Store.LoadState()
	st.LastSleepWritten = &state.SleepRecord{Disabled: true, At: time.Now()}
	e.inst.Store.SaveState(st)
	e.power.disabled = true
	if err := e.inst.Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v\n%s", err, e.logs)
	}
	if len(e.power.sets) != 1 || e.power.sets[0] {
		t.Fatalf("baseline not restored: %v", e.power.sets)
	}
	if !e.wake.current.Empty() {
		t.Fatalf("wake schedule not cancelled: %s", e.wake.current)
	}
	for _, p := range []string{e.lay.PlistPath, e.lay.ConfigPath, e.lay.Binary, e.lay.StatePath, e.lay.StatusPath, e.lay.SupportDir, e.lay.StateDir, e.lay.ConvenienceBinary} {
		if _, err := os.Lstat(p); err == nil {
			t.Fatalf("%s still exists after uninstall", p)
		}
	}
	if strings.Join(e.launchd.calls, ",") != "bootstrap "+layout.Label+".plist,bootout" {
		t.Fatalf("launchd calls = %v", e.launchd.calls)
	}

	// Drifted values are preserved and reported.
	e2 := newEnv(t, cfgTOML)
	e2.inst.Install(context.Background(), e2.cfgPath, false)
	st, _ = e2.inst.Store.LoadState()
	st.LastSleepWritten = &state.SleepRecord{Disabled: true, At: time.Now()}
	e2.inst.Store.SaveState(st)
	e2.power.disabled = false                                                                                                 // someone turned it off
	e2.wake.current = wake.Schedule{On: &wake.Event{Type: wake.TypePowerOn, Minutes: 60, Days: wake.Sunday, DaysExact: true}} // someone replaced it
	if err := e2.inst.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e2.power.sets) != 0 || len(e2.wake.applied) != 1 {
		t.Fatalf("drifted values overwritten: sets=%v applied=%v", e2.power.sets, e2.wake.applied)
	}
	if !strings.Contains(e2.logs.String(), "conflict: disablesleep") || !strings.Contains(e2.logs.String(), "conflict: wake schedule") {
		t.Fatalf("conflicts not reported:\n%s", e2.logs)
	}
}

func TestUninstallWithoutStateLeavesPowerAlone(t *testing.T) {
	e := newEnv(t, cfgTOML)
	e.inst.Install(context.Background(), e.cfgPath, false)
	os.Remove(e.lay.StatePath)
	e.power.disabled = true
	if err := e.inst.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.power.sets) != 0 || !e.wake.current.Equal(ourWake) {
		t.Fatal("mutated without private state")
	}
	if _, err := os.Lstat(e.lay.PlistPath); err == nil {
		t.Fatal("artifacts not removed")
	}
}

func TestEmergencyOff(t *testing.T) {
	e := newEnv(t, cfgTOML)
	e.inst.Install(context.Background(), e.cfgPath, false)
	e.power.disabled = true
	if err := e.inst.EmergencyOff(context.Background()); err != nil {
		t.Fatalf("EmergencyOff: %v\n%s", err, e.logs)
	}
	if len(e.power.sets) != 1 || e.power.sets[0] {
		t.Fatalf("sets = %v", e.power.sets)
	}
	if e.launchd.loaded {
		t.Fatal("service still loaded")
	}
	if !e.wake.current.Empty() {
		t.Fatalf("owned wake schedule not released: %s", e.wake.current)
	}
	if _, err := os.Lstat(e.lay.ConfigPath); err != nil {
		t.Fatal("configuration removed by emergency-off")
	}
	st, _ := e.inst.Store.LoadState()
	if st.EmergencyOffAt == nil || st.LastSleepWritten == nil || st.LastSleepWritten.Disabled {
		t.Fatalf("ledger = %+v", st)
	}
	status, _ := e.inst.Store.ReadStatus()
	if status.Mode != state.ModeEmergencyOff || *status.Observed {
		t.Fatalf("status = %+v", status)
	}
}

func TestEmergencyOffWithoutState(t *testing.T) {
	e := newEnv(t, cfgTOML)
	os.MkdirAll(e.lay.StateDir, 0o755)
	if err := e.inst.EmergencyOff(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.power.sets) != 1 || e.power.sets[0] {
		t.Fatalf("sets = %v", e.power.sets)
	}
}

func TestApply(t *testing.T) {
	e := newEnv(t, cfgTOML)
	if err := e.inst.Apply(context.Background(), e.cfgPath, false); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("apply before install: %v", err)
	}
	if err := e.inst.Install(context.Background(), e.cfgPath, false); err != nil {
		t.Fatal(err)
	}
	// Same config: kickstart only.
	if err := e.inst.Apply(context.Background(), e.cfgPath, false); err != nil {
		t.Fatal(err)
	}
	if e.launchd.calls[len(e.launchd.calls)-1] != "kickstart" {
		t.Fatalf("launchd calls = %v", e.launchd.calls)
	}
	// Changed poll interval and wake disabled: plist rewritten, service
	// reloaded, wake baseline (none) restored.
	os.WriteFile(e.cfgPath, []byte(cfgNoWake), 0o644)
	if err := e.inst.Apply(context.Background(), e.cfgPath, false); err != nil {
		t.Fatalf("Apply: %v\n%s", err, e.logs)
	}
	plist, _ := os.ReadFile(e.lay.PlistPath)
	if !strings.Contains(string(plist), "<integer>45</integer>") {
		t.Fatal("plist not updated")
	}
	if !e.wake.current.Empty() {
		t.Fatalf("wake not released: %s", e.wake.current)
	}
	calls := strings.Join(e.launchd.calls, ",")
	if !strings.HasSuffix(calls, "bootout,bootstrap "+layout.Label+".plist") {
		t.Fatalf("launchd calls = %v", e.launchd.calls)
	}
	if data, _ := os.ReadFile(e.lay.ConfigPath); string(data) != cfgNoWake {
		t.Fatal("configuration not replaced")
	}
}

func TestUninstallAndEmergencyOffCancelOwnedEventsOnly(t *testing.T) {
	foreign := wakeevents.Event{Time: time.Now().Add(time.Hour), Owner: "com.apple.alarm", Type: "wake"}
	ours := wakeevents.Event{Time: time.Now().Add(2 * time.Hour), Owner: wakeevents.Owner, Type: "wakepoweron"}
	e := newEnv(t, cfgTOML)
	e.inst.Install(context.Background(), e.cfgPath, false)
	e.events.Events = []wakeevents.Event{foreign, ours}
	if err := e.inst.EmergencyOff(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.events.Events) != 1 || e.events.Events[0].Owner != "com.apple.alarm" {
		t.Fatalf("events after emergency-off = %+v", e.events.Events)
	}
	e.events.Events = []wakeevents.Event{foreign, ours}
	if err := e.inst.Uninstall(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.events.Events) != 1 || e.events.Events[0].Owner != "com.apple.alarm" {
		t.Fatalf("events after uninstall = %+v", e.events.Events)
	}
}
