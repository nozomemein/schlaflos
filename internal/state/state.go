// Package state persists the private ownership ledger (state.json) and the
// redacted public status projection (status.json).
//
// The ledger records pre-install baselines and the last values written by
// schlaflos so that a later run can tell an owned value from an external one.
// The status projection contains only what `schlaflos status` displays.
package state

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wake"
	"github.com/nozomemein/schlaflos/internal/safefs"
)

// SchemaVersion is the ledger schema understood by this build.
const SchemaVersion = 1

// ErrNoState is returned when the ledger does not exist.
var ErrNoState = errors.New("state: private state does not exist")

// SleepRecord is one observed or written disablesleep value.
type SleepRecord struct {
	Disabled bool      `json:"disabled"`
	At       time.Time `json:"at"`
}

// WakeRecord is one observed or written recurring schedule.
type WakeRecord struct {
	Schedule wake.Schedule `json:"schedule"`
	At       time.Time     `json:"at"`
}

// Transition is the last successful change of the disablesleep setting.
type Transition struct {
	At     time.Time `json:"at"`
	From   bool      `json:"from"`
	To     bool      `json:"to"`
	Reason string    `json:"reason"`
}

// ErrorRecord is the last error. Message is stored only in the private
// ledger and in logs; the public status carries the category alone.
type ErrorRecord struct {
	At       time.Time `json:"at"`
	Category string    `json:"category"`
	Message  string    `json:"message,omitempty"`
}

// State is the private ownership ledger.
type State struct {
	SchemaVersion int       `json:"schema_version"`
	InstallID     string    `json:"install_id"`
	InstalledAt   time.Time `json:"installed_at"`
	ToolVersion   string    `json:"tool_version"`

	// SleepBaseline is the disablesleep value observed before the first
	// mutation. WakeBaseline is the recurring schedule observed at install.
	SleepBaseline *SleepRecord `json:"sleep_baseline"`
	WakeBaseline  *WakeRecord  `json:"wake_baseline"`

	// LastSleepWritten and LastWakeWritten are the values most recently
	// written and verified by schlaflos. Nil means schlaflos never wrote the
	// setting.
	LastSleepWritten *SleepRecord `json:"last_sleep_written,omitempty"`
	LastWakeWritten  *WakeRecord  `json:"last_wake_written,omitempty"`

	// Pending writes are persisted immediately before a mutation so that an
	// interrupted process can be resolved on the next run.
	PendingSleepWrite *SleepRecord `json:"pending_sleep_write,omitempty"`
	PendingWakeWrite  *WakeRecord  `json:"pending_wake_write,omitempty"`

	LastTransition *Transition  `json:"last_transition,omitempty"`
	LastError      *ErrorRecord `json:"last_error,omitempty"`
	EmergencyOffAt *time.Time   `json:"emergency_off_at,omitempty"`
}

// New creates a fresh ledger.
func New(now time.Time, toolVersion string) (*State, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	return &State{
		SchemaVersion: SchemaVersion,
		InstallID:     hex.EncodeToString(b[:]),
		InstalledAt:   now.UTC(),
		ToolVersion:   toolVersion,
	}, nil
}

// Validate checks structural invariants of a loaded ledger.
func (s *State) Validate() error {
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("state: schema version %d is not supported (this build supports %d)", s.SchemaVersion, SchemaVersion)
	}
	if s.InstallID == "" {
		return errors.New("state: install_id is missing")
	}
	if s.SleepBaseline == nil {
		return errors.New("state: sleep_baseline is missing")
	}
	if s.WakeBaseline == nil {
		return errors.New("state: wake_baseline is missing")
	}
	return nil
}

// Status is the redacted public projection.
type Status struct {
	SchemaVersion       int          `json:"schema_version"`
	GeneratedAt         time.Time    `json:"generated_at"`
	Mode                string       `json:"mode"`
	PollIntervalSeconds int          `json:"poll_interval_seconds,omitempty"`
	Requested           bool         `json:"requested"`
	Observed            *bool        `json:"observed"`
	Reason              string       `json:"reason"`
	PowerSource         string       `json:"power_source"`
	InsideWindow        bool         `json:"inside_window"`
	ActiveGuards        []string     `json:"active_guards"`
	NextTransition      *time.Time   `json:"next_transition,omitempty"`
	Degraded            []string     `json:"degraded,omitempty"`
	Conflicts           []string     `json:"conflicts,omitempty"`
	LastTransition      *Transition  `json:"last_transition,omitempty"`
	LastError           *StatusError `json:"last_error,omitempty"`
	WakeInstalled       *string      `json:"wake_installed"`
	WakeObserved        *string      `json:"wake_observed"`
	WakeEventsScheduled int          `json:"wake_events_scheduled"`
	NextWakeEvent       *time.Time   `json:"next_wake_event,omitempty"`
	RunOK               bool         `json:"run_ok"`
}

// StatusError is the sanitised error projection.
type StatusError struct {
	At       time.Time `json:"at"`
	Category string    `json:"category"`
}

// Modes recorded in the status projection.
const (
	ModeReconcile    = "reconcile"
	ModeDryRun       = "dry_run"
	ModeInstall      = "install"
	ModeEmergencyOff = "emergency_off"
	ModeUninstall    = "uninstall"
)

// PublicError projects a ledger error for status.
func PublicError(e *ErrorRecord) *StatusError {
	if e == nil {
		return nil
	}
	return &StatusError{At: e.At, Category: e.Category}
}

// Store reads and writes the ledger and status through the safe filesystem
// layer.
type Store struct {
	Layout layout.Layout
	FS     safefs.Checker
}

// LoadState reads and validates the private ledger.
func (s Store) LoadState() (*State, error) {
	exists, err := safefs.Exists(s.Layout.StatePath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, ErrNoState
	}
	if err := s.FS.VerifyFile(s.Layout.StatePath, layout.ModeState); err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	data, err := s.FS.ReadFile(s.Layout.StatePath, safefs.MaxFileSize)
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}
	var st State
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&st); err != nil {
		return nil, fmt.Errorf("state: corrupt ledger: %w", err)
	}
	if err := st.Validate(); err != nil {
		return nil, err
	}
	return &st, nil
}

// SaveState atomically writes the private ledger.
func (s Store) SaveState(st *State) error {
	if err := st.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return s.FS.WriteFile(s.Layout.StatePath, append(data, '\n'), layout.ModeState)
}

// Encode returns the canonical JSON of a ledger, used to detect changes.
func Encode(st *State) []byte {
	data, _ := json.Marshal(st)
	return data
}

// WriteStatus atomically writes the public projection.
func (s Store) WriteStatus(st *Status) error {
	st.SchemaVersion = SchemaVersion
	if st.ActiveGuards == nil {
		st.ActiveGuards = []string{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return s.FS.WriteFile(s.Layout.StatusPath, append(data, '\n'), layout.ModeStatus)
}

// ReadStatus reads the public projection. It validates that the file is a
// bounded regular file with the expected ownership and never follows a
// symbolic link. It works for unprivileged users.
func (s Store) ReadStatus() (*Status, error) {
	exists, err := safefs.Exists(s.Layout.StatusPath)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fs.ErrNotExist
	}
	if err := s.FS.VerifyFile(s.Layout.StatusPath, layout.ModeStatus); err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	data, err := s.FS.ReadFile(s.Layout.StatusPath, safefs.MaxFileSize)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	var st Status
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("status: corrupt file: %w", err)
	}
	if st.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("status: schema version %d is not supported", st.SchemaVersion)
	}
	return &st, nil
}
