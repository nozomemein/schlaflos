package policy

import (
	"testing"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
)

var (
	allDays  = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}
	weekdays = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
	daytime  = config.Window{Days: allDays, Start: hm(8, 0), End: hm(20, 0)}
	weekday  = config.Window{Days: weekdays, Start: hm(8, 0), End: hm(20, 0)}
	// overnight starts Friday 22:00 and ends Saturday 06:00.
	overnight = config.Window{Days: []time.Weekday{time.Friday}, Start: hm(22, 0), End: hm(6, 0)}
)

func tokyo(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func at(loc *time.Location, y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, loc)
}

func TestEvaluateTable(t *testing.T) {
	loc := tokyo(t)
	// 2026-09-14 is a Monday, 2026-09-19 a Saturday, 2026-09-18 a Friday.
	cases := []struct {
		name    string
		in      Input
		prevent bool
		reason  Reason
		inside  bool
	}{
		{"inside window on AC", Input{Now: at(loc, 2026, 9, 14, 12, 0), RequireAC: true, OnAC: true, Windows: []config.Window{daytime}}, true, ReasonScheduledWindow, true},
		{"inside window on battery", Input{Now: at(loc, 2026, 9, 14, 12, 0), RequireAC: true, OnAC: false, Windows: []config.Window{daytime}}, false, ReasonBatteryPower, true},
		{"inside window on battery without require_ac", Input{Now: at(loc, 2026, 9, 14, 12, 0), RequireAC: false, OnAC: false, Windows: []config.Window{daytime}}, true, ReasonScheduledWindow, true},
		{"before window start", Input{Now: at(loc, 2026, 9, 14, 7, 59), RequireAC: true, OnAC: true, Windows: []config.Window{daytime}}, false, ReasonOutsideWindow, false},
		{"at window start (inclusive)", Input{Now: at(loc, 2026, 9, 14, 8, 0), RequireAC: true, OnAC: true, Windows: []config.Window{daytime}}, true, ReasonScheduledWindow, true},
		{"just before window end", Input{Now: at(loc, 2026, 9, 14, 19, 59), RequireAC: true, OnAC: true, Windows: []config.Window{daytime}}, true, ReasonScheduledWindow, true},
		{"at window end (exclusive)", Input{Now: at(loc, 2026, 9, 14, 20, 0), RequireAC: true, OnAC: true, Windows: []config.Window{daytime}}, false, ReasonOutsideWindow, false},
		{"weekday window on Saturday", Input{Now: at(loc, 2026, 9, 19, 12, 0), RequireAC: true, OnAC: true, Windows: []config.Window{weekday}}, false, ReasonOutsideWindow, false},
		{"weekday window on Friday", Input{Now: at(loc, 2026, 9, 18, 12, 0), RequireAC: true, OnAC: true, Windows: []config.Window{weekday}}, true, ReasonScheduledWindow, true},
		{"overnight window Friday 23:00", Input{Now: at(loc, 2026, 9, 18, 23, 0), RequireAC: true, OnAC: true, Windows: []config.Window{overnight}}, true, ReasonScheduledWindow, true},
		{"overnight window Saturday 05:59", Input{Now: at(loc, 2026, 9, 19, 5, 59), RequireAC: true, OnAC: true, Windows: []config.Window{overnight}}, true, ReasonScheduledWindow, true},
		{"overnight window Saturday 06:00", Input{Now: at(loc, 2026, 9, 19, 6, 0), RequireAC: true, OnAC: true, Windows: []config.Window{overnight}}, false, ReasonOutsideWindow, false},
		{"overnight window Saturday 23:00 (day names refer to the start day)", Input{Now: at(loc, 2026, 9, 19, 23, 0), RequireAC: true, OnAC: true, Windows: []config.Window{overnight}}, false, ReasonOutsideWindow, false},
		{"overnight window Thursday 23:00", Input{Now: at(loc, 2026, 9, 17, 23, 0), RequireAC: true, OnAC: true, Windows: []config.Window{overnight}}, false, ReasonOutsideWindow, false},
		{"guard after window ends", Input{Now: at(loc, 2026, 9, 14, 20, 30), RequireAC: true, OnAC: true, Windows: []config.Window{daytime}, ActiveGuards: []string{"ci"}}, true, ReasonActiveProcessGuard, false},
		{"guard inside window reports window", Input{Now: at(loc, 2026, 9, 14, 12, 0), RequireAC: true, OnAC: true, Windows: []config.Window{daytime}, ActiveGuards: []string{"ci"}}, true, ReasonScheduledWindow, true},
		{"guard on battery", Input{Now: at(loc, 2026, 9, 14, 20, 30), RequireAC: true, OnAC: false, Windows: []config.Window{daytime}, ActiveGuards: []string{"ci"}}, false, ReasonBatteryPower, false},
		{"guard on battery without require_ac", Input{Now: at(loc, 2026, 9, 14, 20, 30), RequireAC: false, OnAC: false, ActiveGuards: []string{"ci"}}, true, ReasonActiveProcessGuard, false},
		{"nothing configured", Input{Now: at(loc, 2026, 9, 14, 12, 0), RequireAC: true, OnAC: true}, false, ReasonOutsideWindow, false},
		{"outside window on battery reports outside_window", Input{Now: at(loc, 2026, 9, 14, 22, 0), RequireAC: true, OnAC: false, Windows: []config.Window{daytime}}, false, ReasonOutsideWindow, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := Evaluate(tc.in)
			if d.PreventSleep != tc.prevent || d.Reason != tc.reason || d.InsideWindow != tc.inside {
				t.Fatalf("got prevent=%v reason=%s inside=%v, want prevent=%v reason=%s inside=%v", d.PreventSleep, d.Reason, d.InsideWindow, tc.prevent, tc.reason, tc.inside)
			}
		})
	}
}

func TestActiveGuardsSortedUnique(t *testing.T) {
	d := Evaluate(Input{Now: time.Now(), OnAC: true, ActiveGuards: []string{"b", "a", "b", ""}})
	if len(d.ActiveGuards) != 2 || d.ActiveGuards[0] != "a" || d.ActiveGuards[1] != "b" {
		t.Fatalf("ActiveGuards = %v", d.ActiveGuards)
	}
}

func TestOverlappingWindowsUnion(t *testing.T) {
	loc := tokyo(t)
	a := config.Window{Days: allDays, Start: hm(8, 0), End: hm(12, 0)}
	b := config.Window{Days: allDays, Start: hm(10, 0), End: hm(14, 0)}
	windows := []config.Window{a, b}
	if !InsideAnyWindow(windows, at(loc, 2026, 9, 14, 11, 0)) || !InsideAnyWindow(windows, at(loc, 2026, 9, 14, 13, 0)) {
		t.Fatal("union of overlapping windows broken")
	}
	next, ok := NextWindowTransition(windows, at(loc, 2026, 9, 14, 11, 0))
	if !ok || !next.Equal(at(loc, 2026, 9, 14, 14, 0)) {
		t.Fatalf("next transition = %v, want 14:00 (the 12:00 boundary of window a does not change the union)", next)
	}
}

func TestNextTransition(t *testing.T) {
	loc := tokyo(t)
	cases := []struct {
		name    string
		windows []config.Window
		now     time.Time
		want    time.Time
		ok      bool
	}{
		{"inside daytime -> end", []config.Window{daytime}, at(loc, 2026, 9, 14, 12, 0), at(loc, 2026, 9, 14, 20, 0), true},
		{"before daytime -> start", []config.Window{daytime}, at(loc, 2026, 9, 14, 6, 0), at(loc, 2026, 9, 14, 8, 0), true},
		{"after daytime -> next day start", []config.Window{daytime}, at(loc, 2026, 9, 14, 21, 0), at(loc, 2026, 9, 15, 8, 0), true},
		{"Friday evening weekday window -> Monday", []config.Window{weekday}, at(loc, 2026, 9, 18, 21, 0), at(loc, 2026, 9, 21, 8, 0), true},
		{"inside overnight -> Saturday 06:00", []config.Window{overnight}, at(loc, 2026, 9, 19, 1, 0), at(loc, 2026, 9, 19, 6, 0), true},
		{"Saturday noon overnight -> next Friday 22:00", []config.Window{overnight}, at(loc, 2026, 9, 19, 12, 0), at(loc, 2026, 9, 25, 22, 0), true},
		{"no windows", nil, at(loc, 2026, 9, 19, 12, 0), time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NextWindowTransition(tc.windows, tc.now)
			if ok != tc.ok || (ok && !got.Equal(tc.want)) {
				t.Fatalf("got %v %v, want %v %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestDaylightSavingTransitions(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-03-08: clocks jump from 02:00 to 03:00. A 01:00-04:00 window on
	// that Sunday lasts two real hours but still covers 03:30.
	spring := config.Window{Days: []time.Weekday{time.Sunday}, Start: hm(1, 0), End: hm(4, 0)}
	if !InsideAnyWindow([]config.Window{spring}, at(ny, 2026, 3, 8, 3, 30)) {
		t.Fatal("03:30 on spring-forward day should be inside 01:00-04:00")
	}
	if InsideAnyWindow([]config.Window{spring}, at(ny, 2026, 3, 8, 4, 0)) {
		t.Fatal("04:00 on spring-forward day should be outside")
	}
	// An overnight window across the spring-forward night ends at the local
	// 06:00 of the next day, which is only 7 real hours after 23:00.
	night := config.Window{Days: []time.Weekday{time.Saturday}, Start: hm(23, 0), End: hm(6, 0)}
	start := at(ny, 2026, 3, 7, 23, 0)
	end, ok := NextWindowTransition([]config.Window{night}, start)
	if !ok || !end.Equal(at(ny, 2026, 3, 8, 6, 0)) {
		t.Fatalf("overnight end = %v", end)
	}
	if end.Sub(start) != 6*time.Hour {
		t.Fatalf("overnight window across spring-forward lasted %s, want 6h of real time", end.Sub(start))
	}
	// 2026-11-01: clocks fall back from 02:00 to 01:00. 01:30 occurs twice;
	// both instants are inside a 01:00-02:00 window and the window lasts two
	// real hours.
	fall := config.Window{Days: []time.Weekday{time.Sunday}, Start: hm(1, 0), End: hm(2, 0)}
	first := at(ny, 2026, 11, 1, 1, 30)
	second := first.Add(time.Hour)
	if second.Hour() != 1 || second.Minute() != 30 {
		t.Fatalf("expected the repeated 01:30, got %v", second)
	}
	for _, now := range []time.Time{first, second} {
		if !InsideAnyWindow([]config.Window{fall}, now) {
			t.Fatalf("%v should be inside the 01:00-02:00 window", now)
		}
	}
	end, ok = NextWindowTransition([]config.Window{fall}, at(ny, 2026, 11, 1, 0, 30))
	if !ok || !end.Equal(at(ny, 2026, 11, 1, 1, 0)) {
		t.Fatalf("fall-back start = %v", end)
	}
}

func hm(h, m int) config.Clock { return config.Clock{Hour: h, Minute: m} }
