// Command schlaflos keeps a macOS machine awake during configured windows or
// while selected workloads run, and manages a recurring pmset wake event.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nozomemein/schlaflos/internal/config"
	"github.com/nozomemein/schlaflos/internal/doctor"
	"github.com/nozomemein/schlaflos/internal/install"
	"github.com/nozomemein/schlaflos/internal/layout"
	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
	"github.com/nozomemein/schlaflos/internal/platform/macos/launchd"
	"github.com/nozomemein/schlaflos/internal/platform/macos/power"
	"github.com/nozomemein/schlaflos/internal/platform/macos/wake"
	"github.com/nozomemein/schlaflos/internal/processguard"
	"github.com/nozomemein/schlaflos/internal/reconcile"
	"github.com/nozomemein/schlaflos/internal/safefs"
	"github.com/nozomemein/schlaflos/internal/state"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

const usage = `schlaflos keeps a Mac awake during scheduled windows or while selected
workloads run, and manages a recurring pmset wake event.

Usage:
  schlaflos init
  schlaflos config check PATH
  schlaflos status [--json]
  schlaflos doctor
  sudo schlaflos install --config PATH [--replace-wake-schedule]
  sudo schlaflos config apply PATH [--replace-wake-schedule]
  sudo schlaflos emergency-off
  sudo schlaflos uninstall
  schlaflos version

Service command (invoked by launchd):
  sudo schlaflos reconcile [--dry-run]
`

type app struct {
	stdout  io.Writer
	stderr  io.Writer
	layout  layout.Layout
	fs      safefs.Checker
	runner  cmdrun.Runner
	isRoot  bool
	clock   func() time.Time
	exe     string
	version string
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	a := &app{
		stdout:  stdout,
		stderr:  stderr,
		layout:  layout.Default(),
		fs:      safefs.Production(),
		runner:  cmdrun.Exec{},
		isRoot:  os.Geteuid() == 0,
		clock:   time.Now,
		exe:     exe,
		version: version,
	}
	return a.dispatch(args)
}

func (a *app) dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, usage)
		return 2
	}
	var err error
	switch args[0] {
	case "init":
		err = a.cmdInit(args[1:])
	case "config":
		err = a.cmdConfig(args[1:])
	case "status":
		err = a.cmdStatus(args[1:])
	case "doctor":
		return a.cmdDoctor(args[1:])
	case "install":
		err = a.cmdInstall(args[1:])
	case "emergency-off":
		err = a.cmdEmergencyOff(args[1:])
	case "uninstall":
		err = a.cmdUninstall(args[1:])
	case "reconcile":
		return a.cmdReconcile(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintf(a.stdout, "schlaflos %s\n", a.version)
		return 0
	case "help", "--help", "-h":
		fmt.Fprint(a.stdout, usage)
		return 0
	default:
		fmt.Fprintf(a.stderr, "schlaflos: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
	if err != nil {
		fmt.Fprintf(a.stderr, "schlaflos: %v\n", err)
		return 1
	}
	return 0
}

func (a *app) requireRoot(cmd string) error {
	if !a.isRoot {
		return fmt.Errorf("%s requires root; run it with sudo", cmd)
	}
	return nil
}

func (a *app) store() state.Store {
	return state.Store{Layout: a.layout, FS: a.fs}
}

func (a *app) powerAdapter() power.Adapter { return power.Adapter{Runner: a.runner} }
func (a *app) wakeAdapter() wake.Adapter   { return wake.Adapter{Runner: a.runner} }
func (a *app) launchdManager() launchd.Manager {
	return launchd.Manager{Runner: a.runner}
}

func (a *app) installer() *install.Installer {
	return &install.Installer{
		Layout:       a.layout,
		FS:           a.fs,
		Store:        a.store(),
		Clock:        a.clock,
		Power:        a.powerAdapter(),
		Wake:         a.wakeAdapter(),
		Launchd:      a.launchdManager(),
		Log:          log.New(a.stdout, "", 0),
		ToolVersion:  a.version,
		SourceBinary: a.exe,
	}
}

func parseFlags(name string, args []string, define func(fs *flag.FlagSet)) (*flag.FlagSet, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if define != nil {
		define(fs)
	}
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return fs, nil
}

func (a *app) cmdInit(args []string) error {
	fs, err := parseFlags("init", args, nil)
	if err != nil {
		return err
	}
	path := "schlaflos.toml"
	if fs.NArg() > 1 {
		return errors.New("init accepts at most one PATH")
	}
	if fs.NArg() == 1 {
		path = fs.Arg(0)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists; refusing to overwrite it", path)
	}
	if err := os.WriteFile(path, []byte(config.Example), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "wrote example configuration to %s\n", path)
	return nil
}

func (a *app) cmdConfig(args []string) error {
	if len(args) == 0 {
		return errors.New("config requires a subcommand: check PATH | apply PATH")
	}
	switch args[0] {
	case "check":
		fs, err := parseFlags("config check", args[1:], nil)
		if err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("config check requires exactly one PATH")
		}
		cfg, err := config.Load(fs.Arg(0))
		if err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "%s: valid\n", fs.Arg(0))
		a.describeConfig(cfg)
		return nil
	case "apply":
		if err := a.requireRoot("config apply"); err != nil {
			return err
		}
		var replace bool
		fs, err := parseFlags("config apply", args[1:], func(fs *flag.FlagSet) {
			fs.BoolVar(&replace, "replace-wake-schedule", false, "replace an unrelated recurring pmset schedule")
		})
		if err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("config apply requires exactly one PATH")
		}
		return a.installer().Apply(context.Background(), fs.Arg(0), replace)
	default:
		return fmt.Errorf("unknown config subcommand %q", args[0])
	}
}

func (a *app) describeConfig(cfg *config.Config) {
	fmt.Fprintf(a.stdout, "  poll interval:  %s\n", cfg.PollInterval)
	fmt.Fprintf(a.stdout, "  require AC:     %v\n", cfg.Power.RequireAC)
	for _, w := range cfg.Windows {
		fmt.Fprintf(a.stdout, "  window:         %s %s-%s%s\n", dayList(w.Days), w.Start, w.End, ternary(w.CrossesMidnight(), " (crosses midnight)", ""))
	}
	if cfg.Wake.Enabled {
		fmt.Fprintf(a.stdout, "  wake:           %s\n", wake.FromConfig(cfg.Wake))
	} else {
		fmt.Fprintf(a.stdout, "  wake:           disabled\n")
	}
	for _, g := range cfg.Guards.Process {
		fmt.Fprintf(a.stdout, "  process guard:  %s -> %s\n", g.Name, g.Executable)
	}
}

func dayList(days []time.Weekday) string {
	names := make([]string, 0, len(days))
	for _, d := range days {
		names = append(names, config.DayName(d))
	}
	return strings.Join(names, ",")
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func (a *app) cmdStatus(args []string) error {
	var asJSON bool
	fs, err := parseFlags("status", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&asJSON, "json", false, "print the raw status projection")
	})
	if err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("status accepts no arguments")
	}
	st, err := a.store().ReadStatus()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no status found at %s; schlaflos is not installed or has not reconciled yet", a.layout.StatusPath)
		}
		return err
	}
	if asJSON {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	printStatus(a.stdout, st)
	return nil
}

func printStatus(w io.Writer, st *state.Status) {
	fmt.Fprintf(w, "schlaflos status (%s, %s)\n", st.GeneratedAt.Local().Format(time.RFC3339), st.Mode)
	request := "normal sleep"
	if st.Requested {
		request = "inhibit sleep"
	}
	fmt.Fprintf(w, "  request:          %s (%s)\n", request, st.Reason)
	observed := "unknown"
	if st.Observed != nil {
		observed = fmt.Sprintf("disablesleep=%d", b2i(*st.Observed))
	}
	fmt.Fprintf(w, "  observed:         %s\n", observed)
	fmt.Fprintf(w, "  power:            %s\n", orDash(st.PowerSource))
	fmt.Fprintf(w, "  inside window:    %s\n", yesNo(st.InsideWindow))
	fmt.Fprintf(w, "  active guards:    %s\n", orNone(strings.Join(st.ActiveGuards, ", ")))
	next := "none"
	if st.NextTransition != nil {
		next = st.NextTransition.Local().Format(time.RFC3339)
	}
	fmt.Fprintf(w, "  next transition:  %s\n", next)
	fmt.Fprintf(w, "  wake installed:   %s\n", orNone(deref(st.WakeInstalled)))
	fmt.Fprintf(w, "  wake observed:    %s\n", orDash(deref(st.WakeObserved)))
	last := "none"
	if st.LastTransition != nil {
		last = fmt.Sprintf("%s disablesleep %d -> %d (%s)", st.LastTransition.At.Local().Format(time.RFC3339), b2i(st.LastTransition.From), b2i(st.LastTransition.To), st.LastTransition.Reason)
	}
	fmt.Fprintf(w, "  last transition:  %s\n", last)
	lastErr := "none"
	if st.LastError != nil {
		lastErr = fmt.Sprintf("%s at %s", st.LastError.Category, st.LastError.At.Local().Format(time.RFC3339))
	}
	fmt.Fprintf(w, "  last error:       %s\n", lastErr)
	fmt.Fprintf(w, "  conflicts:        %s\n", orNone(strings.Join(st.Conflicts, ", ")))
	fmt.Fprintf(w, "  degraded:         %s\n", orNone(strings.Join(st.Degraded, ", ")))
	fmt.Fprintf(w, "  last run:         %s\n", ternary(st.RunOK, "ok", "failed"))
}

func (a *app) cmdDoctor(args []string) int {
	if _, err := parseFlags("doctor", args, nil); err != nil {
		fmt.Fprintf(a.stderr, "schlaflos: %v\n", err)
		return 2
	}
	d := doctor.Doctor{
		Layout:  a.layout,
		FS:      a.fs,
		Store:   a.store(),
		Power:   a.powerAdapter(),
		Wake:    a.wakeAdapter(),
		Launchd: a.launchdManager(),
		IsRoot:  a.isRoot,
		Clock:   a.clock,
	}
	checks, failed := d.Run(context.Background())
	for _, c := range checks {
		fmt.Fprintf(a.stdout, "[%-4s] %s: %s\n", c.Level, c.Name, c.Detail)
	}
	if failed {
		fmt.Fprintln(a.stdout, "doctor: problems found")
		return 1
	}
	fmt.Fprintln(a.stdout, "doctor: ok")
	return 0
}

func (a *app) cmdInstall(args []string) error {
	if err := a.requireRoot("install"); err != nil {
		return err
	}
	var cfgPath string
	var replace bool
	fs, err := parseFlags("install", args, func(fs *flag.FlagSet) {
		fs.StringVar(&cfgPath, "config", "", "configuration file to install")
		fs.BoolVar(&replace, "replace-wake-schedule", false, "replace an unrelated recurring pmset schedule")
	})
	if err != nil {
		return err
	}
	if cfgPath == "" || fs.NArg() != 0 {
		return errors.New("install requires --config PATH")
	}
	if err := a.installer().Install(context.Background(), cfgPath, replace); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "installed; run `schlaflos status` to inspect the result\n")
	return nil
}

func (a *app) cmdEmergencyOff(args []string) error {
	if err := a.requireRoot("emergency-off"); err != nil {
		return err
	}
	if fs, err := parseFlags("emergency-off", args, nil); err != nil || fs.NArg() != 0 {
		return errors.New("emergency-off accepts no arguments")
	}
	return a.installer().EmergencyOff(context.Background())
}

func (a *app) cmdUninstall(args []string) error {
	if err := a.requireRoot("uninstall"); err != nil {
		return err
	}
	if fs, err := parseFlags("uninstall", args, nil); err != nil || fs.NArg() != 0 {
		return errors.New("uninstall accepts no arguments")
	}
	return a.installer().Uninstall(context.Background())
}

func (a *app) cmdReconcile(args []string) int {
	logger := log.New(a.stderr, a.clock().Format(time.RFC3339)+" ", 0)
	var dryRun bool
	fs, err := parseFlags("reconcile", args, func(fs *flag.FlagSet) {
		fs.BoolVar(&dryRun, "dry-run", false, "evaluate and report without mutating anything")
	})
	if err != nil || fs.NArg() != 0 {
		logger.Printf("reconcile accepts only --dry-run")
		return 2
	}
	if err := a.requireRoot("reconcile"); err != nil {
		logger.Print(err)
		return 1
	}
	deps := reconcile.Deps{
		Layout: a.layout,
		FS:     a.fs,
		Store:  a.store(),
		Clock:  a.clock,
		Power:  a.powerAdapter(),
		Procs:  processguard.PS{Runner: a.runner},
		Wake:   a.wakeAdapter(),
		Log:    logger,
		DryRun: dryRun,
	}
	status, err := reconcile.Run(context.Background(), deps)
	if errors.Is(err, reconcile.ErrLocked) {
		return 0
	}
	if dryRun && status != nil {
		printStatus(a.stdout, status)
	}
	if err != nil {
		logger.Printf("reconcile finished with error: %v", err)
		return 1
	}
	return 0
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
