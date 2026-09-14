package wakeevents

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
)

func TestParseList(t *testing.T) {
	in := "1789344000\tcom.apple.alarm.user-invisible-com.apple.acmd.alarm\twake\n1789340400\tio.github.nozomemein.schlaflos\twakepoweron\n\n"
	events, err := ParseList(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Owner != Owner || events[0].Type != "wakepoweron" || events[0].Time.Unix() != 1789340400 {
		t.Fatalf("events = %+v", events)
	}
	if owned := Owned(events); len(owned) != 1 || owned[0].Owner != Owner {
		t.Fatalf("Owned = %+v", owned)
	}
	if _, err := ParseList("garbage\n"); err == nil {
		t.Fatal("malformed line accepted")
	}
	if _, err := ParseList("x\ty\tz\n"); err == nil {
		t.Fatal("malformed time accepted")
	}
}

func TestIOKitType(t *testing.T) {
	if IOKitType(config.WakeActionWakeOrPowerOn) != "wakepoweron" || IOKitType(config.WakeActionWake) != "wake" || IOKitType(config.WakeActionPowerOn) != "poweron" {
		t.Fatal("type mapping wrong")
	}
}

func TestFake(t *testing.T) {
	f := &Fake{}
	e := Event{Time: time.Unix(1789340400, 0), Owner: Owner, Type: "wake"}
	if err := f.Schedule(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.List(context.Background()); len(list) != 1 {
		t.Fatal("event not listed")
	}
	if err := f.Cancel(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if err := f.Cancel(context.Background(), e); err == nil {
		t.Fatal("cancel of missing event succeeded")
	}
}

func TestIOKitListReadOnly(t *testing.T) {
	if os.Getenv("SCHLAFLOS_READONLY_INTEGRATION") == "" {
		t.Skip("set SCHLAFLOS_READONLY_INTEGRATION=1 to list the local scheduled events")
	}
	events, err := IOKit{}.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		t.Logf("%s %s by %q", e.Time.Format(time.RFC3339), e.Type, e.Owner)
	}
}

func TestIOKitRejectsIncompleteEvent(t *testing.T) {
	if err := (IOKit{}).Schedule(context.Background(), Event{Time: time.Now()}); err == nil {
		t.Fatal("event without owner accepted")
	}
}
