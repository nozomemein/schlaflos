package power

import (
	"context"
	"errors"
	"testing"

	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
)

const battAC = "Now drawing from 'AC Power'\n -InternalBattery-0 (id=34996323)\t100%; charged; 0:00 remaining present: true\n"
const battBattery = "Now drawing from 'Battery Power'\n -InternalBattery-0 (id=34996323)\t87%; discharging; 5:12 remaining present: true\n"
const battDesktop = "Now drawing from 'AC Power'\n"

const pmsetGDefault = `System-wide power settings:
Currently in use:
 standby              1
 Sleep On Power Button 1
 sleep                1 (sleep prevented by powerd)
 hibernatemode        3
`
const pmsetGDisabled = "System-wide power settings:\n SleepDisabled\t\t1\nCurrently in use:\n standby              1\n"
const pmsetGEnabled = "System-wide power settings:\n SleepDisabled\t\t0\nCurrently in use:\n standby              1\n"

func TestParseBatt(t *testing.T) {
	cases := []struct {
		in   string
		want Source
		err  bool
	}{
		{battAC, SourceAC, false},
		{battBattery, SourceBattery, false},
		{battDesktop, SourceAC, false},
		{"Now drawing from 'UPS Power'\n", SourceUPS, false},
		{"Now drawing from 'Fusion Power'\n", SourceUnknown, true},
		{"", SourceUnknown, true},
	}
	for _, tc := range cases {
		got, err := ParseBatt([]byte(tc.in))
		if (err != nil) != tc.err || got != tc.want {
			t.Fatalf("ParseBatt(%q) = %v, %v", tc.in, got, err)
		}
	}
	if SourceUPS.AC() || SourceBattery.AC() || !SourceAC.AC() {
		t.Fatal("AC classification wrong")
	}
}

func TestParseSleepDisabled(t *testing.T) {
	cases := []struct {
		in   string
		want bool
		err  bool
	}{
		{pmsetGDefault, false, false},
		{pmsetGDisabled, true, false},
		{pmsetGEnabled, false, false},
		{"System-wide power settings:\n SleepDisabled\t\tyes\n", false, true},
	}
	for _, tc := range cases {
		got, err := ParseSleepDisabled([]byte(tc.in))
		if (err != nil) != tc.err || got != tc.want {
			t.Fatalf("ParseSleepDisabled(%q) = %v, %v", tc.in, got, err)
		}
	}
}

func TestSetSleepDisabledArgvAndVerification(t *testing.T) {
	state := "0"
	fake := &cmdrun.Fake{Handler: func(c cmdrun.Call) (cmdrun.Response, bool) {
		switch c.String() {
		case "/usr/bin/pmset -a disablesleep 1":
			state = "1"
			return cmdrun.Response{}, true
		case "/usr/bin/pmset -a disablesleep 0":
			state = "0"
			return cmdrun.Response{}, true
		case "/usr/bin/pmset -g":
			if state == "1" {
				return cmdrun.Response{Stdout: pmsetGDisabled}, true
			}
			return cmdrun.Response{Stdout: pmsetGDefault}, true
		}
		return cmdrun.Response{}, false
	}}
	a := Adapter{Runner: fake}
	if err := a.SetSleepDisabled(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := a.SetSleepDisabled(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/pmset -a disablesleep 1",
		"/usr/bin/pmset -g",
		"/usr/bin/pmset -a disablesleep 0",
		"/usr/bin/pmset -g",
	}
	got := fake.CallLines()
	if len(got) != len(want) {
		t.Fatalf("calls = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSetSleepDisabledDetectsUnverifiedWrite(t *testing.T) {
	fake := &cmdrun.Fake{Responses: map[string]cmdrun.Response{
		"/usr/bin/pmset -a disablesleep 1": {},
		"/usr/bin/pmset -g":                {Stdout: pmsetGDefault},
	}}
	if err := (Adapter{Runner: fake}).SetSleepDisabled(context.Background(), true); err == nil {
		t.Fatal("unverified write accepted")
	}
	fake = &cmdrun.Fake{Responses: map[string]cmdrun.Response{
		"/usr/bin/pmset -a disablesleep 1": {Err: errors.New("denied")},
	}}
	if err := (Adapter{Runner: fake}).SetSleepDisabled(context.Background(), true); err == nil {
		t.Fatal("failed write accepted")
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("verification attempted after failed write: %v", fake.CallLines())
	}
}
