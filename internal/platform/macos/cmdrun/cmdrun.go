// Package cmdrun executes fixed macOS tools by absolute path with explicit
// argument arrays. It never invokes a shell and passes a minimal environment.
package cmdrun

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runner executes an executable at a fixed absolute path.
type Runner interface {
	// Run executes path with args and returns its standard output. A non-zero
	// exit status is returned as an *ExitError.
	Run(ctx context.Context, path string, args ...string) ([]byte, error)
}

// ExitError reports a non-zero exit status together with a bounded copy of
// standard error.
type ExitError struct {
	Path   string
	Args   []string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s %s: exit status %d", e.Path, strings.Join(e.Args, " "), e.Code)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// DefaultTimeout bounds every subprocess so a wedged tool cannot hold the
// reconciliation lock indefinitely.
const DefaultTimeout = 30 * time.Second

// Exec runs real subprocesses.
type Exec struct {
	Timeout time.Duration
}

// Run implements Runner.
func (e Exec) Run(ctx context.Context, path string, args ...string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("cmdrun: refusing non-absolute executable path %q", path)
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	cmd.Dir = "/"
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var xe *exec.ExitError
		if errors.As(err, &xe) {
			return stdout.Bytes(), &ExitError{Path: path, Args: args, Code: xe.ExitCode(), Stderr: truncate(stderr.String(), 512)}
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", path, err)
	}
	return stdout.Bytes(), nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// Call records one executed command.
type Call struct {
	Path string
	Args []string
}

// String renders the call as a single argv line for assertions.
func (c Call) String() string {
	return strings.Join(append([]string{c.Path}, c.Args...), " ")
}

// Response is a canned result for Fake.
type Response struct {
	Stdout string
	Err    error
}

// Fake is a Runner for tests. Responses are keyed by the argv line produced
// by Call.String. A Handler, when set, takes precedence and can implement
// stateful behaviour.
type Fake struct {
	Responses map[string]Response
	Handler   func(call Call) (Response, bool)
	Calls     []Call
}

// Run implements Runner.
func (f *Fake) Run(_ context.Context, path string, args ...string) ([]byte, error) {
	call := Call{Path: path, Args: append([]string(nil), args...)}
	f.Calls = append(f.Calls, call)
	if f.Handler != nil {
		if r, ok := f.Handler(call); ok {
			return []byte(r.Stdout), r.Err
		}
	}
	if r, ok := f.Responses[call.String()]; ok {
		return []byte(r.Stdout), r.Err
	}
	return nil, fmt.Errorf("cmdrun.Fake: unexpected command %q", call.String())
}

// CallLines returns every recorded call as an argv line.
func (f *Fake) CallLines() []string {
	out := make([]string, 0, len(f.Calls))
	for _, c := range f.Calls {
		out = append(out, c.String())
	}
	return out
}
