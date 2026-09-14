// Package processguard takes a snapshot of running processes and matches
// configured guards by canonical executable path. Process arguments are never
// captured; the snapshot holds only the PID and executable path.
package processguard

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nozomemein/schlaflos/internal/config"
	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
)

// Process is one running process.
type Process struct {
	PID        int
	Executable string
}

// Inspector produces a process snapshot.
type Inspector interface {
	Snapshot(ctx context.Context) ([]Process, error)
}

// PS inspects processes with `/bin/ps -axo pid=,comm=`. On macOS the comm
// column is the executable path passed to execve, which is the canonical
// form matched against configured guards.
type PS struct {
	Runner cmdrun.Runner
}

// Snapshot implements Inspector.
func (p PS) Snapshot(ctx context.Context) ([]Process, error) {
	out, err := p.Runner.Run(ctx, layout.PsPath, "-axo", "pid=,comm=")
	if err != nil {
		return nil, fmt.Errorf("process inspection: %w", err)
	}
	return ParsePS(out)
}

// ParsePS parses `ps -axo pid=,comm=` output.
func ParsePS(out []byte) ([]Process, error) {
	var procs []Process
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		pidStr, exe, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			return nil, fmt.Errorf("process inspection: malformed line %q", line)
		}
		procs = append(procs, Process{PID: pid, Executable: strings.TrimSpace(exe)})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("process inspection: %w", err)
	}
	return procs, nil
}

// Match returns the sorted names of the guards whose executable path equals
// the executable of at least one running process.
func Match(guards []config.ProcessGuard, procs []Process) []string {
	running := make(map[string]bool, len(procs))
	for _, p := range procs {
		if p.Executable != "" {
			running[filepath.Clean(p.Executable)] = true
		}
	}
	var names []string
	for _, g := range guards {
		if running[filepath.Clean(g.Executable)] {
			names = append(names, g.Name)
		}
	}
	sort.Strings(names)
	return names
}
