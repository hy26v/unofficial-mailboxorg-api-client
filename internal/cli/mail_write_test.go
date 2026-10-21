// ABOUTME: CLI integration tests for mail.move, mail.copy, mail.flag, and mail.delete: request
// ABOUTME: mapping, mutation safety (dry-run, delete confirmation), and error handling.
package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestBuildDraftMIME_IncludesHeadersAndCRLFBody(t *testing.T) {
	msg := mailSendInput{
		To:      []string{"a@example.com", "b@example.com"},
		Cc:      []string{"c@example.com"},
		Subject: "hi there",
		Body:    "line one\nline two",
	}
	raw := string(buildDraftMIME(msg, nil))

	if !strings.Contains(raw, "To: a@example.com, b@example.com\r\n") {
		t.Errorf("raw = %q, want To header", raw)
	}
	if !strings.Contains(raw, "Cc: c@example.com\r\n") {
		t.Errorf("raw = %q, want Cc header", raw)
	}
	if !strings.Contains(raw, "Subject: hi there\r\n") {
		t.Errorf("raw = %q, want Subject header", raw)
	}
	if !strings.Contains(raw, "Content-Type: text/plain; charset=UTF-8\r\n") {
		t.Errorf("raw = %q, want Content-Type header", raw)
	}
	if !strings.HasSuffix(raw, "line one\r\nline two") {
		t.Errorf("raw = %q, want CRLF-joined body", raw)
	}
}

func TestBuildDraftMIME_EncodesNonASCIISubject(t *testing.T) {
	msg := mailSendInput{Subject: "Grüße"}
	raw := string(buildDraftMIME(msg, nil))

	if !strings.Contains(raw, "Subject: =?UTF-8?") {
		t.Errorf("raw = %q, want encoded-word subject", raw)
	}
}

func TestBuildDraftMIME_OmitsEmptyHeaders(t *testing.T) {
	msg := mailSendInput{Body: "just a body"}
	raw := string(buildDraftMIME(msg, nil))

	if strings.Contains(raw, "To:") || strings.Contains(raw, "Cc:") || strings.Contains(raw, "Bcc:") || strings.Contains(raw, "Subject:") {
		t.Errorf("raw = %q, want no empty header lines", raw)
	}
}

func TestMailCommands_RejectBlankRecipientEntries(t *testing.T) {
	tests := map[string][]string{
		"send":  {"mail", "send", "--to", "   ", "--subject", "hi", "--dry-run", "--llm"},
		"draft": {"mail", "draft", "--cc", "\t", "--subject", "hi", "--dry-run", "--llm"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code == 0 {
				t.Fatalf("exit code = 0, want blank recipient rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "blank address") {
				t.Fatalf("stderr = %q, want blank address error", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("blank recipient made HTTP requests: %+v", srv.Requests())
			}
		})
	}
}

func TestMailDelete_MissingConfirm_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "confirm") {
		t.Errorf("stderr = %q, want mention of confirm", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailDelete_ConfirmMismatch_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--confirm", "8", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "confirm") {
		t.Errorf("stderr = %q, want mention of confirm", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailDelete_ConfirmMatches_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--confirm", "7", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != "{\"deleted\":true,\"id\":\"7\"}\n" {
		t.Errorf("stdout = %q, want exact delete result", stdout)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want login + delete", len(reqs))
	}
	req := reqs[1]
	query := req.URL.Query()
	if req.Method != "PUT" || req.URL.Path != "/ajax/mail" || len(query) != 2 ||
		query.Get("action") != "delete" || query.Get("session") != "sess-1" {
		t.Errorf("delete request = %s %s, want exact mail delete query", req.Method, req.URL)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read delete request body: %v", err)
	}
	if string(body) != `[{"folder":"default0/INBOX","id":"7"}]` {
		t.Errorf("delete request body = %q, want exact folder/id array", body)
	}
}

func TestMailDelete_APIReportsFailedIDs_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "delete", testutil.Response{Body: map[string]any{"data": []any{"7"}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--confirm", "7", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if stderr == "" {
		t.Errorf("stderr should describe the failure")
	}
}

func TestMailDelete_RejectsNullResult(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "delete", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--confirm", "7", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailDelete_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--confirm", "7", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailDelete_MissingID_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "delete", "--folder", "default0/INBOX", "--confirm", "7", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "id") {
		t.Errorf("stderr = %q, want mention of id", stderr)
	}
}

func TestMailDelete_RejectsBlankIdentifiersBeforeNetwork(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantFlag string
	}{
		{
			name:     "message id",
			args:     []string{"mail", "delete", "--id", " \t", "--folder", "default0/INBOX", "--confirm", " \t", "--llm"},
			wantFlag: "--id",
		},
		{
			name:     "folder id",
			args:     []string{"mail", "delete", "--id", "7", "--folder", " \t", "--confirm", "7", "--llm"},
			wantFlag: "--folder",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/mail", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, tt.args)

			if code == 0 {
				t.Fatalf("exit code = 0, want blank identifier rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_argument") || !strings.Contains(stderr, tt.wantFlag) {
				t.Fatalf("stderr = %q, want invalid_argument mentioning %s", stderr, tt.wantFlag)
			}
			if got := len(srv.Requests()); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
		})
	}
}

func TestMailWriteCommands_RejectBlankLocationIdentifiersBeforeNetwork(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantFlag string
	}{
		{name: "draft folder", args: []string{"mail", "draft", "--subject", "fixture", "--folder", " \t", "--llm"}, wantFlag: "--folder"},
		{name: "move id", args: []string{"mail", "move", "--id", " \t", "--folder", "default0/Drafts", "--to", "default0/Trash", "--llm"}, wantFlag: "--id"},
		{name: "move folder", args: []string{"mail", "move", "--id", "7", "--folder", " \t", "--to", "default0/Trash", "--llm"}, wantFlag: "--folder"},
		{name: "move destination", args: []string{"mail", "move", "--id", "7", "--folder", "default0/Drafts", "--to", " \t", "--llm"}, wantFlag: "--to"},
		{name: "copy id", args: []string{"mail", "copy", "--id", " \t", "--folder", "default0/Drafts", "--to", "default0/Trash", "--llm"}, wantFlag: "--id"},
		{name: "copy folder", args: []string{"mail", "copy", "--id", "7", "--folder", " \t", "--to", "default0/Trash", "--llm"}, wantFlag: "--folder"},
		{name: "copy destination", args: []string{"mail", "copy", "--id", "7", "--folder", "default0/Drafts", "--to", " \t", "--llm"}, wantFlag: "--to"},
		{name: "flag id", args: []string{"mail", "flag", "--id", " \t", "--folder", "default0/Drafts", "--seen", "true", "--llm"}, wantFlag: "--id"},
		{name: "flag folder", args: []string{"mail", "flag", "--id", "7", "--folder", " \t", "--seen", "true", "--llm"}, wantFlag: "--folder"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, tt.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want blank location rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_argument") || !strings.Contains(stderr, tt.wantFlag) {
				t.Fatalf("stderr = %q, want invalid_argument mentioning %s", stderr, tt.wantFlag)
			}
			if got := len(srv.Requests()); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
		})
	}
}

func TestMailMove_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"folder_id": "default0/Trash", "id": "17"}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Trash", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	want := "{\"source\":{\"folder\":\"default0/INBOX\",\"id\":\"7\"},\"destination\":{\"folder\":\"default0/Trash\",\"id\":\"17\"},\"verified\":false}\n"
	if stdout != want {
		t.Errorf("stdout = %q, want stable source/destination identity %q", stdout, want)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want login + move", len(reqs))
	}
	req := reqs[1]
	query := req.URL.Query()
	if req.Method != "PUT" || req.URL.Path != "/ajax/mail" || len(query) != 4 ||
		query.Get("action") != "update" || query.Get("session") != "sess-1" ||
		query.Get("id") != "7" || query.Get("folder") != "default0/INBOX" {
		t.Errorf("move request = %s %s, want exact mail update query", req.Method, req.URL)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read move request body: %v", err)
	}
	if string(body) != `{"folder_id":"default0/Trash"}` {
		t.Errorf("move request body = %q, want exact destination", body)
	}
}

func TestMailMove_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Trash", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailMove_MissingTo_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "to") {
		t.Errorf("stderr = %q, want mention of --to", stderr)
	}
}

func TestMailCopy_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "copy", testutil.Response{Body: map[string]any{"data": map[string]any{"folder_id": "default0/Drafts", "id": "42"}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "copy", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Drafts", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != "{\"id\":\"42\",\"folder\":\"default0/Drafts\"}\n" {
		t.Errorf("stdout = %q, want exact new id/folder", stdout)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want login + copy", len(reqs))
	}
	req := reqs[1]
	query := req.URL.Query()
	if req.Method != "PUT" || req.URL.Path != "/ajax/mail" || len(query) != 4 ||
		query.Get("action") != "copy" || query.Get("session") != "sess-1" ||
		query.Get("id") != "7" || query.Get("folder") != "default0/INBOX" {
		t.Errorf("copy request = %s %s, want exact mail copy query", req.Method, req.URL)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read copy request body: %v", err)
	}
	if string(body) != `{"folder_id":"default0/Drafts"}` {
		t.Errorf("copy request body = %q, want exact destination", body)
	}
}

func TestMailCopy_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "copy", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Drafts", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailCopy_MissingTo_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "copy", "--id", "7", "--folder", "default0/INBOX", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "to") {
		t.Errorf("stderr = %q, want mention of --to", stderr)
	}
}

func TestMailLocationMutation_RejectsMismatchedDestination(t *testing.T) {
	tests := []struct {
		name   string
		action string
	}{
		{name: "move", action: "move"},
		{name: "copy", action: "copy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/mail", map[string]string{"move": "update", "copy": "copy"}[tt.action], testutil.Response{
				Body: map[string]any{"data": map[string]any{"id": "42", "folder_id": "default0/Spam"}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"mail", tt.action, "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Archive", "--llm",
			})

			if code == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestDescribe_LLM_MailCopyIsWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.copy", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"effect":"write"`) {
		t.Errorf("stdout = %q, want effect write", stdout)
	}
}

func TestMailFlag_SetSeen_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"id": "7"}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "flag", "--id", "7", "--folder", "default0/INBOX", "--seen", "true", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"updated":true`) {
		t.Errorf("stdout = %q, want updated:true", stdout)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 { // login + update
		t.Fatalf("expected 2 requests (login, update), got %d: %v", len(reqs), reqs)
	}
	upd := reqs[1]
	if upd.URL.Query().Get("action") != "update" || upd.Method != "PUT" {
		t.Errorf("expected PUT update request, got %s %s", upd.Method, upd.URL)
	}
}

func TestMailFlag_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"id": "8"}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "flag", "--id", "7", "--folder", "default0/INBOX", "--seen", "true", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want mismatched response rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailFlag_SetSeenAndFlagged_TwoUpdateRequests(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"id": "7"}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "flag", "--id", "7", "--folder", "default0/INBOX", "--seen", "true", "--flagged", "false", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if stdout != "{\"updated\":true}\n" {
		t.Fatalf("stdout = %q, want exact compact update result", stdout)
	}

	reqs := srv.Requests()
	if len(reqs) != 3 { // login + 2 updates
		t.Fatalf("expected 3 requests (login + 2 updates), got %d: %v", len(reqs), reqs)
	}
	wantBodies := []string{`{"flags":32,"value":true}`, `{"flags":8,"value":false}`}
	for i, wantBody := range wantBodies {
		req := reqs[i+1]
		query := req.URL.Query()
		if req.Method != "PUT" || req.URL.Path != "/ajax/mail" || len(query) != 4 ||
			query.Get("action") != "update" || query.Get("session") != "sess-1" ||
			query.Get("id") != "7" || query.Get("folder") != "default0/INBOX" {
			t.Errorf("update request %d = %s %s, want exact mail update query", i+1, req.Method, req.URL)
		}
		if req.Header.Get("Content-Type") != "application/json" {
			t.Errorf("update request %d Content-Type = %q, want application/json", i+1, req.Header.Get("Content-Type"))
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("read update request %d body: %v", i+1, err)
		}
		if string(body) != wantBody {
			t.Errorf("update request %d body = %q, want %q", i+1, body, wantBody)
		}
	}
}

func TestMailFlag_NoFlagsGiven_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "flag", "--id", "7", "--folder", "default0/INBOX", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "seen") && !strings.Contains(stderr, "flagged") {
		t.Errorf("stderr = %q, want mention of --seen or --flagged", stderr)
	}
}

func TestMailFlag_InvalidBoolValue_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "flag", "--id", "7", "--folder", "default0/INBOX", "--seen", "yes", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "seen") {
		t.Errorf("stderr = %q, want mention of --seen", stderr)
	}
}

func TestMailFlag_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "flag", "--id", "7", "--folder", "default0/INBOX", "--seen", "true", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestDescribe_LLM_MailDeleteIsDestructive(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.delete", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"effect":"destructive"`) {
		t.Errorf("stdout = %q, want effect destructive", stdout)
	}
}

func TestDescribe_LLM_MailMoveIsWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.move", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"effect":"write"`) {
		t.Errorf("stdout = %q, want effect write", stdout)
	}
}

func TestDescribe_LLM_MailSendIsWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.send", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"effect":"write"`) {
		t.Errorf("stdout = %q, want effect write", stdout)
	}
}

func TestMailSend_MissingTo_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "send", "--subject", "hi", "--body", "hello", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--to") {
		t.Errorf("stderr = %q, want mention of --to", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailSend_MissingSubject_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "send", "--to", "fixture-user@example.invalid", "--body", "hello", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--subject") {
		t.Errorf("stderr = %q, want mention of --subject", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailSend_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	attachPath := filepath.Join(t.TempDir(), "payload.mbxbin")
	if err := os.WriteFile(attachPath, []byte("attachment contents"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{
			"mail", "send",
			"--to", "fixture-user@example.invalid",
			"--cc", "cc@example.org",
			"--bcc", "bcc@example.org",
			"--subject", "cli-test send",
			"--body", "hello body",
			"--attach", attachPath,
			"--dry-run", "--llm",
		})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := "{\"dry_run\":true,\"to\":[\"fixture-user@example.invalid\"],\"cc\":[\"cc@example.org\"],\"bcc\":[\"bcc@example.org\"],\"subject\":\"cli-test send\",\"attachments\":[\"payload.mbxbin\"]}\n"
	if stdout != want {
		t.Errorf("stdout = %q, want exact irreversible-send preview %q", stdout, want)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailSend_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: []byte(`<script>(parent["callback_new"])({"data":"default0/INBOX/999"})</script>`),
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "send", "--to", "fixture-user@example.invalid", "--subject", "hi", "--body", "hello", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"sent":true`) {
		t.Errorf("stdout = %q, want sent:true", stdout)
	}

	req := srv.Requests()[1]
	if req.Method != "POST" {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if !strings.Contains(req.Header.Get("Content-Type"), "multipart/form-data") {
		t.Errorf("content type = %q, want multipart/form-data", req.Header.Get("Content-Type"))
	}
}

func TestMailSend_WithAttachment_SendsFilePart(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: []byte(`<script>(parent["callback_new"])({"data":"default0/INBOX/999"})</script>`),
	})

	dir := t.TempDir()
	attachPath := dir + "/payload.mbxbin"
	if err := os.WriteFile(attachPath, []byte("attachment contents"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{
			"mail", "send",
			"--to", "fixture-user@example.invalid",
			"--cc", "cc@example.org",
			"--bcc", "bcc@example.org",
			"--subject", "cli-test send",
			"--body", "hello body",
			"--attach", attachPath,
			"--llm",
		})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	wantOutput := "{\"sent\":true,\"id\":\"default0/INBOX/999\",\"to\":[\"fixture-user@example.invalid\"],\"subject\":\"cli-test send\"}\n"
	if stdout != wantOutput {
		t.Errorf("stdout = %q, want exact send result %q", stdout, wantOutput)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want login + send", len(reqs))
	}
	req := reqs[1]
	query := req.URL.Query()
	if req.Method != "POST" || req.URL.Path != "/ajax/mail" || len(query) != 2 ||
		query.Get("action") != "new" || query.Get("session") != "sess-1" {
		t.Errorf("send request = %s %s, want exact mail new query", req.Method, req.URL)
	}
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
		t.Errorf("Content-Type = %q, want multipart/form-data boundary", req.Header.Get("Content-Type"))
	}
	mr, err := req.MultipartReader()
	if err != nil {
		t.Fatalf("MultipartReader failed: %v", err)
	}
	var partNames []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		partNames = append(partNames, part.FormName())
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read %s part: %v", part.FormName(), err)
		}
		switch part.FormName() {
		case "json_0":
			if part.FileName() != "" || part.Header.Get("Content-Type") != "application/json" {
				t.Errorf("json_0 headers = filename %q, Content-Type %q", part.FileName(), part.Header.Get("Content-Type"))
			}
			wantMeta := `{"attachments":[{"content":"hello body","content_type":"text/plain","disp":"inline"},{"content_type":"application/octet-stream","disp":"attachment","filename":"payload.mbxbin","id":"file_0"}],"bcc":[[null,"bcc@example.org"]],"cc":[[null,"cc@example.org"]],"subject":"cli-test send","to":[[null,"fixture-user@example.invalid"]]}`
			if string(body) != wantMeta {
				t.Errorf("json_0 = %s, want exact metadata %s", body, wantMeta)
			}
		case "file_0":
			if string(body) != "attachment contents" {
				t.Errorf("file_0 = %q, want attachment contents", body)
			}
			if part.FileName() != "payload.mbxbin" {
				t.Errorf("filename = %q, want payload.mbxbin", part.FileName())
			}
			if part.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("file_0 Content-Type = %q, want application/octet-stream", part.Header.Get("Content-Type"))
			}
		default:
			t.Errorf("unexpected multipart part %q", part.FormName())
		}
	}
	if strings.Join(partNames, ",") != "json_0,file_0" {
		t.Errorf("multipart parts = %v, want exactly json_0 then file_0", partNames)
	}
}

func TestMailSend_AttachmentFileMissing_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "send", "--to", "fixture-user@example.invalid", "--subject", "hi", "--attach", "/nonexistent/path.txt", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--attach") {
		t.Errorf("stderr = %q, want mention of --attach", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailDraft_Success_HumanMode(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "default0/Drafts", "id": "42"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{
			"mail", "draft",
			"--to", "fixture-user@example.invalid",
			"--cc", "cc@example.org",
			"--bcc", "bcc@example.org",
			"--subject", "cli-test draft",
			"--body", "line one\nline two",
		})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "42") || !strings.Contains(stdout, "default0/Drafts") {
		t.Errorf("stdout = %q, want id and folder", stdout)
	}

	req := srv.Requests()[1]
	query := req.URL.Query()
	if req.Method != "PUT" || req.URL.Path != "/ajax/mail" || len(query) != 3 ||
		query.Get("action") != "new" || query.Get("session") != "sess-1" ||
		query.Get("folder") != "default0/Drafts" {
		t.Errorf("draft request = %s %s, want exact mail new query", req.Method, req.URL)
	}
	if req.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", req.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read draft request body: %v", err)
	}
	wantBody := "To: fixture-user@example.invalid\r\n" +
		"Cc: cc@example.org\r\n" +
		"Bcc: bcc@example.org\r\n" +
		"Subject: cli-test draft\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n" +
		"\r\n" +
		"line one\r\nline two"
	if string(body) != wantBody {
		t.Errorf("body = %q, want exact MIME payload %q", body, wantBody)
	}
}

func TestMailDraft_Success_LLMMode(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "default0/Drafts", "id": "42"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "draft", "--subject", "hi", "--body", "hello", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != `{"id":"42","folder":"default0/Drafts"}`+"\n" {
		t.Errorf("stdout = %q, want compact JSON with id and folder", stdout)
	}
}

func TestMailDraft_CustomFolder_UsesGivenFolder(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "23/Drafts", "id": "9"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "draft", "--subject", "hi", "--folder", "23/Drafts", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"folder":"23/Drafts"`) {
		t.Errorf("stdout = %q, want custom folder", stdout)
	}

	req := srv.Requests()[1]
	if req.URL.Query().Get("folder") != "23/Drafts" {
		t.Errorf("folder = %q, want 23/Drafts", req.URL.Query().Get("folder"))
	}
}

func TestMailDraft_RejectsMismatchedResponseFolder(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "default0/Trash", "id": "42"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "draft", "--subject", "hi", "--folder", "default0/Drafts", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailDraft_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "draft", "--subject", "hi", "--body", "hello", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if !strings.Contains(stdout, `"folder":"default0/Drafts"`) {
		t.Errorf("stdout = %q, want default folder", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailDraft_WithAttachment_RejectsLocallyNoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"},
		[]string{"mail", "draft", "--subject", "hi", "--input", `{"subject":"hi","attachments":["/tmp/x.txt"]}`, "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "attachment") {
		t.Errorf("stderr = %q, want mention of attachments", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network requests, got %v", srv.Requests())
	}
}

func TestMailDraft_UnknownFlag_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "draft", "--bogus", "x", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--bogus") {
		t.Errorf("stderr = %q, want mention of unknown flag", stderr)
	}
}

func TestDescribe_LLM_MailDraftIsWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.draft", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"effect":"write"`) {
		t.Errorf("stdout = %q, want effect write", stdout)
	}
}

func TestMailSend_StructuredInput_MultipleRecipients(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: []byte(`<script>(parent["callback_new"])({"data":"default0/INBOX/999"})</script>`),
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "send", "--input", `{"to":["a@example.com","b@example.com"],"subject":"hi","body":"hello"}`, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"sent":true`) {
		t.Errorf("stdout = %q, want sent:true", stdout)
	}
}

// TestMailFlag_DescribeDocumentsSeenAndFlaggedAsValueTaking is the regression test for
// discovery output that contradicted the parser: --seen and --flagged are tri-state and
// take an explicit true/false, so describing them as bare bool flags told agents to send a
// form the command always rejects.
func TestMailFlag_DescribeDocumentsSeenAndFlaggedAsValueTaking(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.flag", "--llm", "--full"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var described struct {
		Args map[string]struct {
			Type string `json:"type"`
		} `json:"args"`
	}
	if err := json.Unmarshal([]byte(stdout), &described); err != nil {
		t.Fatalf("decoding describe output %q: %v", stdout, err)
	}
	for _, name := range []string{"seen", "flagged"} {
		spec, ok := described.Args[name]
		if !ok {
			t.Fatalf("describe mail.flag does not document --%s", name)
		}
		if spec.Type == "bool" || spec.Type == "boolean" {
			t.Errorf("describe mail.flag reports --%s as %q, but the parser requires an explicit value", name, spec.Type)
		}
	}
}

// TestMailFlag_ExplicitBooleanValuesAreAccepted pins the documented calling form end to
// end, so the tri-state semantics stay reachable.
func TestMailFlag_ExplicitBooleanValuesAreAccepted(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "flag", "--id", "7", "--folder", "default0/INBOX", "--seen", "true", "--flagged", "false", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"updated":true`) {
		t.Errorf("stdout = %q, want updated:true", stdout)
	}
}

// TestMailSend_AttachDirectory_InvalidArgument is the regression test for a directory
// passing --attach validation: os.Stat alone succeeds on a directory, so the failure
// surfaced later as an opaque api_error from the multipart upload instead of a local
// invalid_argument. files.create, contacts.import, and events.import all reject it locally.
func TestMailSend_AttachDirectory_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	dir := t.TempDir()
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "send", "--to", "a@example.org", "--subject", "hi", "--attach", dir, "--llm",
	})

	if code != 4 {
		t.Fatalf("exit code = %d, want 4 (invalid_argument); stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "invalid_argument") || !strings.Contains(stderr, dir) {
		t.Errorf("stderr = %q, want an invalid_argument naming %q", stderr, dir)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/mail" {
			t.Fatal("a mail request was sent for an unusable --attach path")
		}
	}
}

// TestMailSend_AttachRegularFile_IsAccepted pins that the tightened check still admits an
// ordinary file.
func TestMailSend_AttachRegularFile_IsAccepted(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing attachment: %v", err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "send", "--to", "a@example.org", "--subject", "hi", "--attach", path, "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want a dry-run result", stdout)
	}
}

// TestBuildDraftMIME_RejectsHeaderInjection is the regression test for CR/LF smuggled
// through an address: mail.draft renders the message itself, so an unfiltered address
// terminates the To header and lets the caller append arbitrary headers, or an entire
// forged body, to the message stored in Drafts.
func TestBuildDraftMIME_RejectsHeaderInjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  mailSendInput
	}{
		{"crlf in to", mailSendInput{To: []string{"a@example.org\r\nBcc: victim@example.org"}, Subject: "s"}},
		{"lf in to", mailSendInput{To: []string{"a@example.org\nBcc: victim@example.org"}, Subject: "s"}},
		{"cr in cc", mailSendInput{To: []string{"a@example.org"}, Cc: []string{"b@example.org\rX-Spoof: y"}, Subject: "s"}},
		{"body break in bcc", mailSendInput{To: []string{"a@example.org"}, Bcc: []string{"c@example.org\r\n\r\nforged body"}, Subject: "s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateDraftAddresses(tc.msg); err == nil {
				t.Fatalf("validateDraftAddresses accepted %+v", tc.msg)
			}
		})
	}
}

// TestBuildDraftMIME_EncodesControlCharactersInSubject pins that the subject is already
// safe, because it always goes through RFC 2047 encoded-word encoding.
func TestBuildDraftMIME_EncodesControlCharactersInSubject(t *testing.T) {
	mime := string(buildDraftMIME(mailSendInput{
		To:      []string{"a@example.org"},
		Subject: "hi\r\nBcc: victim@example.org",
	}, nil))

	headers, _, ok := strings.Cut(mime, "\r\n\r\n")
	if !ok {
		t.Fatalf("message has no header/body separator: %q", mime)
	}
	// The break survives only inside a single encoded word (=0D=0A), so it never starts a
	// header line of its own.
	for _, line := range strings.Split(headers, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("subject started a new header line: %q", headers)
		}
	}
	if !strings.Contains(headers, "=0D=0A") {
		t.Errorf("headers = %q, want the line break encoded rather than emitted literally", headers)
	}
}

// TestMailDraft_HeaderInjectionInAddress_InvalidArgument checks the rejection end to end
// and confirms nothing reaches the network.
func TestMailDraft_HeaderInjectionInAddress_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "draft", "--to", "a@example.org\r\nBcc: victim@example.org", "--subject", "hi", "--llm",
	})

	if code != 4 {
		t.Fatalf("exit code = %d, want 4 (invalid_argument); stdout=%s stderr=%s", code, stdout, stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/mail" {
			t.Fatal("a draft request was sent for an address containing a line break")
		}
	}
}
