package processguard

import (
	"context"
	"errors"
	"testing"

	"github.com/nozomemein/schlaflos/internal/config"
	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
)

const captured = `    1 /sbin/launchd
  399 /usr/libexec/wifivelocityd
  524 /System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/FSEvents.framework/Versions/A/Support/fseventsd
 8123 /Users/example/actions-runner/bin/Runner.Listener
 8130 /Users/example/actions-runner/bin/Runner.Worker
 8131 /bin/bash
`

func TestParsePS(t *testing.T) {
	procs, err := ParsePS([]byte(captured))
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 6 {
		t.Fatalf("got %d processes", len(procs))
	}
	if procs[2].PID != 524 || procs[2].Executable != "/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/FSEvents.framework/Versions/A/Support/fseventsd" {
		t.Fatalf("long path parsed wrongly: %+v", procs[2])
	}
	if _, err := ParsePS([]byte("abc /bin/x\n")); err == nil {
		t.Fatal("malformed pid accepted")
	}
}

func TestMatch(t *testing.T) {
	procs, _ := ParsePS([]byte(captured))
	guards := []config.ProcessGuard{
		{Name: "shell", Executable: "/bin/bash"},
		{Name: "ci", Executable: "/Users/example/actions-runner/bin/Runner.Worker"},
		{Name: "absent", Executable: "/usr/local/bin/nothing"},
		{Name: "substring", Executable: "/Users/example/actions-runner/bin/Runner"},
	}
	got := Match(guards, procs)
	if len(got) != 2 || got[0] != "ci" || got[1] != "shell" {
		t.Fatalf("Match = %v", got)
	}
}

func TestPSInspectorUsesFixedArgv(t *testing.T) {
	fake := &cmdrun.Fake{Responses: map[string]cmdrun.Response{
		"/bin/ps -axo pid=,comm=": {Stdout: captured},
	}}
	procs, err := PS{Runner: fake}.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 6 {
		t.Fatalf("got %d processes", len(procs))
	}
	fake = &cmdrun.Fake{Responses: map[string]cmdrun.Response{
		"/bin/ps -axo pid=,comm=": {Err: errors.New("boom")},
	}}
	if _, err := (PS{Runner: fake}).Snapshot(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
