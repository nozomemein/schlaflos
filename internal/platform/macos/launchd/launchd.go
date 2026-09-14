// Package launchd renders the LaunchDaemon plist and manages the service
// through /bin/launchctl with fixed arguments.
package launchd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"strings"
	"text/template"

	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
	packaging "github.com/nozomemein/schlaflos/packaging/launchd"
)

// Params are the values rendered into the plist.
type Params struct {
	Label                string
	ProgramArguments     []string
	StartIntervalSeconds int
	LogPath              string
}

var plistTmpl = template.Must(template.New("plist").Parse(packaging.PlistTemplate))

// RenderPlist renders the LaunchDaemon plist. Every value is XML-escaped.
func RenderPlist(p Params) ([]byte, error) {
	if p.Label == "" || len(p.ProgramArguments) == 0 || p.StartIntervalSeconds <= 0 || p.LogPath == "" {
		return nil, errors.New("launchd: incomplete plist parameters")
	}
	esc := Params{
		Label:                html.EscapeString(p.Label),
		StartIntervalSeconds: p.StartIntervalSeconds,
		LogPath:              html.EscapeString(p.LogPath),
	}
	for _, a := range p.ProgramArguments {
		esc.ProgramArguments = append(esc.ProgramArguments, html.EscapeString(a))
	}
	var buf bytes.Buffer
	if err := plistTmpl.Execute(&buf, esc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Manager controls the system-domain service.
type Manager struct {
	Runner cmdrun.Runner
	Label  string
}

func (m Manager) label() string {
	if m.Label == "" {
		return layout.Label
	}
	return m.Label
}

func (m Manager) target() string { return "system/" + m.label() }

// Loaded reports whether the service is loaded in the system domain. It
// requires root; launchd refuses to describe system services otherwise.
func (m Manager) Loaded(ctx context.Context) (bool, error) {
	_, err := m.Runner.Run(ctx, layout.LaunchctlPath, "print", m.target())
	if err == nil {
		return true, nil
	}
	var xe *cmdrun.ExitError
	if errors.As(err, &xe) && strings.Contains(xe.Stderr, "Could not find service") {
		return false, nil
	}
	return false, fmt.Errorf("launchd: query %s: %w", m.target(), err)
}

// Bootstrap loads the plist into the system domain. RunAtLoad then runs the
// reconciler immediately.
func (m Manager) Bootstrap(ctx context.Context, plistPath string) error {
	if _, err := m.Runner.Run(ctx, layout.LaunchctlPath, "enable", m.target()); err != nil {
		return fmt.Errorf("launchd: enable %s: %w", m.target(), err)
	}
	if _, err := m.Runner.Run(ctx, layout.LaunchctlPath, "bootstrap", "system", plistPath); err != nil {
		return fmt.Errorf("launchd: bootstrap %s: %w", plistPath, err)
	}
	return nil
}

// Bootout unloads the service. It is a no-op when the service is not loaded.
func (m Manager) Bootout(ctx context.Context) error {
	loaded, err := m.Loaded(ctx)
	if err != nil {
		return err
	}
	if !loaded {
		return nil
	}
	if _, err := m.Runner.Run(ctx, layout.LaunchctlPath, "bootout", m.target()); err != nil {
		return fmt.Errorf("launchd: bootout %s: %w", m.target(), err)
	}
	return nil
}

// Kickstart runs the loaded service now, restarting it if it is running.
func (m Manager) Kickstart(ctx context.Context) error {
	if _, err := m.Runner.Run(ctx, layout.LaunchctlPath, "kickstart", "-k", m.target()); err != nil {
		return fmt.Errorf("launchd: kickstart %s: %w", m.target(), err)
	}
	return nil
}
