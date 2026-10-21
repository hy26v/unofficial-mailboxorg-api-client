// ABOUTME: Tests for mail senders and the --from argument of mail send and mail draft.
// ABOUTME: A fake OX server returns accounts, aliases, the default sender and the user name.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

var senderEnv = map[string]string{"MBX_USERNAME": "owner@mailbox.org", "MBX_PASSWORD": "pw"}

// senderServer serves the columns id, name, primary_address, personal, addresses,
// transport_url and deactivated for four accounts: the primary account with aliases and
// no personal name, an external account that can send, one without a transport and a
// deactivated one.
func senderServer(t *testing.T) *testutil.Server {
	t.Helper()
	srv := testutil.NewServer()
	t.Cleanup(srv.Close)
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "all", testutil.Response{Body: map[string]any{"data": []any{
		[]any{0, "mailbox.org", "Owner@mailbox.org", nil, "owner@mailbox.org, Team@Example.org, owner@secure.mailbox.org, alias@example.org, owner@tls.mailbox.org", nil, false},
		[]any{2, "Work", "me@work.example", "Work Name", "", "smtp://smtp.work.example", false},
		[]any{3, "Read only", "ro@other.example", nil, "", nil, false},
		[]any{4, "Old", "old@other.example", nil, "", "smtp://smtp.other.example", true},
	}}})
	srv.Handle("/ajax/user", "get", testutil.Response{Body: map[string]any{"data": map[string]any{"display_name": "Owner Name"}}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{"id": "io.ox/mail", "tree": map[string]any{"defaultSendAddress": "team@example.org"}},
	}}})
	return srv
}

func TestMailSenders_ListsAddressesWithDefaultAndVariants(t *testing.T) {
	srv := senderServer(t)
	stdout, stderr, code := run(t, srv, senderEnv, []string{"mail", "senders", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	var got struct {
		Senders []mailSender `json:"senders"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout = %q: %v", stdout, err)
	}
	var addresses []string
	for _, s := range got.Senders {
		addresses = append(addresses, s.Address)
	}
	want := []string{"owner@mailbox.org", "alias@example.org", "owner@secure.mailbox.org", "owner@tls.mailbox.org", "team@example.org", "me@work.example"}
	if strings.Join(addresses, ",") != strings.Join(want, ",") {
		t.Fatalf("addresses = %v, want %v", addresses, want)
	}
	byAddress := map[string]mailSender{}
	for _, s := range got.Senders {
		byAddress[s.Address] = s
	}
	if s := byAddress["owner@mailbox.org"]; !s.Primary || s.Default || s.Name != "Owner Name" || s.AccountID != 0 {
		t.Errorf("primary sender = %+v", s)
	}
	if s := byAddress["team@example.org"]; !s.Default || s.Primary {
		t.Errorf("default sender = %+v", s)
	}
	if byAddress["owner@secure.mailbox.org"].MailboxOrgVariant != "secure" || byAddress["owner@tls.mailbox.org"].MailboxOrgVariant != "tls" || byAddress["alias@example.org"].MailboxOrgVariant != "" {
		t.Errorf("variants = %+v", got.Senders)
	}
	if s := byAddress["me@work.example"]; s.Name != "Work Name" || s.AccountID != 2 || !s.Primary {
		t.Errorf("external sender = %+v", s)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/account" && r.URL.Query().Get("columns") != mailSenderColumns {
			t.Errorf("columns = %q, want %q", r.URL.Query().Get("columns"), mailSenderColumns)
		}
	}
}

func TestMailSenders_DefaultFallsBackToPrimary(t *testing.T) {
	srv := senderServer(t)
	srv.Handle("/ajax/jslob", "list", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{"id": "io.ox/mail", "tree": map[string]any{}},
	}}})
	stdout, _, code := run(t, srv, senderEnv, []string{"mail", "senders", "--format", "values", "--select", "address,default"})
	if code != 0 || !strings.HasPrefix(stdout, "owner@mailbox.org\ttrue\n") {
		t.Fatalf("code %d, stdout = %q", code, stdout)
	}
}

func draftRequest(t *testing.T, srv *testutil.Server) *http.Request {
	t.Helper()
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/mail" && r.URL.Query().Get("action") == "new" {
			return r
		}
	}
	return nil
}

func TestMailDraft_FromAliasAddsFromHeader(t *testing.T) {
	srv := senderServer(t)
	srv.Handle("/ajax/mail", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"folder_id": "default0/Drafts", "id": "7"}}})
	stdout, stderr, code := run(t, srv, senderEnv, []string{"mail", "draft", "--from", "Team@Example.org", "--to", "a@example.org", "--subject", "hi", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	if want := `{"id":"7","folder":"default0/Drafts","from":"Owner Name <team@example.org>"}` + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	req := draftRequest(t, srv)
	if req == nil {
		t.Fatal("no draft request")
	}
	body, _ := io.ReadAll(req.Body)
	if !strings.HasPrefix(string(body), "From: \"Owner Name\" <team@example.org>\r\n") {
		t.Errorf("draft MIME = %q, want a From header first", body)
	}
}

func TestMailDraft_WithoutFromHasNoFromHeaderAndNoSenderLookup(t *testing.T) {
	srv := senderServer(t)
	srv.Handle("/ajax/mail", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"folder_id": "default0/Drafts", "id": "7"}}})
	if _, stderr, code := run(t, srv, senderEnv, []string{"mail", "draft", "--to", "a@example.org", "--subject", "hi", "--llm"}); code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/account" {
			t.Error("draft without --from looked up the senders")
		}
	}
	body, _ := io.ReadAll(draftRequest(t, srv).Body)
	if strings.Contains(string(body), "From:") {
		t.Errorf("draft MIME = %q, want no From header", body)
	}
}

func TestMailDraft_RejectsUnknownFromBeforeWriting(t *testing.T) {
	srv := senderServer(t)
	srv.Handle("/ajax/mail", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"folder_id": "default0/Drafts", "id": "7"}}})
	for _, from := range []string{"ro@other.example", "old@other.example", "stranger@example.org"} {
		_, stderr, code := run(t, srv, senderEnv, []string{"mail", "draft", "--from", from, "--to", "a@example.org", "--llm"})
		if code == 0 || !strings.Contains(stderr, "mbx mail senders") {
			t.Errorf("--from %s: code %d, stderr %q", from, code, stderr)
		}
	}
	if draftRequest(t, srv) != nil {
		t.Error("a draft was written with an unknown --from")
	}
}

func TestMailSend_FromWithDisplayName(t *testing.T) {
	srv := senderServer(t)
	srv.Handle("/ajax/mail", "new", testutil.Response{Body: []byte(`<script>(parent["callback_new"])({"data":"default0/INBOX/999"})</script>`)})
	stdout, stderr, code := run(t, srv, senderEnv, []string{"mail", "send", "--from", "Support Desk <alias@example.org>", "--to", "a@example.org", "--subject", "hi", "--body", "x", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"from":"Support Desk <alias@example.org>"`) {
		t.Errorf("stdout = %q", stdout)
	}
	req := draftRequest(t, srv)
	if err := req.ParseMultipartForm(1 << 20); err != nil {
		t.Fatal(err)
	}
	var meta struct {
		From [][]any `json:"from"`
	}
	if err := json.Unmarshal([]byte(req.FormValue("json_0")), &meta); err != nil {
		t.Fatal(err)
	}
	if len(meta.From) != 1 || meta.From[0][0] != "Support Desk" || meta.From[0][1] != "alias@example.org" {
		t.Errorf("from = %#v", meta.From)
	}
}

func TestMailFrom_DryRunAndValidation(t *testing.T) {
	srv := senderServer(t)
	stdout, _, code := run(t, srv, senderEnv, []string{"mail", "draft", "--input", `{"from":"alias@example.org","to":["a@example.org"]}`, "--dry-run", "--llm"})
	if code != 0 || !strings.Contains(stdout, `"from":"alias@example.org"`) {
		t.Errorf("dry run: code %d, stdout %q", code, stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry run made %d requests", len(srv.Requests()))
	}
	for _, from := range []string{"not an address", "a@example.org, b@example.org", "x@example.org\r\nBcc: y@example.org"} {
		if _, _, code := run(t, srv, senderEnv, []string{"mail", "send", "--from", from, "--to", "a@example.org", "--subject", "s", "--dry-run", "--llm"}); code == 0 {
			t.Errorf("--from %q accepted", from)
		}
	}
}
