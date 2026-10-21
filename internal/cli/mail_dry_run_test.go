// ABOUTME: Tests that the mail send/draft previews name every recipient and attachment the
// ABOUTME: real request would carry, since sending is the one mutation nothing can undo.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// A dry run exists so a caller can check an irreversible action before taking it. For mail
// send that means checking who receives the message. The preview reported only --to, so the
// blind-copy list - the one recipient set the delivered message does not reveal, and the one
// a caller most needs to confirm - was invisible, as were the files being attached.
func TestMailSendDryRun_NamesEveryRecipientAndAttachment(t *testing.T) {
	dir := t.TempDir()
	attachment := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(attachment, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatalf("writing attachment fixture: %v", err)
	}

	srv := testutil.NewServer()
	defer srv.Close()
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	args := []string{
		"mail", "send",
		"--to", "to@example.org",
		"--cc", "cc@example.org",
		"--bcc", "bcc@example.org",
		"--subject", "Quarterly report",
		"--body", "Attached.",
		"--attach", attachment,
		"--dry-run",
	}

	for _, mode := range []struct {
		name string
		args []string
	}{
		{"human", args},
		{"llm", append(append([]string{}, args...), "--llm")},
	} {
		t.Run(mode.name, func(t *testing.T) {
			stdout, stderr, code := run(t, srv, env, mode.args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}
			for _, want := range []string{"cc@example.org", "bcc@example.org", "report.pdf", "to@example.org", "Quarterly report"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("dry run does not mention %q: %s", want, strings.TrimSpace(stdout))
				}
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("dry run made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}

// mail draft's preview named only the destination folder, so it confirmed nothing about the
// message that was about to be stored.
func TestMailDraftDryRun_NamesTheMessageItWouldStore(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	args := []string{
		"mail", "draft",
		"--to", "to@example.org",
		"--bcc", "bcc@example.org",
		"--subject", "Draft subject",
		"--body", "Body text.",
		"--dry-run",
	}

	for _, mode := range []struct {
		name string
		args []string
	}{
		{"human", args},
		{"llm", append(append([]string{}, args...), "--llm")},
	} {
		t.Run(mode.name, func(t *testing.T) {
			stdout, stderr, code := run(t, srv, env, mode.args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}
			for _, want := range []string{"to@example.org", "bcc@example.org", "Draft subject", "default0/Drafts"} {
				if !strings.Contains(stdout, want) {
					t.Errorf("dry run does not mention %q: %s", want, strings.TrimSpace(stdout))
				}
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("dry run made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}
