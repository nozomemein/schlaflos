package wake

import (
	"context"
	"os"
	"testing"

	"github.com/nozomemein/schlaflos/internal/platform/macos/cmdrun"
)

// TestReadOnlyIntegration reads the real recurring schedule. It never
// mutates anything and is opt-in.
func TestReadOnlyIntegration(t *testing.T) {
	if os.Getenv("SCHLAFLOS_READONLY_INTEGRATION") == "" {
		t.Skip("set SCHLAFLOS_READONLY_INTEGRATION=1 to query the local pmset")
	}
	s, err := Adapter{Runner: cmdrun.Exec{}}.Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	t.Logf("recurring schedule: %s (exact=%v)", s, s.Exact())
}
