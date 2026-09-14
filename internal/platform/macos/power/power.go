// Package power reads the power source and manages the system-wide
// disablesleep setting through /usr/bin/pmset with fixed arguments.
package power

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
)

// Source is the current power source.
type Source string

const (
	SourceAC      Source = "ac"
	SourceBattery Source = "battery"
	SourceUPS     Source = "ups"
	SourceUnknown Source = "unknown"
)

// AC reports whether the source counts as AC power for the policy. UPS power
// is treated conservatively as not AC.
func (s Source) AC() bool { return s == SourceAC }

// Adapter talks to pmset.
type Adapter struct {
	Runner cmdrun.Runner
}

// Source runs `pmset -g batt` and reports the power source.
func (a Adapter) Source(ctx context.Context) (Source, error) {
	out, err := a.Runner.Run(ctx, layout.PmsetPath, "-g", "batt")
	if err != nil {
		return SourceUnknown, fmt.Errorf("read power source: %w", err)
	}
	return ParseBatt(out)
}

// ParseBatt parses `pmset -g batt` output. The first line reads
// "Now drawing from 'AC Power'" or "Now drawing from 'Battery Power'".
func ParseBatt(out []byte) (Source, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "Now drawing from") {
			continue
		}
		switch {
		case strings.Contains(line, "'AC Power'"):
			return SourceAC, nil
		case strings.Contains(line, "'Battery Power'"):
			return SourceBattery, nil
		case strings.Contains(line, "'UPS Power'"):
			return SourceUPS, nil
		default:
			return SourceUnknown, fmt.Errorf("read power source: unrecognised line %q", line)
		}
	}
	return SourceUnknown, fmt.Errorf("read power source: no \"Now drawing from\" line in pmset output")
}

// SleepDisabled runs `pmset -g` and reports the system-wide SleepDisabled
// setting. pmset prints the key only when it has been set; absence means 0.
func (a Adapter) SleepDisabled(ctx context.Context) (bool, error) {
	out, err := a.Runner.Run(ctx, layout.PmsetPath, "-g")
	if err != nil {
		return false, fmt.Errorf("read disablesleep: %w", err)
	}
	return ParseSleepDisabled(out)
}

// ParseSleepDisabled parses `pmset -g` output for the " SleepDisabled\t\t1"
// line printed under "System-wide power settings:".
func ParseSleepDisabled(out []byte) (bool, error) {
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "SleepDisabled" {
			switch fields[1] {
			case "1":
				return true, nil
			case "0":
				return false, nil
			default:
				return false, fmt.Errorf("read disablesleep: unexpected value %q", fields[1])
			}
		}
	}
	return false, nil
}

// SetSleepDisabled runs `pmset -a disablesleep N` and verifies the observed
// setting afterwards.
func (a Adapter) SetSleepDisabled(ctx context.Context, disabled bool) error {
	value := "0"
	if disabled {
		value = "1"
	}
	if _, err := a.Runner.Run(ctx, layout.PmsetPath, "-a", "disablesleep", value); err != nil {
		return fmt.Errorf("write disablesleep=%s: %w", value, err)
	}
	observed, err := a.SleepDisabled(ctx)
	if err != nil {
		return fmt.Errorf("verify disablesleep=%s: %w", value, err)
	}
	if observed != disabled {
		return fmt.Errorf("verify disablesleep=%s: pmset still reports %v", value, observed)
	}
	return nil
}
