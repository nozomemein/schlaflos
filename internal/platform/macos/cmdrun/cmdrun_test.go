package cmdrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecRefusesRelativePath(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), "echo", "hi")
	if err == nil || !strings.Contains(err.Error(), "non-absolute") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecCapturesStdout(t *testing.T) {
	out, err := Exec{}.Run(context.Background(), "/bin/echo", "hello", "world")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "hello world\n" {
		t.Fatalf("stdout = %q", out)
	}
}

func TestExecReportsExitStatusAndStderr(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), "/bin/ls", "/nonexistent-schlaflos-test-path")
	var xe *ExitError
	if !errors.As(err, &xe) {
		t.Fatalf("err = %T %v, want *ExitError", err, err)
	}
	if xe.Code == 0 || xe.Path != "/bin/ls" || len(xe.Args) != 1 {
		t.Fatalf("ExitError = %+v", xe)
	}
	if !strings.Contains(xe.Stderr, "No such file") {
		t.Fatalf("stderr not captured: %q", xe.Stderr)
	}
	if !strings.Contains(xe.Error(), "/bin/ls /nonexistent-schlaflos-test-path: exit status") {
		t.Fatalf("Error() = %q", xe.Error())
	}
}

func TestExecMissingExecutable(t *testing.T) {
	_, err := Exec{}.Run(context.Background(), "/nonexistent/schlaflos-tool")
	if err == nil {
		t.Fatal("missing executable accepted")
	}
	var xe *ExitError
	if errors.As(err, &xe) {
		t.Fatal("a missing executable must not be reported as an exit status")
	}
}

func TestExecTimeout(t *testing.T) {
	start := time.Now()
	_, err := Exec{Timeout: 200 * time.Millisecond}.Run(context.Background(), "/bin/sleep", "5")
	if err == nil {
		t.Fatal("timed-out command succeeded")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout not enforced: took %s", elapsed)
	}
}

func TestExecHonoursCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (Exec{}).Run(ctx, "/bin/echo", "hi"); err == nil {
		t.Fatal("cancelled context ignored")
	}
}

func TestExecPassesMinimalEnvironment(t *testing.T) {
	t.Setenv("SCHLAFLOS_LEAK_TEST", "leaked")
	out, err := Exec{}.Run(context.Background(), "/usr/bin/env")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 1 || lines[0] != "PATH=/usr/bin:/bin:/usr/sbin:/sbin" {
		t.Fatalf("environment = %q, want only the fixed PATH", lines)
	}
}

func TestExitErrorStderrIsTruncated(t *testing.T) {
	long := strings.Repeat("x", 600)
	if got := truncate(long, 512); len(got) != 515 || !strings.HasSuffix(got, "...") {
		t.Fatalf("truncate = %d bytes", len(got))
	}
	if got := truncate("  short  ", 512); got != "short" {
		t.Fatalf("truncate = %q", got)
	}
}

func TestFakeRecordsCallsAndMatchesArgv(t *testing.T) {
	f := &Fake{Responses: map[string]Response{
		"/usr/bin/pmset -g batt":           {Stdout: "Now drawing from 'AC Power'\n"},
		"/usr/bin/pmset -a disablesleep 1": {Err: &ExitError{Code: 1, Stderr: "denied"}},
	}}
	out, err := f.Run(context.Background(), "/usr/bin/pmset", "-g", "batt")
	if err != nil || !strings.HasPrefix(string(out), "Now drawing") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if _, err := f.Run(context.Background(), "/usr/bin/pmset", "-a", "disablesleep", "1"); err == nil {
		t.Fatal("canned error not returned")
	}
	if _, err := f.Run(context.Background(), "/usr/bin/pmset", "-a", "disablesleep", "2"); err == nil || !strings.Contains(err.Error(), "unexpected command") {
		t.Fatalf("unexpected command accepted: %v", err)
	}
	want := []string{
		"/usr/bin/pmset -g batt",
		"/usr/bin/pmset -a disablesleep 1",
		"/usr/bin/pmset -a disablesleep 2",
	}
	if strings.Join(f.CallLines(), "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v", f.CallLines())
	}
	// Recorded args are copies, not aliases of the caller's slice.
	args := []string{"-g"}
	f.Run(context.Background(), "/usr/bin/pmset", args...)
	args[0] = "mutated"
	if f.Calls[len(f.Calls)-1].Args[0] != "-g" {
		t.Fatal("recorded args alias the caller's slice")
	}
}

func TestFakeHandlerTakesPrecedence(t *testing.T) {
	f := &Fake{
		Responses: map[string]Response{"/bin/x": {Stdout: "from responses"}},
		Handler: func(c Call) (Response, bool) {
			if c.Path == "/bin/x" {
				return Response{Stdout: "from handler"}, true
			}
			return Response{}, false
		},
	}
	out, _ := f.Run(context.Background(), "/bin/x")
	if string(out) != "from handler" {
		t.Fatalf("out = %q", out)
	}
	// A handler that declines falls back to Responses.
	f.Responses["/bin/y"] = Response{Stdout: "y"}
	if out, err := f.Run(context.Background(), "/bin/y"); err != nil || string(out) != "y" {
		t.Fatalf("fallback: out=%q err=%v", out, err)
	}
}
