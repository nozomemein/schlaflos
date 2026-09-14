// Package wake inspects and mutates the single recurring pmset power event
// pair ("repeat") through /usr/bin/pmset with fixed arguments.
//
// pmset stores at most one recurring "on" event (wake, poweron,
// wakeorpoweron) and one recurring "off" event (sleep, shutdown, restart).
// Writing either replaces the whole pair, so the pair is modelled as one
// Schedule and treated as one exclusively managed resource.
package wake

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
)

// DayMask uses the IOPM bit layout: Monday = 1<<0 through Sunday = 1<<6.
type DayMask uint8

const (
	Monday DayMask = 1 << iota
	Tuesday
	Wednesday
	Thursday
	Friday
	Saturday
	Sunday

	EveryDay DayMask = 0x7f
	Weekdays DayMask = 0x1f
	Weekends DayMask = 0x60
)

// dayLetters is the pmset spelling: M T W R F S U.
var dayLetters = []struct {
	mask   DayMask
	letter string
	label  string
	day    time.Weekday
}{
	{Monday, "M", "Monday", time.Monday},
	{Tuesday, "T", "Tuesday", time.Tuesday},
	{Wednesday, "W", "Wednesday", time.Wednesday},
	{Thursday, "R", "Thursday", time.Thursday},
	{Friday, "F", "Friday", time.Friday},
	{Saturday, "S", "Saturday", time.Saturday},
	{Sunday, "U", "Sunday", time.Sunday},
}

// MaskFromWeekdays converts configuration weekdays to a DayMask.
func MaskFromWeekdays(days []time.Weekday) DayMask {
	var m DayMask
	for _, d := range days {
		for _, dl := range dayLetters {
			if dl.day == d {
				m |= dl.mask
			}
		}
	}
	return m
}

// Letters renders the mask as the pmset argument, for example "MTWRF".
func (m DayMask) Letters() string {
	var b strings.Builder
	for _, dl := range dayLetters {
		if m&dl.mask != 0 {
			b.WriteString(dl.letter)
		}
	}
	return b.String()
}

// Label renders the mask exactly as `pmset -g sched` prints it. Masks that
// pmset cannot describe collapse to "Some days".
func (m DayMask) Label() string {
	switch m {
	case EveryDay:
		return "every day"
	case Weekdays:
		return "weekdays only"
	case Weekends:
		return "weekends only"
	}
	for _, dl := range dayLetters {
		if m == dl.mask {
			return dl.label
		}
	}
	return "Some days"
}

func maskFromLabel(label string) (DayMask, bool) {
	switch label {
	case "every day":
		return EveryDay, true
	case "weekdays only":
		return Weekdays, true
	case "weekends only":
		return Weekends, true
	}
	for _, dl := range dayLetters {
		if label == dl.label {
			return dl.mask, true
		}
	}
	return 0, false
}

// Event types as accepted by `pmset repeat`.
const (
	TypeWakeOrPowerOn = "wakeorpoweron"
	TypeWake          = "wake"
	TypePowerOn       = "poweron"
	TypeSleep         = "sleep"
	TypeShutdown      = "shutdown"
	TypeRestart       = "restart"
)

// normaliseType maps the raw IOPM constant printed by `pmset -g sched`
// ("wakepoweron") to the `pmset repeat` spelling.
func normaliseType(t string) (string, error) {
	switch t {
	case "wakepoweron", TypeWakeOrPowerOn:
		return TypeWakeOrPowerOn, nil
	case TypeWake, TypePowerOn, TypeSleep, TypeShutdown, TypeRestart:
		return t, nil
	}
	return "", fmt.Errorf("unknown repeating event type %q", t)
}

func isOnType(t string) bool {
	return t == TypeWakeOrPowerOn || t == TypeWake || t == TypePowerOn
}

// Event is one recurring power event.
type Event struct {
	Type    string  `json:"type"`
	Minutes int     `json:"minutes"`
	Days    DayMask `json:"days"`
	// DaysExact is false when the days came from the lossy "Some days" label
	// and could not be recovered from the powerd preference file. DaysLabel
	// then carries the observed label.
	DaysExact bool   `json:"days_exact"`
	DaysLabel string `json:"days_label,omitempty"`
}

// Equal compares two events. Inexact day sets are compared by label.
func (e *Event) Equal(o *Event) bool {
	if e == nil || o == nil {
		return e == nil && o == nil
	}
	if e.Type != o.Type || e.Minutes != o.Minutes {
		return false
	}
	if e.DaysExact && o.DaysExact {
		return e.Days == o.Days
	}
	return e.label() == o.label()
}

func (e *Event) label() string {
	if e.DaysExact {
		return e.Days.Label()
	}
	return e.DaysLabel
}

// String renders the event as a `pmset repeat` argument triple.
func (e *Event) String() string {
	days := e.Days.Letters()
	if !e.DaysExact {
		days = "?" + strings.ReplaceAll(e.DaysLabel, " ", "-")
	}
	return fmt.Sprintf("%s %s %02d:%02d:00", e.Type, days, e.Minutes/60, e.Minutes%60)
}

// Schedule is the recurring on/off pair. Both nil means no recurring
// schedule exists.
type Schedule struct {
	On  *Event `json:"on,omitempty"`
	Off *Event `json:"off,omitempty"`
}

// Empty reports whether no recurring event exists.
func (s Schedule) Empty() bool { return s.On == nil && s.Off == nil }

// Exact reports whether every day set is known precisely.
func (s Schedule) Exact() bool {
	return (s.On == nil || s.On.DaysExact) && (s.Off == nil || s.Off.DaysExact)
}

// Equal compares two schedules.
func (s Schedule) Equal(o Schedule) bool {
	return s.On.Equal(o.On) && s.Off.Equal(o.Off)
}

// String renders the schedule for status output and logs.
func (s Schedule) String() string {
	if s.Empty() {
		return "none"
	}
	var parts []string
	if s.On != nil {
		parts = append(parts, s.On.String())
	}
	if s.Off != nil {
		parts = append(parts, s.Off.String())
	}
	return strings.Join(parts, "; ")
}

// FromConfig builds the desired schedule from an enabled wake configuration.
func FromConfig(w config.Wake) Schedule {
	if !w.Enabled {
		return Schedule{}
	}
	return Schedule{On: &Event{
		Type:      w.Action,
		Minutes:   w.Time.Minutes(),
		Days:      MaskFromWeekdays(w.Days),
		DaysExact: true,
	}}
}

// repeatLine matches "  wakepoweron at 8:00AM every day".
var repeatLine = regexp.MustCompile(`^\s*([a-z]+) at (\d{1,2}):(\d{2})(AM|PM) (.+?)\s*$`)

// ParseSched parses `pmset -g sched` output and returns the recurring
// schedule. One-off scheduled events are ignored.
func ParseSched(out []byte) (Schedule, error) {
	var s Schedule
	sc := bufio.NewScanner(bytes.NewReader(out))
	inRepeating := false
	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "Repeating power events:":
			inRepeating = true
			continue
		case trimmed == "Scheduled power events:" || trimmed == "No scheduled events.":
			inRepeating = false
			continue
		}
		if !inRepeating || trimmed == "" {
			continue
		}
		m := repeatLine.FindStringSubmatch(line)
		if m == nil {
			return Schedule{}, fmt.Errorf("parse pmset schedule: unrecognised repeating event line %q", trimmed)
		}
		typ, err := normaliseType(m[1])
		if err != nil {
			return Schedule{}, fmt.Errorf("parse pmset schedule: %w", err)
		}
		hour, _ := strconv.Atoi(m[2])
		minute, _ := strconv.Atoi(m[3])
		if m[4] == "PM" && hour < 12 {
			hour += 12
		}
		if hour > 23 || minute > 59 {
			return Schedule{}, fmt.Errorf("parse pmset schedule: invalid time in %q", trimmed)
		}
		ev := &Event{Type: typ, Minutes: hour*60 + minute, DaysLabel: m[5]}
		if mask, ok := maskFromLabel(m[5]); ok {
			ev.Days, ev.DaysExact = mask, true
		}
		if isOnType(typ) {
			if s.On != nil {
				return Schedule{}, fmt.Errorf("parse pmset schedule: two recurring on events")
			}
			s.On = ev
		} else {
			if s.Off != nil {
				return Schedule{}, fmt.Errorf("parse pmset schedule: two recurring off events")
			}
			s.Off = ev
		}
	}
	return s, sc.Err()
}

// Adapter talks to pmset. PlistPath overrides the powerd preference file
// used to recover exact day masks; it defaults to layout.AutoWakePlist.
type Adapter struct {
	Runner    cmdrun.Runner
	PlistPath string
}

// Current reads the recurring schedule. When `pmset -g sched` prints a lossy
// day label, the exact mask is recovered from the powerd preference file if
// it is readable; otherwise the event keeps DaysExact = false.
func (a Adapter) Current(ctx context.Context) (Schedule, error) {
	out, err := a.Runner.Run(ctx, layout.PmsetPath, "-g", "sched")
	if err != nil {
		return Schedule{}, fmt.Errorf("read wake schedule: %w", err)
	}
	s, err := ParseSched(out)
	if err != nil {
		return Schedule{}, err
	}
	if s.Exact() {
		return s, nil
	}
	plist := a.PlistPath
	if plist == "" {
		plist = layout.AutoWakePlist
	}
	xmlOut, err := a.Runner.Run(ctx, layout.PlutilPath, "-convert", "xml1", "-o", "-", plist)
	if err != nil {
		return s, nil
	}
	exact, err := ParseAutoWakePlist(xmlOut)
	if err != nil {
		return s, nil
	}
	refine := func(observed, fromPlist *Event) {
		if observed == nil || observed.DaysExact || fromPlist == nil {
			return
		}
		if fromPlist.Type == observed.Type && fromPlist.Minutes == observed.Minutes && fromPlist.Days.Label() == observed.DaysLabel {
			observed.Days, observed.DaysExact = fromPlist.Days, true
		}
	}
	refine(s.On, exact.On)
	refine(s.Off, exact.Off)
	return s, nil
}

// Apply makes the recurring schedule equal to s: an empty schedule cancels
// every recurring event, otherwise the pair is written in one pmset call.
// The result is read back and verified.
func (a Adapter) Apply(ctx context.Context, s Schedule) error {
	if !s.Exact() {
		return fmt.Errorf("write wake schedule: refusing to write a schedule with unknown days (%s)", s)
	}
	args := []string{"repeat"}
	if s.Empty() {
		args = append(args, "cancel")
	} else {
		for _, ev := range []*Event{s.On, s.Off} {
			if ev == nil {
				continue
			}
			args = append(args, ev.Type, ev.Days.Letters(), fmt.Sprintf("%02d:%02d:00", ev.Minutes/60, ev.Minutes%60))
		}
	}
	if _, err := a.Runner.Run(ctx, layout.PmsetPath, args...); err != nil {
		return fmt.Errorf("write wake schedule %s: %w", s, err)
	}
	observed, err := a.Current(ctx)
	if err != nil {
		return fmt.Errorf("verify wake schedule %s: %w", s, err)
	}
	if !observed.Equal(s) {
		return fmt.Errorf("verify wake schedule %s: pmset reports %s", s, observed)
	}
	return nil
}
