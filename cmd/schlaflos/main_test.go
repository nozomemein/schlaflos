package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
	"github.com/nozomemein/schlaflos/internal/safefs"
	"github.com/nozomemein/schlaflos/internal/state"
)

func testApp(t *testing.T) (*app, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0o755)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	a := &app{
		stdout: stdout, stderr: stderr,
		layout:  layout.Rooted(root),
		fs:      safefs.Checker{UID: os.Getuid(), GID: os.Getgid(), Root: root},
		runner:  &cmdrun.Fake{},
		isRoot:  false,
		clock:   func() time.Time { return time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC) },
		version: "test",
	}
	return a, stdout, stderr
}

func TestUsageAndUnknownCommand(t *testing.T) {
	a, _, stderr := testApp(t)
	if code := a.dispatch(nil); code != 2 || !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	stderr.Reset()
	if code := a.dispatch([]string{"bogus"}); code != 2 || !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestConfigCheckAndInit(t *testing.T) {
	a, stdout, stderr := testApp(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "schlaflos.toml")
	if code := a.dispatch([]string{"init", path}); code != 0 {
		t.Fatalf("init: %d %s", code, stderr)
	}
	data, _ := os.ReadFile(path)
	if string(data) != config.Example {
		t.Fatal("init did not write the example")
	}
	if code := a.dispatch([]string{"init", path}); code != 1 {
		t.Fatal("init overwrote an existing file")
	}
	stdout.Reset()
	if code := a.dispatch([]string{"config", "check", path}); code != 0 || !strings.Contains(stdout.String(), "valid") {
		t.Fatalf("config check: %d %s %s", code, stdout, stderr)
	}
	os.WriteFile(path, []byte("version = 1\nnope = 1\n"), 0o644)
	stderr.Reset()
	if code := a.dispatch([]string{"config", "check", path}); code != 1 || !strings.Contains(stderr.String(), "unknown configuration keys") {
		t.Fatalf("config check invalid: %d %s", code, stderr)
	}
}

func TestPrivilegedCommandsRequireRoot(t *testing.T) {
	a, _, stderr := testApp(t)
	for _, args := range [][]string{{"install", "--config", "x"}, {"config", "apply", "x"}, {"uninstall"}, {"emergency-off"}, {"reconcile"}} {
		stderr.Reset()
		if code := a.dispatch(args); code != 1 || !strings.Contains(stderr.String(), "requires root") {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
		}
	}
	if fake := a.runner.(*cmdrun.Fake); len(fake.Calls) != 0 {
		t.Fatalf("commands executed without root: %v", fake.CallLines())
	}
}

func TestStatusOutput(t *testing.T) {
	a, stdout, stderr := testApp(t)
	if code := a.dispatch([]string{"status"}); code != 1 || !strings.Contains(stderr.String(), "not installed") {
		t.Fatalf("status without file: %d %s", code, stderr)
	}
	os.MkdirAll(a.layout.StateDir, 0o755)
	obs := true
	next := a.clock().Add(time.Hour)
	installed := "wakeorpoweron MTWRFSU 08:00:00"
	err := a.store().WriteStatus(&state.Status{
		GeneratedAt: a.clock(), Mode: state.ModeReconcile, Requested: true, Observed: &obs, Reason: "scheduled_window",
		PowerSource: "ac", InsideWindow: true, ActiveGuards: []string{"ci"}, NextTransition: &next,
		WakeInstalled: &installed, WakeObserved: &installed, RunOK: true,
		LastTransition: &state.Transition{At: a.clock(), From: false, To: true, Reason: "scheduled_window"},
	})
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := a.dispatch([]string{"status"}); code != 0 {
		t.Fatalf("status: %d %s", code, stderr)
	}
	out := stdout.String()
	for _, want := range []string{"inhibit sleep (scheduled_window)", "disablesleep=1", "active guards:    ci", "wake installed:   wakeorpoweron MTWRFSU 08:00:00", "disablesleep 0 -> 1", "last run:         ok"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
	stdout.Reset()
	if code := a.dispatch([]string{"status", "--json"}); code != 0 || !strings.Contains(stdout.String(), `"reason": "scheduled_window"`) {
		t.Fatalf("status --json: %d %s", code, stdout)
	}
}
