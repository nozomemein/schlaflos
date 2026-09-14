// Package config defines the versioned TOML configuration, decodes it
// strictly, and validates every field before any privileged use.
//
// The schema deliberately cannot express commands, scripts, hooks, environment
// expansion, or secrets. A process guard executable path is only ever compared
// against running processes; it is never executed.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Example is the reference configuration shipped with the repository. It is
// written by `schlaflos init`.
//
//go:embed example.toml
var Example string

// SupportedVersion is the only configuration schema version accepted by this
// build.
const SupportedVersion = 1

// Poll interval bounds. The lower bound keeps launchd from spawning root
// processes continuously; the upper bound keeps the "finish the running job"
// behaviour responsive.
const (
	DefaultPollInterval = 30 * time.Second
	MinPollInterval     = 5 * time.Second
	MaxPollInterval     = time.Hour
)

// MaxSize bounds the configuration file size.
const MaxSize = 256 * 1024

// Wake actions accepted by pmset repeat for the "on" event.
const (
	WakeActionWakeOrPowerOn = "wakeorpoweron"
	WakeActionWake          = "wake"
	WakeActionPowerOn       = "poweron"
)

// Config is the validated configuration.
type Config struct {
	Version      int
	PollInterval time.Duration
	Power        Power
	Windows      []Window
	Wake         Wake
	Guards       Guards
}

// Power holds the power-source policy.
type Power struct {
	// RequireAC, when true, forbids inhibiting sleep on battery power.
	RequireAC bool
}

// Window is a recurring local-time window. Start is inclusive, End is
// exclusive, and a window whose End is not after Start crosses midnight.
// Days name the day on which the window starts.
type Window struct {
	Days  []time.Weekday
	Start Clock
	End   Clock
}

// CrossesMidnight reports whether the window ends on the following day.
func (w Window) CrossesMidnight() bool {
	return w.End.Minutes() <= w.Start.Minutes()
}

// HasDay reports whether the window starts on d.
func (w Window) HasDay(d time.Weekday) bool {
	for _, x := range w.Days {
		if x == d {
			return true
		}
	}
	return false
}

// Wake describes the single recurring pmset wake event.
type Wake struct {
	Enabled bool
	Days    []time.Weekday
	Time    Clock
	Action  string
}

// Guards groups the workload guards.
type Guards struct {
	Process []ProcessGuard
}

// ProcessGuard keeps the machine awake while a process with exactly this
// executable path is running.
type ProcessGuard struct {
	Name       string
	Executable string
}

// Clock is a wall-clock time of day.
type Clock struct {
	Hour   int
	Minute int
}

// Minutes returns the minutes since midnight.
func (c Clock) Minutes() int { return c.Hour*60 + c.Minute }

// String renders the clock as HH:MM.
func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

// ParseClock parses HH:MM in 24-hour notation.
func ParseClock(s string) (Clock, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return Clock{}, fmt.Errorf("time %q must use HH:MM", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return Clock{}, fmt.Errorf("time %q must use HH:MM between 00:00 and 23:59", s)
	}
	return Clock{Hour: h, Minute: m}, nil
}

var dayNames = map[string]time.Weekday{
	"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday,
	"fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
}

// DayName returns the configuration spelling of a weekday.
func DayName(d time.Weekday) string {
	return strings.ToLower(d.String()[:3])
}

// ParseDays parses a non-empty list of unique lowercase day abbreviations and
// returns them ordered Monday first.
func ParseDays(names []string) ([]time.Weekday, error) {
	if len(names) == 0 {
		return nil, errors.New("days must not be empty")
	}
	seen := map[time.Weekday]bool{}
	var out []time.Weekday
	for _, n := range names {
		d, ok := dayNames[n]
		if !ok {
			return nil, fmt.Errorf("unknown day %q (use mon, tue, wed, thu, fri, sat, sun)", n)
		}
		if seen[d] {
			return nil, fmt.Errorf("day %q is listed twice", n)
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return mondayFirst(out[i]) < mondayFirst(out[j]) })
	return out, nil
}

func mondayFirst(d time.Weekday) int {
	return (int(d) + 6) % 7
}

var guardNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// raw mirrors the TOML document. Every value is decoded into the simplest
// type so that validation produces precise messages.
type raw struct {
	Version      *int        `toml:"version"`
	PollInterval *string     `toml:"poll_interval"`
	Power        *rawPower   `toml:"power"`
	Windows      []rawWindow `toml:"windows"`
	Wake         *rawWake    `toml:"wake"`
	Guards       *rawGuards  `toml:"guards"`
}

type rawPower struct {
	RequireAC *bool `toml:"require_ac"`
}

type rawWindow struct {
	Days  []string `toml:"days"`
	Start *string  `toml:"start"`
	End   *string  `toml:"end"`
}

type rawWake struct {
	Enabled *bool    `toml:"enabled"`
	Days    []string `toml:"days"`
	Time    *string  `toml:"time"`
	Action  *string  `toml:"action"`
}

type rawGuards struct {
	Process []rawProcessGuard `toml:"process"`
}

type rawProcessGuard struct {
	Name       *string `toml:"name"`
	Executable *string `toml:"executable"`
}

// Parse strictly decodes and validates a TOML document.
func Parse(data []byte) (*Config, error) {
	if len(data) > MaxSize {
		return nil, fmt.Errorf("configuration is larger than %d bytes", MaxSize)
	}
	var r raw
	md, err := toml.Decode(string(data), &r)
	if err != nil {
		return nil, fmt.Errorf("invalid TOML: %w", err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return nil, fmt.Errorf("unknown configuration keys: %s", strings.Join(keys, ", "))
	}
	return r.validate()
}

// Load reads and parses the configuration at path. It is intended for
// operator-supplied paths; privileged reads go through the state store.
func Load(path string) (*Config, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a regular file", path)
	}
	if fi.Size() > MaxSize {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, MaxSize)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func (r raw) validate() (*Config, error) {
	cfg := &Config{}
	if r.Version == nil {
		return nil, errors.New("version is required")
	}
	if *r.Version != SupportedVersion {
		return nil, fmt.Errorf("version %d is not supported (this build supports version %d)", *r.Version, SupportedVersion)
	}
	cfg.Version = *r.Version

	cfg.PollInterval = DefaultPollInterval
	if r.PollInterval != nil {
		d, err := time.ParseDuration(*r.PollInterval)
		if err != nil {
			return nil, fmt.Errorf("poll_interval %q is not a valid duration", *r.PollInterval)
		}
		if d < MinPollInterval || d > MaxPollInterval {
			return nil, fmt.Errorf("poll_interval %q must be between %s and %s", *r.PollInterval, MinPollInterval, MaxPollInterval)
		}
		if d%time.Second != 0 {
			return nil, fmt.Errorf("poll_interval %q must be a whole number of seconds", *r.PollInterval)
		}
		cfg.PollInterval = d
	}

	cfg.Power.RequireAC = true
	if r.Power != nil && r.Power.RequireAC != nil {
		cfg.Power.RequireAC = *r.Power.RequireAC
	}

	for i, w := range r.Windows {
		prefix := fmt.Sprintf("windows[%d]", i)
		days, err := ParseDays(w.Days)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", prefix, err)
		}
		if w.Start == nil || w.End == nil {
			return nil, fmt.Errorf("%s: start and end are required", prefix)
		}
		start, err := ParseClock(*w.Start)
		if err != nil {
			return nil, fmt.Errorf("%s: start: %w", prefix, err)
		}
		end, err := ParseClock(*w.End)
		if err != nil {
			return nil, fmt.Errorf("%s: end: %w", prefix, err)
		}
		if start == end {
			return nil, fmt.Errorf("%s: window from %s to %s has zero length", prefix, start, end)
		}
		cfg.Windows = append(cfg.Windows, Window{Days: days, Start: start, End: end})
	}

	if r.Wake != nil {
		if r.Wake.Enabled != nil {
			cfg.Wake.Enabled = *r.Wake.Enabled
		}
		cfg.Wake.Action = WakeActionWakeOrPowerOn
		if r.Wake.Action != nil {
			switch *r.Wake.Action {
			case WakeActionWakeOrPowerOn, WakeActionWake, WakeActionPowerOn:
				cfg.Wake.Action = *r.Wake.Action
			default:
				return nil, fmt.Errorf("wake.action %q must be one of wakeorpoweron, wake, poweron", *r.Wake.Action)
			}
		}
		if cfg.Wake.Enabled {
			days, err := ParseDays(r.Wake.Days)
			if err != nil {
				return nil, fmt.Errorf("wake: %w", err)
			}
			cfg.Wake.Days = days
			if r.Wake.Time == nil {
				return nil, errors.New("wake.time is required when wake is enabled")
			}
			t, err := ParseClock(*r.Wake.Time)
			if err != nil {
				return nil, fmt.Errorf("wake.time: %w", err)
			}
			cfg.Wake.Time = t
		} else if len(r.Wake.Days) > 0 || r.Wake.Time != nil {
			// Still validate so a typo is caught before the operator enables it.
			if len(r.Wake.Days) > 0 {
				if _, err := ParseDays(r.Wake.Days); err != nil {
					return nil, fmt.Errorf("wake: %w", err)
				}
			}
			if r.Wake.Time != nil {
				if _, err := ParseClock(*r.Wake.Time); err != nil {
					return nil, fmt.Errorf("wake.time: %w", err)
				}
			}
		}
	}

	if r.Guards != nil {
		names := map[string]bool{}
		for i, g := range r.Guards.Process {
			prefix := fmt.Sprintf("guards.process[%d]", i)
			if g.Name == nil || *g.Name == "" {
				return nil, fmt.Errorf("%s: name is required", prefix)
			}
			if !guardNameRE.MatchString(*g.Name) {
				return nil, fmt.Errorf("%s: name %q must match %s", prefix, *g.Name, guardNameRE)
			}
			if names[*g.Name] {
				return nil, fmt.Errorf("%s: name %q is used twice", prefix, *g.Name)
			}
			names[*g.Name] = true
			if g.Executable == nil || *g.Executable == "" {
				return nil, fmt.Errorf("%s: executable is required", prefix)
			}
			if err := validateExecutablePath(*g.Executable); err != nil {
				return nil, fmt.Errorf("%s: executable: %w", prefix, err)
			}
			cfg.Guards.Process = append(cfg.Guards.Process, ProcessGuard{Name: *g.Name, Executable: *g.Executable})
		}
	}

	return cfg, nil
}

func validateExecutablePath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%q must be an absolute path", p)
	}
	if filepath.Clean(p) != p {
		return fmt.Errorf("%q must be a clean path (no trailing slash, '.', or '..' components)", p)
	}
	if strings.ContainsAny(p, "~$\n\r\x00") || strings.Contains(p, "${") {
		return fmt.Errorf("%q must not contain shell or environment expansion characters", p)
	}
	return nil
}
