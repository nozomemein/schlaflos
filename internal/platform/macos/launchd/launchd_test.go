package launchd

import (
	"context"
	"strings"
	"testing"

	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
)

func TestRenderPlist(t *testing.T) {
	out, err := RenderPlist(Params{
		Label:                "io.github.nozomemein.schlaflos",
		ProgramArguments:     []string{"/Library/Application Support/schlaflos/bin/schlaflos", "reconcile"},
		StartIntervalSeconds: 30,
		LogPath:              "/private/var/log/schlaflos.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"<string>io.github.nozomemein.schlaflos</string>",
		"<string>/Library/Application Support/schlaflos/bin/schlaflos</string>",
		"<string>reconcile</string>",
		"<key>StartInterval</key>\n\t<integer>30</integer>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<string>/usr/bin:/bin:/usr/sbin:/sbin</string>",
		"<key>StandardErrorPath</key>\n\t<string>/private/var/log/schlaflos.log</string>",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("plist missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "/usr/local/bin") {
		t.Fatal("plist must not reference the convenience binary")
	}
	if _, err := RenderPlist(Params{}); err == nil {
		t.Fatal("incomplete params accepted")
	}
	esc, _ := RenderPlist(Params{Label: "a<b", ProgramArguments: []string{"/x&y"}, StartIntervalSeconds: 5, LogPath: "/l"})
	if !strings.Contains(string(esc), "a&lt;b") || !strings.Contains(string(esc), "/x&amp;y") {
		t.Fatalf("values not escaped:\n%s", esc)
	}
}

func TestManager(t *testing.T) {
	notFound := &cmdrun.ExitError{Code: 113, Stderr: "Bad request.\nCould not find service \"io.github.nozomemein.schlaflos\" in domain for system"}
	fake := &cmdrun.Fake{Responses: map[string]cmdrun.Response{
		"/bin/launchctl print system/io.github.nozomemein.schlaflos": {Err: notFound},
	}}
	m := Manager{Runner: fake}
	loaded, err := m.Loaded(context.Background())
	if err != nil || loaded {
		t.Fatalf("Loaded = %v, %v", loaded, err)
	}
	if err := m.Bootout(context.Background()); err != nil {
		t.Fatalf("Bootout when not loaded: %v", err)
	}
	if len(fake.Calls) != 2 {
		t.Fatalf("bootout attempted on an unloaded service: %v", fake.CallLines())
	}

	fake = &cmdrun.Fake{Responses: map[string]cmdrun.Response{
		"/bin/launchctl print system/io.github.nozomemein.schlaflos":        {Stdout: "system/io.github.nozomemein.schlaflos = {\n\tstate = waiting\n}\n"},
		"/bin/launchctl bootout system/io.github.nozomemein.schlaflos":      {},
		"/bin/launchctl enable system/io.github.nozomemein.schlaflos":       {},
		"/bin/launchctl bootstrap system /Library/LaunchDaemons/x.plist":    {},
		"/bin/launchctl kickstart -k system/io.github.nozomemein.schlaflos": {},
	}}
	m = Manager{Runner: fake}
	if loaded, err := m.Loaded(context.Background()); err != nil || !loaded {
		t.Fatalf("Loaded = %v, %v", loaded, err)
	}
	if err := m.Bootout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Bootstrap(context.Background(), "/Library/LaunchDaemons/x.plist"); err != nil {
		t.Fatal(err)
	}
	if err := m.Kickstart(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/bin/launchctl print system/io.github.nozomemein.schlaflos",
		"/bin/launchctl print system/io.github.nozomemein.schlaflos",
		"/bin/launchctl bootout system/io.github.nozomemein.schlaflos",
		"/bin/launchctl enable system/io.github.nozomemein.schlaflos",
		"/bin/launchctl bootstrap system /Library/LaunchDaemons/x.plist",
		"/bin/launchctl kickstart -k system/io.github.nozomemein.schlaflos",
	}
	got := fake.CallLines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Any other launchctl failure is an error, not "not loaded".
	fake = &cmdrun.Fake{Responses: map[string]cmdrun.Response{
		"/bin/launchctl print system/io.github.nozomemein.schlaflos": {Err: &cmdrun.ExitError{Code: 1, Stderr: "Operation not permitted"}},
	}}
	if _, err := (Manager{Runner: fake}).Loaded(context.Background()); err == nil {
		t.Fatal("permission error treated as not loaded")
	}
}
