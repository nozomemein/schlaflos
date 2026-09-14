package doctor

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/power"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wake"
	"github.com/nozomemein/schlaflos/internal/safefs"
	"github.com/nozomemein/schlaflos/internal/state"
)

type fakePower struct{ disabled bool }

func (f fakePower) Source(context.Context) (power.Source, error) { return power.SourceAC, nil }
func (f fakePower) SleepDisabled(context.Context) (bool, error)  { return f.disabled, nil }
func (f fakePower) SetSleepDisabled(context.Context, bool) error { return nil }

type fakeWake struct{ s wake.Schedule }

func (f fakeWake) Current(context.Context) (wake.Schedule, error) { return f.s, nil }
func (f fakeWake) Apply(context.Context, wake.Schedule) error     { return nil }

type fakeLaunchd struct{ loaded bool }

func (f fakeLaunchd) Loaded(context.Context) (bool, error) { return f.loaded, nil }

func newDoctor(t *testing.T) (Doctor, layout.Layout) {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0o755)
	lay := layout.Rooted(root)
	fs := safefs.Checker{UID: os.Getuid(), GID: os.Getgid(), Root: root}
	return Doctor{
		Layout: lay, FS: fs, Store: state.Store{Layout: lay, FS: fs},
		Power: fakePower{}, Wake: fakeWake{}, Launchd: fakeLaunchd{loaded: true}, IsRoot: true,
		Clock: func() time.Time { return time.Date(2026, 9, 14, 9, 5, 0, 0, time.UTC) },
	}, lay
}

func levels(checks []Check) map[string]Level {
	out := map[string]Level{}
	for _, c := range checks {
		out[c.Name] = c.Level
	}
	return out
}

func TestNotInstalled(t *testing.T) {
	d, _ := newDoctor(t)
	checks, failed := d.Run(context.Background())
	if failed || len(checks) != 1 || checks[0].Level != OK {
		t.Fatalf("checks = %+v failed=%v", checks, failed)
	}
}

func TestHealthyInstallation(t *testing.T) {
	d, lay := newDoctor(t)
	for _, dir := range []string{lay.BinDir, lay.StateDir, lay.LaunchDaemonsDir} {
		os.MkdirAll(dir, 0o755)
	}
	fs := d.FS
	fs.WriteFile(lay.Binary, []byte("bin"), layout.ModeBinary)
	fs.WriteFile(lay.ConfigPath, []byte("version = 1\n"), layout.ModeConfig)
	fs.WriteFile(lay.PlistPath, []byte("<plist/>"), layout.ModePlist)
	st, _ := state.New(d.Clock(), "test")
	st.SleepBaseline = &state.SleepRecord{At: d.Clock()}
	st.WakeBaseline = &state.WakeRecord{At: d.Clock()}
	d.Store.SaveState(st)
	obs := false
	d.Store.WriteStatus(&state.Status{GeneratedAt: d.Clock().Add(-20 * time.Second), Mode: state.ModeReconcile, PollIntervalSeconds: 30, Observed: &obs, Reason: "outside_window", RunOK: true})

	checks, failed := d.Run(context.Background())
	if failed {
		t.Fatalf("healthy installation failed:\n%s", render(checks))
	}
	for _, c := range checks {
		if c.Level != OK {
			t.Fatalf("unexpected %s: %s: %s", c.Level, c.Name, c.Detail)
		}
	}

	// Unprivileged: root-only checks are skipped, nothing fails.
	d.IsRoot = false
	checks, failed = d.Run(context.Background())
	lv := levels(checks)
	if failed || lv["configuration"] != Skip || lv["launchd"] != Skip || lv["private state"] != Skip {
		t.Fatalf("unprivileged run:\n%s", render(checks))
	}
	d.IsRoot = true

	// Wrong mode on the configuration and a stale status are reported.
	os.Chmod(lay.ConfigPath, 0o644)
	d.Store.WriteStatus(&state.Status{GeneratedAt: d.Clock().Add(-time.Hour), Mode: state.ModeReconcile, PollIntervalSeconds: 30, Observed: &obs, RunOK: false, LastError: &state.StatusError{At: d.Clock(), Category: "sleep_mutation"}, Conflicts: []string{"sleep_setting_drift"}})
	checks, failed = d.Run(context.Background())
	lv = levels(checks)
	if !failed || lv["path "+lay.ConfigPath] != Fail || lv["status"] != Warn || lv["last run"] != Warn || lv["conflict"] != Warn {
		t.Fatalf("degraded run:\n%s", render(checks))
	}

	// External disablesleep change and unloaded service.
	os.Chmod(lay.ConfigPath, 0o600)
	d.Power = fakePower{disabled: true}
	d.Launchd = fakeLaunchd{loaded: false}
	st.LastSleepWritten = &state.SleepRecord{Disabled: false, At: d.Clock()}
	d.Store.SaveState(st)
	checks, failed = d.Run(context.Background())
	lv = levels(checks)
	if !failed || lv["launchd"] != Fail || lv["disablesleep"] != Warn || lv["ownership"] != Warn {
		t.Fatalf("drift run:\n%s", render(checks))
	}
}

func render(checks []Check) string {
	var b strings.Builder
	for _, c := range checks {
		b.WriteString(string(c.Level) + " " + c.Name + ": " + c.Detail + "\n")
	}
	return b.String()
}

func TestEmergencyOffIsWarnNotFail(t *testing.T) {
	d, lay := newDoctor(t)
	for _, dir := range []string{lay.BinDir, lay.StateDir, lay.LaunchDaemonsDir} {
		os.MkdirAll(dir, 0o755)
	}
	d.FS.WriteFile(lay.Binary, []byte("bin"), layout.ModeBinary)
	d.FS.WriteFile(lay.ConfigPath, []byte("version = 1\n"), layout.ModeConfig)
	d.FS.WriteFile(lay.PlistPath, []byte("<plist/>"), layout.ModePlist)
	st, _ := state.New(d.Clock(), "test")
	st.SleepBaseline = &state.SleepRecord{At: d.Clock()}
	st.WakeBaseline = &state.WakeRecord{At: d.Clock()}
	d.Store.SaveState(st)
	off := false
	d.Store.WriteStatus(&state.Status{GeneratedAt: d.Clock(), Mode: state.ModeEmergencyOff, Observed: &off, Reason: "emergency_off", RunOK: true})
	d.Launchd = fakeLaunchd{loaded: false}

	checks, failed := d.Run(context.Background())
	lv := levels(checks)
	if failed || lv["launchd"] != Warn || lv["status"] != Warn {
		t.Fatalf("emergency-off must warn, not fail:\n%s", render(checks))
	}
	// Unprivileged users see the status warning and no failure either.
	d.IsRoot = false
	if _, failed := d.Run(context.Background()); failed {
		t.Fatal("unprivileged doctor failed during emergency-off")
	}
}
