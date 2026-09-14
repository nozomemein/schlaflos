package layout

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultMatchesDocumentedLayout(t *testing.T) {
	l := Default()
	want := map[string]string{
		"ConvenienceBinary": "/usr/local/bin/schlaflos",
		"SupportDir":        "/Library/Application Support/schlaflos",
		"BinDir":            "/Library/Application Support/schlaflos/bin",
		"Binary":            "/Library/Application Support/schlaflos/bin/schlaflos",
		"ConfigPath":        "/Library/Application Support/schlaflos/config.toml",
		"LaunchDaemonsDir":  "/Library/LaunchDaemons",
		"PlistPath":         "/Library/LaunchDaemons/io.github.nozomemein.schlaflos.plist",
		"StateDir":          "/private/var/db/schlaflos",
		"StatePath":         "/private/var/db/schlaflos/state.json",
		"StatusPath":        "/private/var/db/schlaflos/status.json",
		"LockPath":          "/private/var/db/schlaflos/reconcile.lock",
		"LogDir":            "/private/var/log",
		"LogPath":           "/private/var/log/schlaflos.log",
	}
	got := fields(l)
	for name, path := range want {
		if got[name] != path {
			t.Errorf("%s = %q, want %q", name, got[name], path)
		}
	}
	if len(got) != len(want) {
		t.Errorf("layout has %d fields, test covers %d", len(got), len(want))
	}
}

func TestPathsAreAbsoluteCleanAndCanonical(t *testing.T) {
	for name, p := range fields(Default()) {
		if !filepath.IsAbs(p) {
			t.Errorf("%s = %q is not absolute", name, p)
		}
		if filepath.Clean(p) != p {
			t.Errorf("%s = %q is not clean", name, p)
		}
		// /var and /etc are symbolic links on macOS; the layout must use the
		// /private form so the path chain contains no symbolic link.
		if strings.HasPrefix(p, "/var/") || strings.HasPrefix(p, "/etc/") || strings.HasPrefix(p, "/tmp/") {
			t.Errorf("%s = %q uses a symlinked top-level directory", name, p)
		}
	}
	for _, tool := range []string{PmsetPath, PsPath, LaunchctlPath, PlutilPath, AutoWakePlist} {
		if !filepath.IsAbs(tool) || filepath.Clean(tool) != tool {
			t.Errorf("tool path %q is not absolute and clean", tool)
		}
	}
}

func TestRootedRelocatesEverything(t *testing.T) {
	root := "/tmp/x"
	def := fields(Default())
	for name, p := range fields(Rooted(root)) {
		if !strings.HasPrefix(p, root+"/") {
			t.Errorf("%s = %q is not under %s", name, p, root)
		}
		if strings.TrimPrefix(p, root) != def[name] {
			t.Errorf("%s = %q does not mirror the default %q", name, p, def[name])
		}
	}
	if Rooted("/") != Default() {
		t.Error(`Rooted("/") must equal Default()`)
	}
}

func TestBinaryLivesUnderSupportDir(t *testing.T) {
	l := Default()
	if filepath.Dir(l.Binary) != l.BinDir || filepath.Dir(l.BinDir) != l.SupportDir {
		t.Errorf("binary %q is not under %q", l.Binary, l.SupportDir)
	}
	if filepath.Dir(l.StatePath) != l.StateDir || filepath.Dir(l.StatusPath) != l.StateDir || filepath.Dir(l.LockPath) != l.StateDir {
		t.Error("state, status, and lock must share the state directory")
	}
	if l.ConvenienceBinary == l.Binary {
		t.Error("the convenience binary must not be the launchd target")
	}
}

func TestModeTable(t *testing.T) {
	cases := map[string]struct{ got, want uint32 }{
		"dir":    {uint32(ModeDir), 0o755},
		"binary": {uint32(ModeBinary), 0o755},
		"config": {uint32(ModeConfig), 0o600},
		"plist":  {uint32(ModePlist), 0o600},
		"state":  {uint32(ModeState), 0o600},
		"status": {uint32(ModeStatus), 0o644},
		"lock":   {uint32(ModeLock), 0o600},
	}
	for name, c := range cases {
		if c.got != c.want {
			t.Errorf("%s mode = %04o, want %04o", name, c.got, c.want)
		}
		if c.got&0o022 != 0 {
			t.Errorf("%s mode %04o is group- or world-writable", name, c.got)
		}
	}
}

func fields(l Layout) map[string]string {
	return map[string]string{
		"ConvenienceBinary": l.ConvenienceBinary,
		"SupportDir":        l.SupportDir,
		"BinDir":            l.BinDir,
		"Binary":            l.Binary,
		"ConfigPath":        l.ConfigPath,
		"LaunchDaemonsDir":  l.LaunchDaemonsDir,
		"PlistPath":         l.PlistPath,
		"StateDir":          l.StateDir,
		"StatePath":         l.StatePath,
		"StatusPath":        l.StatusPath,
		"LockPath":          l.LockPath,
		"LogDir":            l.LogDir,
		"LogPath":           l.LogPath,
	}
}
