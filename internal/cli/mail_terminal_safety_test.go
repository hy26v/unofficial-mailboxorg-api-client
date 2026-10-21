// ABOUTME: Tests that mail human output escapes terminal controls in headers and file names.
// ABOUTME: Covers C1 controls and Unicode bidi controls in mail read, get, list, and attachment output.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// unsafeHeaderText holds a C1 control (NEL, U+0085; CSI, U+009B), a bidi mark (U+200F), a
// bidi override (U+202E), a bidi isolate (U+2066), and an escape (U+001B).
const unsafeHeaderText = "Invoice\u0085\u009b2J\u200fevil\u202etxt.exe\u2066\x1b[31m"

var terminalUnsafeRunes = []rune{0x85, 0x9b, 0x200e, 0x200f, 0x202a, 0x202e, 0x2066, 0x2069, 0x1b}

func assertTerminalSafe(t *testing.T, output string) {
	t.Helper()
	for _, r := range terminalUnsafeRunes {
		if strings.ContainsRune(output, r) {
			t.Errorf("human output kept terminal control U+%04X: %q", r, output)
		}
	}
}

func TestSafeMultilineText_EscapesC1AndBidiControls(t *testing.T) {
	got := render.SafeMultilineText("a\u0085b\u009bc\u200ed\u200fe\u202af\u2069g\nh\ti\rj")
	want := `a\u0085b\u009bc\u200ed\u200fe\u202af\u2069g` + "\nh\tij"
	if got != want {
		t.Errorf("SafeMultilineText = %q, want %q", got, want)
	}
}

func TestMailRead_HumanOutputEscapesHeadersAndFileNames(t *testing.T) {
	message := mailReadMessage(plainBodyPart, map[string]any{
		"id": "2", "disp": "attachment", "content_type": "application/pdf", "filename": "report\u202efdp.exe", "size": 10,
	})
	message["subject"] = unsafeHeaderText
	message["from"] = [][]any{{"Billing", "billing\u200f@example.org"}}
	message["cc"] = [][]any{{nil, "copy\u0085@example.org"}}
	srv := newMailReadServer(t, message)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	assertTerminalSafe(t, stdout)
	for _, want := range []string{"Invoice", "u202etxt.exe", "report", "u202efdp.exe", "billing", "copy"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q: %s", want, stdout)
		}
	}
	if strings.Contains(stdout, `\\u202e`) {
		t.Errorf("human output escaped a control twice: %s", stdout)
	}
}

func TestMailGet_HumanOutputEscapesHeaders(t *testing.T) {
	row := safeMailRow("7", "default0/INBOX")
	row[2] = unsafeHeaderText
	row[3] = []any{[]any{"Sender", "sender\u202e@example.org"}}
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{Body: map[string]any{"data": []any{row}}})

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "get", "--id", "7", "--folder", "default0/INBOX"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	assertTerminalSafe(t, stdout)
	if !strings.Contains(stdout, "Invoice") || !strings.Contains(stdout, "sender") {
		t.Errorf("human output lost the header text: %s", stdout)
	}
}

func TestMailList_HumanOutputEscapesHeaders(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{Body: map[string]any{"data": []any{
		[]any{"7", unsafeHeaderText, []any{[]any{"Sender", "sender\u2066@example.org"}}, int64(1784851574000), []any{[]any{nil, "to\u009b@example.org"}}},
	}}})

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "list", "--fields", "to"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	assertTerminalSafe(t, stdout)
	if !strings.Contains(stdout, "Invoice") || !strings.Contains(stdout, "sender") || !strings.Contains(stdout, "to") {
		t.Errorf("human output lost the header text: %s", stdout)
	}
}

func TestMailAttachment_HumanOutputEscapesFileName(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart, map[string]any{
		"id": "2", "disp": "attachment", "content_type": "application/pdf\u202e", "filename": "report\u202efdp.exe", "size": 10,
	}))
	defer srv.Close()
	srv.Handle("/ajax/mail", "attachment", testutil.Response{Body: []byte("%PDF")})
	saveTo := filepath.Join(t.TempDir(), "report.pdf")

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2", "--save-to", saveTo})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	assertTerminalSafe(t, stdout)
	if _, err := os.Stat(saveTo); err != nil {
		t.Errorf("attachment was not saved: %v", err)
	}
}
