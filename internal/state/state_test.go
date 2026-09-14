package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wake"
	"github.com/nozomemein/schlaflos/internal/safefs"
)

func testStore(t *testing.T) Store {
	t.Helper()
	root := t.TempDir()
	os.Chmod(root, 0o755)
	lay := layout.Rooted(root)
	if err := os.MkdirAll(lay.StateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return Store{Layout: lay, FS: safefs.Checker{UID: os.Getuid(), GID: os.Getgid(), Root: root}}
}

func sample(t *testing.T) *State {
	t.Helper()
	now := time.Date(2026, 9, 14, 9, 0, 0, 0, time.UTC)
	st, err := New(now, "test")
	if err != nil {
		t.Fatal(err)
	}
	st.SleepBaseline = &SleepRecord{Disabled: false, At: now}
	st.WakeBaseline = &WakeRecord{Schedule: wake.Schedule{On: &wake.Event{Type: wake.TypeWake, Minutes: 600, Days: wake.Weekdays, DaysExact: true}}, At: now}
	return st
}

func TestStateRoundTrip(t *testing.T) {
	s := testStore(t)
	if _, err := s.LoadState(); !errors.Is(err, ErrNoState) {
		t.Fatalf("LoadState on empty store = %v", err)
	}
	st := sample(t)
	st.LastError = &ErrorRecord{At: st.InstalledAt, Category: "mutation", Message: "pmset: exit status 1: secret detail"}
	if err := s.SaveState(st); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat(s.Layout.StatePath)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("state mode = %04o", fi.Mode().Perm())
	}
	got, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if got.InstallID != st.InstallID || !got.WakeBaseline.Schedule.Equal(st.WakeBaseline.Schedule) || got.LastError.Message != st.LastError.Message {
		t.Fatalf("round trip mismatch: %+v", got)
	}
	if string(Encode(got)) != string(Encode(st)) {
		t.Fatal("Encode differs after round trip")
	}
}

func TestCorruptAndUnsupportedState(t *testing.T) {
	s := testStore(t)
	os.WriteFile(s.Layout.StatePath, []byte("{not json"), 0o600)
	if _, err := s.LoadState(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt state err = %v", err)
	}
	st := sample(t)
	st.SchemaVersion = 99
	data, _ := json.Marshal(st)
	os.WriteFile(s.Layout.StatePath, data, 0o600)
	if _, err := s.LoadState(); err == nil || !strings.Contains(err.Error(), "schema version 99") {
		t.Fatalf("unsupported schema err = %v", err)
	}
	os.WriteFile(s.Layout.StatePath, []byte(`{"schema_version":1,"install_id":"x","unknown":true}`), 0o600)
	if _, err := s.LoadState(); err == nil {
		t.Fatal("unknown field accepted")
	}
	// Wrong mode is rejected.
	good := sample(t)
	data, _ = json.Marshal(good)
	os.WriteFile(s.Layout.StatePath, data, 0o644)
	os.Chmod(s.Layout.StatePath, 0o644)
	if _, err := s.LoadState(); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("world-readable state accepted: %v", err)
	}
	// Symlink is rejected.
	os.Remove(s.Layout.StatePath)
	real := filepath.Join(filepath.Dir(s.Layout.StatePath), "real.json")
	os.WriteFile(real, data, 0o600)
	os.Symlink(real, s.Layout.StatePath)
	if _, err := s.LoadState(); err == nil {
		t.Fatal("symlinked state accepted")
	}
}

func TestStatusRedaction(t *testing.T) {
	s := testStore(t)
	obs := true
	installed := "wakeorpoweron MTWRFSU 08:00:00"
	err := s.WriteStatus(&Status{
		GeneratedAt:   time.Now(),
		Mode:          ModeReconcile,
		Requested:     true,
		Observed:      &obs,
		Reason:        "scheduled_window",
		PowerSource:   "ac",
		ActiveGuards:  []string{"ci"},
		LastError:     PublicError(&ErrorRecord{At: time.Now(), Category: "mutation", Message: "secret detail"}),
		WakeInstalled: &installed,
	})
	if err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Lstat(s.Layout.StatusPath)
	if fi.Mode().Perm() != 0o644 {
		t.Fatalf("status mode = %04o", fi.Mode().Perm())
	}
	raw, _ := os.ReadFile(s.Layout.StatusPath)
	for _, forbidden := range []string{"secret detail", "baseline", "install_id", "message", "pending"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("status leaks %q:\n%s", forbidden, raw)
		}
	}
	got, err := s.ReadStatus()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastError == nil || got.LastError.Category != "mutation" || !got.Requested || *got.Observed != true || *got.WakeInstalled != installed {
		t.Fatalf("status = %+v", got)
	}
	if err := s.WriteStatus(&Status{}); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(s.Layout.StatusPath)
	if !strings.Contains(string(raw), `"active_guards": []`) {
		t.Fatalf("nil guards not normalised:\n%s", raw)
	}
}

func TestReadStatusRejectsUnsafeFile(t *testing.T) {
	s := testStore(t)
	os.WriteFile(s.Layout.StatusPath, []byte(`{"schema_version":1}`), 0o666)
	os.Chmod(s.Layout.StatusPath, 0o666)
	if _, err := s.ReadStatus(); err == nil {
		t.Fatal("world-writable status accepted")
	}
	os.Chmod(s.Layout.StatusPath, 0o644)
	if _, err := s.ReadStatus(); err != nil {
		t.Fatal(err)
	}
	os.Remove(s.Layout.StatusPath)
	if _, err := s.ReadStatus(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing status err = %v", err)
	}
}
