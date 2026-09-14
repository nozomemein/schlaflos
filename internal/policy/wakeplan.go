package policy

import (
	"sort"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
)

// PlanWakeEvents returns the instants at which one-off wake events should be
// reserved: every window start plus each multiple of interval before the
// window ends, restricted to (now+margin, now+horizon]. A Mac that fell asleep
// during a window is therefore woken again within one interval, and the next
// reconciliation decides whether it stays awake.
//
// Occurrences that started before now are included so that the remainder of
// the current window is covered. The result is sorted and de-duplicated.
func PlanWakeEvents(windows []config.Window, interval time.Duration, now time.Time, margin, horizon time.Duration) []time.Time {
	if interval <= 0 || len(windows) == 0 || horizon <= 0 {
		return nil
	}
	earliest := now.Add(margin)
	latest := now.Add(horizon)
	seen := map[int64]bool{}
	var out []time.Time
	year, month, day := now.Date()
	days := int(horizon/(24*time.Hour)) + 2
	for back := -1; back <= days; back++ {
		for _, w := range windows {
			start := time.Date(year, month, day+back, w.Start.Hour, w.Start.Minute, 0, 0, now.Location())
			if !w.HasDay(start.Weekday()) {
				continue
			}
			end := windowEnd(w, start)
			for t := start; t.Before(end); t = t.Add(interval) {
				if !t.After(earliest) || t.After(latest) {
					continue
				}
				if seen[t.Unix()] {
					continue
				}
				seen[t.Unix()] = true
				out = append(out, t)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}
