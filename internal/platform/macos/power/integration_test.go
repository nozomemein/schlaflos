package power

import (
	"context"
	"os"
	"testing"

	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
)

// TestReadOnlyIntegration runs the read-only pmset queries against the real
// machine. It never mutates anything and is opt-in so that shared CI runners
// are not depended upon.
func TestReadOnlyIntegration(t *testing.T) {
	if os.Getenv("SCHLAFLOS_READONLY_INTEGRATION") == "" {
		t.Skip("set SCHLAFLOS_READONLY_INTEGRATION=1 to query the local pmset")
	}
	a := Adapter{Runner: cmdrun.Exec{}}
	src, err := a.Source(context.Background())
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	disabled, err := a.SleepDisabled(context.Background())
	if err != nil {
		t.Fatalf("SleepDisabled: %v", err)
	}
	t.Logf("source=%s disablesleep=%v", src, disabled)
}
