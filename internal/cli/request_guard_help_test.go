// ABOUTME: Covers the user-facing contract for built-in request and login rate protection.
// ABOUTME: Ensures the help topic is local and exposes retry behavior without network access.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestRateLimitHelpDocumentsBuiltInGuardWithoutNetwork(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"help", "rate-limits"})
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q, want local success", code, stderr)
	}
	for _, want := range []string{
		"250 milliseconds",
		"four HTTP requests per second",
		"20 sub-requests",
		"each consumes one shared pacing slot",
		"2, 4, 8, 16, 32",
		"Retry-After",
		"LGI-0026",
		"retry_after_ms",
		"never automatically retries",
		"same MBX_HOME",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help missing %q:\n%s", want, stdout)
		}
	}
	if got := len(srv.Requests()); got != 0 {
		t.Errorf("network requests = %d, want 0", got)
	}
}
