// Package layout defines the privileged filesystem layout, the launchd label,
// and the ownership and mode table documented in docs/architecture.md.
//
// macOS exposes /var and /etc as symbolic links into /private. Because the
// security model rejects symbolic links anywhere in a privileged path chain,
// the canonical /private/var form is used for every path under /var.
package layout

import (
	"io/fs"
	"path/filepath"
)

// Label is the launchd job label.
const Label = "io.github.nozomemein.schlaflos"

// Layout holds the absolute paths of every artifact managed by schlaflos.
// Default returns the production layout; tests construct a layout rooted in a
// temporary directory.
type Layout struct {
	// ConvenienceBinary is the user-facing executable. launchd never targets it.
	ConvenienceBinary string
	// SupportDir is the root-owned installation directory.
	SupportDir string
	// BinDir contains the root-executed binary.
	BinDir string
	// Binary is the executable invoked by launchd.
	Binary string
	// ConfigPath is the installed configuration.
	ConfigPath string
	// LaunchDaemonsDir is the directory holding the launchd job.
	LaunchDaemonsDir string
	// PlistPath is the launchd job definition.
	PlistPath string
	// StateDir holds the private ledger, the public status projection, and
	// the reconciliation lock.
	StateDir string
	// StatePath is the private ownership ledger.
	StatePath string
	// StatusPath is the redacted, world-readable status projection.
	StatusPath string
	// LockPath is the root-owned reconciliation lock.
	LockPath string
	// LogDir is the directory that receives launchd standard output and
	// error output.
	LogDir string
	// LogPath is the launchd standard output and error path.
	LogPath string
}

// Default returns the production layout.
func Default() Layout {
	return Rooted("/")
}

// Rooted returns the production layout relocated under root. Rooted("/") is
// the production layout itself.
func Rooted(root string) Layout {
	j := func(elem ...string) string {
		return filepath.Join(append([]string{root}, elem...)...)
	}
	support := j("Library", "Application Support", "schlaflos")
	stateDir := j("private", "var", "db", "schlaflos")
	return Layout{
		ConvenienceBinary: j("usr", "local", "bin", "schlaflos"),
		SupportDir:        support,
		BinDir:            filepath.Join(support, "bin"),
		Binary:            filepath.Join(support, "bin", "schlaflos"),
		ConfigPath:        filepath.Join(support, "config.toml"),
		LaunchDaemonsDir:  j("Library", "LaunchDaemons"),
		PlistPath:         j("Library", "LaunchDaemons", Label+".plist"),
		StateDir:          stateDir,
		StatePath:         filepath.Join(stateDir, "state.json"),
		StatusPath:        filepath.Join(stateDir, "status.json"),
		LockPath:          filepath.Join(stateDir, "reconcile.lock"),
		LogDir:            j("private", "var", "log"),
		LogPath:           j("private", "var", "log", "schlaflos.log"),
	}
}

// Modes from the permission table in docs/architecture.md.
const (
	ModeDir    fs.FileMode = 0o755
	ModeBinary fs.FileMode = 0o755
	ModeConfig fs.FileMode = 0o600
	ModePlist  fs.FileMode = 0o600
	ModeState  fs.FileMode = 0o600
	ModeStatus fs.FileMode = 0o644
	ModeLock   fs.FileMode = 0o600
)

// Fixed absolute paths of the macOS tools schlaflos executes. No other
// executables are ever spawned.
const (
	PmsetPath     = "/usr/bin/pmset"
	PsPath        = "/bin/ps"
	LaunchctlPath = "/bin/launchctl"
	PlutilPath    = "/usr/bin/plutil"

	// AutoWakePlist is the powerd preference file holding the recurring
	// power events. It is read only to recover the exact weekday mask when
	// `pmset -g sched` prints the lossy "Some days" label.
	AutoWakePlist = "/Library/Preferences/SystemConfiguration/com.apple.AutoWake.plist"
)
