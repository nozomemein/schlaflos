// Package policy is the pure desired-state calculation:
//
//	prevent_sleep = power_allowed AND (inside_any_window OR any_process_guard_active)
//
// It accepts values and returns a decision. It knows nothing about commands,
// files, or macOS.
package policy

import (
	"sort"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
)

// Reason is a stable reason code reported alongside the decision.
type Reason string

const (
	// ReasonScheduledWindow: the local time is inside a configured window.
	ReasonScheduledWindow Reason = "scheduled_window"
	// ReasonActiveProcessGuard: outside every window, but a guard matches a
	// running process.
	ReasonActiveProcessGuard Reason = "active_process_guard"
	// ReasonBatteryPower: a window or guard would apply, but the machine is on
	// battery and require_ac is enabled.
	ReasonBatteryPower Reason = "battery_power"
	// ReasonOutsideWindow: no window and no guard applies.
	ReasonOutsideWindow Reason = "outside_window"
)

// Input is everything the policy needs.
type Input struct {
	Now          time.Time
	RequireAC    bool
	OnAC         bool
	Windows      []config.Window
	ActiveGuards []string
}

// Decision is the policy output.
type Decision struct {
	// PreventSleep is schlaflos's own inhibition request.
	PreventSleep bool
	Reason       Reason
	// InsideWindow reports whether Now is inside at least one window.
	InsideWindow bool
	// ActiveGuards is the sorted, de-duplicated list of matching guard names.
	ActiveGuards []string
	// NextTransition is the next instant at which the window state flips,
	// when a window is configured.
	NextTransition    time.Time
	HasNextTransition bool
}

// Evaluate calculates the desired state without side effects.
func Evaluate(in Input) Decision {
	d := Decision{
		InsideWindow: InsideAnyWindow(in.Windows, in.Now),
		ActiveGuards: uniqueSorted(in.ActiveGuards),
	}
	d.NextTransition, d.HasNextTransition = NextWindowTransition(in.Windows, in.Now)

	powerAllowed := in.OnAC || !in.RequireAC
	wants := d.InsideWindow || len(d.ActiveGuards) > 0

	switch {
	case !wants:
		d.Reason = ReasonOutsideWindow
	case !powerAllowed:
		d.Reason = ReasonBatteryPower
	case d.InsideWindow:
		d.PreventSleep = true
		d.Reason = ReasonScheduledWindow
	default:
		d.PreventSleep = true
		d.Reason = ReasonActiveProcessGuard
	}
	return d
}

// InsideAnyWindow reports whether now falls inside at least one window,
// evaluated in now's location.
func InsideAnyWindow(windows []config.Window, now time.Time) bool {
	for _, w := range windows {
		if insideWindow(w, now) {
			return true
		}
	}
	return false
}

// insideWindow checks the occurrences of w that start today or yesterday
// (yesterday matters for windows that cross midnight).
func insideWindow(w config.Window, now time.Time) bool {
	year, month, day := now.Date()
	for back := 0; back <= 1; back++ {
		start := time.Date(year, month, day-back, w.Start.Hour, w.Start.Minute, 0, 0, now.Location())
		if !w.HasDay(start.Weekday()) {
			continue
		}
		end := windowEnd(w, start)
		if !now.Before(start) && now.Before(end) {
			return true
		}
	}
	return false
}

// windowEnd derives the exclusive end instant from a start instant. Crossing
// midnight adds a calendar day so daylight-saving changes follow local rules.
func windowEnd(w config.Window, start time.Time) time.Time {
	y, m, d := start.Date()
	if w.CrossesMidnight() {
		d++
	}
	return time.Date(y, m, d, w.End.Hour, w.End.Minute, 0, 0, start.Location())
}

// lookahead bounds the search for the next boundary; one week covers every
// weekly schedule, one extra day covers windows that cross midnight.
const lookaheadDays = 8

// NextWindowTransition returns the earliest instant after now at which the
// union of the windows changes state. It is used for the status report only;
// process guards are not predictable and are ignored.
func NextWindowTransition(windows []config.Window, now time.Time) (time.Time, bool) {
	if len(windows) == 0 {
		return time.Time{}, false
	}
	current := InsideAnyWindow(windows, now)
	var boundaries []time.Time
	year, month, day := now.Date()
	for back := -1; back <= lookaheadDays; back++ {
		for _, w := range windows {
			start := time.Date(year, month, day+back, w.Start.Hour, w.Start.Minute, 0, 0, now.Location())
			if !w.HasDay(start.Weekday()) {
				continue
			}
			end := windowEnd(w, start)
			for _, b := range []time.Time{start, end} {
				if b.After(now) {
					boundaries = append(boundaries, b)
				}
			}
		}
	}
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i].Before(boundaries[j]) })
	for _, b := range boundaries {
		if InsideAnyWindow(windows, b) != current {
			return b, true
		}
	}
	return time.Time{}, false
}

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
