// Package wakeevents schedules one-off wake events through the IOKit power
// management API. Every event carries schlaflos's own identifier as its
// owner, so ownership is established by the event itself: listing filters
// by owner and cancellation matches owner, time, and type exactly.
//
// The API is a plain user-space framework call; scheduling and cancelling
// require root, listing does not.
package wakeevents

/*
#cgo LDFLAGS: -framework CoreFoundation -framework IOKit
#include <stdlib.h>
#include "iokit.h"
*/
import "C"

import (
	"bufio"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/nozomemein/schlaflos/internal/config"
	"github.com/nozomemein/schlaflos/internal/layout"
)

// Owner is the identifier recorded on every event schlaflos schedules.
const Owner = layout.Label

// Event is one scheduled power event.
type Event struct {
	Time  time.Time
	Owner string
	Type  string
}

// Key identifies an event for comparison: whole seconds and type.
func (e Event) Key() string {
	return strconv.FormatInt(e.Time.Unix(), 10) + " " + e.Type
}

// Scheduler is the boundary used by the reconciler.
type Scheduler interface {
	// List returns every scheduled event, including those of other owners.
	List(ctx context.Context) ([]Event, error)
	Schedule(ctx context.Context, e Event) error
	Cancel(ctx context.Context, e Event) error
}

// IOKitType maps a configuration wake action to the IOKit event type.
func IOKitType(action string) string {
	switch action {
	case config.WakeActionWakeOrPowerOn:
		return "wakepoweron"
	case config.WakeActionPowerOn:
		return "poweron"
	default:
		return "wake"
	}
}

// IOKit is the real scheduler.
type IOKit struct{}

// List implements Scheduler.
func (IOKit) List(_ context.Context) ([]Event, error) {
	cstr := C.schlaflos_list_events()
	if cstr == nil {
		return nil, fmt.Errorf("wake events: out of memory")
	}
	defer C.free(unsafe.Pointer(cstr))
	return ParseList(C.GoString(cstr))
}

// Schedule implements Scheduler.
func (IOKit) Schedule(_ context.Context, e Event) error {
	return call("schedule", e, func(t C.double, owner, typ *C.char) C.int {
		return C.schlaflos_schedule_event(t, owner, typ)
	})
}

// Cancel implements Scheduler.
func (IOKit) Cancel(_ context.Context, e Event) error {
	return call("cancel", e, func(t C.double, owner, typ *C.char) C.int {
		return C.schlaflos_cancel_event(t, owner, typ)
	})
}

func call(op string, e Event, fn func(C.double, *C.char, *C.char) C.int) error {
	if e.Owner == "" || e.Type == "" {
		return fmt.Errorf("wake events: %s: owner and type are required", op)
	}
	owner := C.CString(e.Owner)
	defer C.free(unsafe.Pointer(owner))
	typ := C.CString(e.Type)
	defer C.free(unsafe.Pointer(typ))
	if r := fn(C.double(e.Time.Unix()), owner, typ); r != 0 {
		return fmt.Errorf("wake events: %s %s at %s: IOReturn 0x%x", op, e.Type, e.Time.Format(time.RFC3339), uint32(r))
	}
	return nil
}

// ParseList parses the "unix_seconds\towner\ttype" lines produced by the C
// helper.
func ParseList(s string) ([]Event, error) {
	var out []Event
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("wake events: malformed line %q", line)
		}
		secs, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			return nil, fmt.Errorf("wake events: malformed time in %q", line)
		}
		out = append(out, Event{Time: time.Unix(int64(secs), 0), Owner: parts[1], Type: parts[2]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, sc.Err()
}

// Owned returns the events that belong to schlaflos.
func Owned(events []Event) []Event {
	var out []Event
	for _, e := range events {
		if e.Owner == Owner {
			out = append(out, e)
		}
	}
	return out
}

// Fake is an in-memory Scheduler for tests.
type Fake struct {
	Events      []Event
	ListErr     error
	ScheduleErr error
	CancelErr   error
	Scheduled   []Event
	Cancelled   []Event
}

// List implements Scheduler.
func (f *Fake) List(context.Context) ([]Event, error) {
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	out := append([]Event(nil), f.Events...)
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// Schedule implements Scheduler.
func (f *Fake) Schedule(_ context.Context, e Event) error {
	f.Scheduled = append(f.Scheduled, e)
	if f.ScheduleErr != nil {
		return f.ScheduleErr
	}
	f.Events = append(f.Events, e)
	return nil
}

// Cancel implements Scheduler.
func (f *Fake) Cancel(_ context.Context, e Event) error {
	f.Cancelled = append(f.Cancelled, e)
	if f.CancelErr != nil {
		return f.CancelErr
	}
	for i, x := range f.Events {
		if x.Owner == e.Owner && x.Key() == e.Key() {
			f.Events = append(f.Events[:i], f.Events[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("wake events: cancel: no such event %s", e.Key())
}
