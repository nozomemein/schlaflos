package policy

import (
	"testing"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
)

func TestPlanWakeEvents(t *testing.T) {
	loc := tokyo(t)
	// Monday 2026-09-14 10:30, window 08:00-12:00 every day, hourly.
	w := config.Window{Days: allDays, Start: hm(8, 0), End: hm(12, 0)}
	now := at(loc, 2026, 9, 14, 10, 30)
	got := PlanWakeEvents([]config.Window{w}, time.Hour, now, time.Minute, 24*time.Hour)
	want := []time.Time{
		at(loc, 2026, 9, 14, 11, 0),
		at(loc, 2026, 9, 15, 8, 0),
		at(loc, 2026, 9, 15, 9, 0),
		at(loc, 2026, 9, 15, 10, 0),
	}
	// 12:00 is the exclusive end and 10:30 itself is in the past; the horizon
	// ends at 10:30 the next day so 11:00 on the 15th is excluded.
	assertTimes(t, got, want)
}

func TestPlanWakeEventsMarginAndOverlap(t *testing.T) {
	loc := tokyo(t)
	a := config.Window{Days: allDays, Start: hm(8, 0), End: hm(10, 0)}
	b := config.Window{Days: allDays, Start: hm(9, 0), End: hm(11, 0)}
	now := at(loc, 2026, 9, 14, 8, 59, 30)
	got := PlanWakeEvents([]config.Window{a, b}, time.Hour, now, time.Minute, 3*time.Hour)
	// 09:00 is within the one-minute margin and is skipped; 10:00 comes from
	// window b only once even though window a also ends there.
	assertTimes(t, got, []time.Time{at(loc, 2026, 9, 14, 10, 0)})
}

func TestPlanWakeEventsOvernightAndWeekday(t *testing.T) {
	loc := tokyo(t)
	// Friday 22:00 - Saturday 06:00, every 30 minutes, evaluated Friday 23:10
	// with a six-hour horizon (until 05:10).
	now := at(loc, 2026, 9, 18, 23, 10)
	got := PlanWakeEvents([]config.Window{overnight}, 30*time.Minute, now, time.Minute, 6*time.Hour)
	assertTimes(t, got, []time.Time{
		at(loc, 2026, 9, 18, 23, 30),
		at(loc, 2026, 9, 19, 0, 0),
		at(loc, 2026, 9, 19, 0, 30),
		at(loc, 2026, 9, 19, 1, 0),
		at(loc, 2026, 9, 19, 1, 30),
		at(loc, 2026, 9, 19, 2, 0),
		at(loc, 2026, 9, 19, 2, 30),
		at(loc, 2026, 9, 19, 3, 0),
		at(loc, 2026, 9, 19, 3, 30),
		at(loc, 2026, 9, 19, 4, 0),
		at(loc, 2026, 9, 19, 4, 30),
		at(loc, 2026, 9, 19, 5, 0),
	})
	// Saturday noon: nothing until next Friday, which is outside a 24h horizon.
	if got := PlanWakeEvents([]config.Window{overnight}, time.Hour, at(loc, 2026, 9, 19, 12, 0), time.Minute, 24*time.Hour); len(got) != 0 {
		t.Fatalf("expected no events, got %v", got)
	}
}

func TestPlanWakeEventsDisabled(t *testing.T) {
	if PlanWakeEvents([]config.Window{daytime}, 0, time.Now(), time.Minute, time.Hour) != nil {
		t.Fatal("zero interval must plan nothing")
	}
	if PlanWakeEvents(nil, time.Hour, time.Now(), time.Minute, time.Hour) != nil {
		t.Fatal("no windows must plan nothing")
	}
}

func assertTimes(t *testing.T, got, want []time.Time) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Fatalf("event %d = %v, want %v", i, got[i], want[i])
		}
	}
}
