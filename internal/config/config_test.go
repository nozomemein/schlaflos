package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestExampleMatchesRepositoryExample(t *testing.T) {
	data, err := os.ReadFile("../../examples/schlaflos.toml")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != Example {
		t.Fatal("internal/config/example.toml differs from examples/schlaflos.toml")
	}
}

func TestParseExample(t *testing.T) {
	cfg, err := Parse([]byte(Example))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Version != 1 || cfg.PollInterval != 30*time.Second || !cfg.Power.RequireAC {
		t.Fatalf("unexpected top-level values: %+v", cfg)
	}
	if len(cfg.Windows) != 1 || len(cfg.Windows[0].Days) != 7 || cfg.Windows[0].Start != (Clock{Hour: 8, Minute: 0}) || cfg.Windows[0].End != (Clock{Hour: 20, Minute: 0}) {
		t.Fatalf("unexpected windows: %+v", cfg.Windows)
	}
	if !cfg.Wake.Enabled || cfg.Wake.Time != (Clock{Hour: 8, Minute: 0}) || cfg.Wake.Action != WakeActionWakeOrPowerOn || len(cfg.Wake.Days) != 7 {
		t.Fatalf("unexpected wake: %+v", cfg.Wake)
	}
	if len(cfg.Guards.Process) != 1 || cfg.Guards.Process[0].Name != "github-actions-job" {
		t.Fatalf("unexpected guards: %+v", cfg.Guards)
	}
}

func TestDefaults(t *testing.T) {
	cfg, err := Parse([]byte("version = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != DefaultPollInterval {
		t.Fatalf("poll interval = %s", cfg.PollInterval)
	}
	if !cfg.Power.RequireAC {
		t.Fatal("require_ac must default to true")
	}
	if cfg.Wake.Enabled {
		t.Fatal("wake must default to disabled")
	}
}

func TestDaysOrderedMondayFirst(t *testing.T) {
	cfg, err := Parse([]byte("version = 1\n[[windows]]\ndays = [\"sun\", \"mon\", \"sat\"]\nstart = \"01:00\"\nend = \"02:00\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Windows[0].Days
	want := []time.Weekday{time.Monday, time.Saturday, time.Sunday}
	if len(got) != len(want) {
		t.Fatalf("days = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("days = %v, want %v", got, want)
		}
	}
}

func TestRejections(t *testing.T) {
	cases := []struct {
		name string
		toml string
		want string
	}{
		{"missing version", "poll_interval = \"30s\"\n", "version is required"},
		{"unsupported version", "version = 2\n", "not supported"},
		{"unknown top-level key", "version = 1\nrequire_ac = true\n", "unknown configuration keys: require_ac"},
		{"unknown nested key", "version = 1\n[power]\nrequire_ac = true\nrequire_battery = false\n", "unknown configuration keys: power.require_battery"},
		{"secret-looking key", "version = 1\n[runner]\ntoken = \"abc\"\n", "unknown configuration keys"},
		{"invalid duration", "version = 1\npoll_interval = \"soon\"\n", "not a valid duration"},
		{"too short duration", "version = 1\npoll_interval = \"1s\"\n", "between 5s and 1h0m0s"},
		{"too long duration", "version = 1\npoll_interval = \"2h\"\n", "between"},
		{"fractional duration", "version = 1\npoll_interval = \"30.5s\"\n", "whole number of seconds"},
		{"malformed day", "version = 1\n[[windows]]\ndays = [\"monday\"]\nstart = \"08:00\"\nend = \"09:00\"\n", "unknown day \"monday\""},
		{"uppercase day", "version = 1\n[[windows]]\ndays = [\"Mon\"]\nstart = \"08:00\"\nend = \"09:00\"\n", "unknown day \"Mon\""},
		{"duplicate day", "version = 1\n[[windows]]\ndays = [\"mon\", \"mon\"]\nstart = \"08:00\"\nend = \"09:00\"\n", "listed twice"},
		{"empty days", "version = 1\n[[windows]]\ndays = []\nstart = \"08:00\"\nend = \"09:00\"\n", "days must not be empty"},
		{"zero-length window", "version = 1\n[[windows]]\ndays = [\"mon\"]\nstart = \"08:00\"\nend = \"08:00\"\n", "zero length"},
		{"bad time", "version = 1\n[[windows]]\ndays = [\"mon\"]\nstart = \"8:00\"\nend = \"09:00\"\n", "HH:MM"},
		{"out of range time", "version = 1\n[[windows]]\ndays = [\"mon\"]\nstart = \"24:00\"\nend = \"09:00\"\n", "between 00:00 and 23:59"},
		{"missing end", "version = 1\n[[windows]]\ndays = [\"mon\"]\nstart = \"08:00\"\n", "start and end are required"},
		{"wake without time", "version = 1\n[wake]\nenabled = true\ndays = [\"mon\"]\n", "wake.time is required"},
		{"wake bad action", "version = 1\n[wake]\nenabled = true\ndays = [\"mon\"]\ntime = \"08:00\"\naction = \"shutdown\"\n", "wake.action"},
		{"wake disabled but malformed", "version = 1\n[wake]\nenabled = false\ndays = [\"xyz\"]\n", "unknown day"},
		{"guard without name", "version = 1\n[[guards.process]]\nexecutable = \"/bin/ls\"\n", "name is required"},
		{"guard bad name", "version = 1\n[[guards.process]]\nname = \"a b\"\nexecutable = \"/bin/ls\"\n", "must match"},
		{"guard duplicate name", "version = 1\n[[guards.process]]\nname = \"a\"\nexecutable = \"/bin/ls\"\n[[guards.process]]\nname = \"a\"\nexecutable = \"/bin/cat\"\n", "used twice"},
		{"guard relative path", "version = 1\n[[guards.process]]\nname = \"a\"\nexecutable = \"bin/ls\"\n", "absolute path"},
		{"guard tilde", "version = 1\n[[guards.process]]\nname = \"a\"\nexecutable = \"/Users/~x/ls\"\n", "expansion"},
		{"guard env", "version = 1\n[[guards.process]]\nname = \"a\"\nexecutable = \"/opt/$HOME/ls\"\n", "expansion"},
		{"guard unclean", "version = 1\n[[guards.process]]\nname = \"a\"\nexecutable = \"/opt/../bin/ls\"\n", "clean path"},
		{"guard unknown key", "version = 1\n[[guards.process]]\nname = \"a\"\nexecutable = \"/bin/ls\"\ncommand = \"rm -rf /\"\n", "unknown configuration keys: guards.process.command"},
		{"not toml", "version = [\n", "invalid TOML"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.toml))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err, tc.want)
			}
		})
	}
}

func TestOverlappingWindowsAreAllowed(t *testing.T) {
	_, err := Parse([]byte("version = 1\n[[windows]]\ndays = [\"mon\"]\nstart = \"08:00\"\nend = \"12:00\"\n[[windows]]\ndays = [\"mon\"]\nstart = \"10:00\"\nend = \"14:00\"\n"))
	if err != nil {
		t.Fatalf("overlapping windows rejected: %v", err)
	}
}

func TestCrossesMidnight(t *testing.T) {
	if !(Window{Start: Clock{22, 0}, End: Clock{6, 0}}).CrossesMidnight() {
		t.Fatal("22:00-06:00 should cross midnight")
	}
	if (Window{Start: Clock{8, 0}, End: Clock{20, 0}}).CrossesMidnight() {
		t.Fatal("08:00-20:00 should not cross midnight")
	}
}
