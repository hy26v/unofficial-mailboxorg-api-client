// ABOUTME: Tests for "mail draft --replace-id": fields taken from the earlier draft, the move of
// ABOUTME: that draft to Trash after the save, a failed move, delete protection and to_source.
package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// earlierDraft is a reply draft that mbx saved before: it threads to <orig@example.org>.
func earlierDraft() map[string]any {
	return map[string]any{
		"id": "41", "folder_id": "default0/Drafts", "subject": "Re: SUPPLIER UPDATE",
		"from":  [][]any{{"Owner Name", "admin@example.com"}},
		"to":    [][]any{{"Supplier Support", "support@supplier.example"}},
		"cc":    [][]any{{nil, "replies@supplier.example"}},
		"flags": 4,
		"headers": map[string]any{
			"Message-ID": "<draft@example.com>", "In-Reply-To": "<orig@example.org>",
			"References": "<first@example.org> <orig@example.org>",
		},
		"attachments": []any{map[string]any{"id": "1", "disp": "inline", "content_type": "text/plain", "content": "Old body\nsecond line"}},
	}
}

type replaceOutput struct {
	draftOutput
	ToSource string             `json:"to_source"`
	Replaced *mailDraftReplaced `json:"replaced"`
}

func runReplaceDraft(t *testing.T, s *replyServer, extra ...string) (replaceOutput, string, string, int) {
	t.Helper()
	args := append([]string{"mail", "draft", "--replace-id", "41", "--llm"}, extra...)
	stdout, stderr, code := runAgainstHTTPServer(t, s.Server, t.TempDir(), args)
	var out replaceOutput
	if stdout != "" {
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("stdout = %q: %v", stdout, err)
		}
	}
	_, mime := s.request("/ajax/mail", "new")
	return out, mime, stderr, code
}

func TestMailDraftReplace_KeepsWhatIsNotGivenAndMovesTheEarlierDraftToTrash(t *testing.T) {
	s := newReplyServer(t)
	s.original = earlierDraft()
	out, mime, stderr, code := runReplaceDraft(t, s, "--body", "New body")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr)
	}
	if out.ID != "77" || out.Subject != "Re: SUPPLIER UPDATE" || out.InReplyTo != "<orig@example.org>" || out.ToSource != mailToSourceReplaced ||
		strings.Join(out.To, ",") != `"Supplier Support" <support@supplier.example>` || strings.Join(out.Cc, ",") != "<replies@supplier.example>" ||
		out.From != "Owner Name <admin@example.com>" {
		t.Errorf("output = %+v", out)
	}
	if out.Replaced == nil || *out.Replaced != (mailDraftReplaced{ID: "41", Folder: "default0/Drafts", MovedTo: "default0/Trash", TrashID: "900"}) {
		t.Errorf("replaced = %+v", out.Replaced)
	}
	for _, want := range []string{
		"In-Reply-To: <orig@example.org>\r\n",
		"References: <first@example.org>\r\n <orig@example.org>\r\n",
		"Subject: Re: SUPPLIER UPDATE\r\n",
		"\r\n\r\nNew body",
	} {
		if !strings.Contains(mime, want) {
			t.Errorf("draft MIME lacks %q:\n%s", want, mime)
		}
	}
	if strings.Contains(mime, "Old body") {
		t.Errorf("draft MIME kept the old body:\n%s", mime)
	}
	// The move comes after the save, so a failed save leaves the earlier draft alone.
	saved, moved := -1, -1
	for i, r := range s.requests {
		switch r.URL.Query().Get("action") {
		case "new":
			saved = i
		case "update":
			moved = i
			if r.URL.Query().Get("id") != "41" || r.URL.Query().Get("folder") != "default0/Drafts" || !strings.Contains(s.bodies[r], `"folder_id":"default0/Trash"`) {
				t.Errorf("move request = %s %s", r.URL, s.bodies[r])
			}
		}
	}
	if saved < 0 || moved < saved {
		t.Errorf("save at %d, move at %d; want the move after the save", saved, moved)
	}
}

func TestMailDraftReplace_WithoutBodyKeepsTheBodyAndGivenToWins(t *testing.T) {
	s := newReplyServer(t)
	s.original = earlierDraft()
	out, mime, stderr, code := runReplaceDraft(t, s, "--to", "r.smith@supplier.example")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr)
	}
	if strings.Join(out.To, ",") != "r.smith@supplier.example" || out.ToSource != mailToSourceGiven || !strings.Contains(mime, "Old body\r\nsecond line") {
		t.Errorf("output = %+v, MIME:\n%s", out, mime)
	}
}

func TestMailDraftReplace_AFailedMoveKeepsBothDraftsAndSaysSo(t *testing.T) {
	s := newReplyServer(t)
	s.original = earlierDraft()
	s.moveFails = true
	out, _, stderr, code := runReplaceDraft(t, s, "--body", "New body")
	if code == 0 || out.ID != "77" || out.Replaced != nil {
		t.Errorf("code %d, output = %+v; want the new draft and an error", code, out)
	}
	if !strings.Contains(stderr, "saved as id 77") || !strings.Contains(stderr, "do not save the draft again") {
		t.Errorf("stderr = %s", stderr)
	}
}

func TestMailDraftReplace_ValidationAndDeleteProtection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	for _, tc := range []struct {
		args   []string
		config string
		want   string
	}{
		{[]string{"mail", "draft", "--replace-id", "41", "--reply-id", "5", "--reply-folder", "default0/INBOX", "--llm"}, "", "cannot be combined with --reply-id"},
		{[]string{"mail", "draft", "--replace-id", " ", "--llm"}, "", "--replace-id must be"},
		{[]string{"mail", "draft", "--replace-id", "41", "--body", "x", "--llm"}, protectionConfig("mail"), "delete_protected"},
	} {
		_, stderr, code := runWithConfig(t, srv, mailReadEnv, tc.args, tc.config)
		if code == 0 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: code %d, stderr = %s; want %q", tc.args, code, stderr, tc.want)
		}
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("validation made %d requests", len(srv.Requests()))
	}
	stdout, stderr, code := runWithConfig(t, srv, mailReadEnv, []string{"mail", "draft", "--replace-id", "41", "--body", "x", "--dry-run", "--llm"}, protectionConfig("mail"))
	if code != 0 || !strings.Contains(stdout, `"replace_id":"41"`) {
		t.Errorf("dry run: code %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
}

func TestMailDraftReply_ToSourceNamesReplyToOrSender(t *testing.T) {
	s := newReplyServer(t)
	stdout, stderr, code := runAgainstHTTPServer(t, s.Server, t.TempDir(), []string{"mail", "draft", "--reply-id", "12488", "--reply-folder", "default0/Trash", "--body", "Hi", "--llm"})
	if code != 0 || !strings.Contains(stdout, `"to_source":"reply-to"`) {
		t.Errorf("with Reply-To: code %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	delete(s.original["headers"].(map[string]any), "Reply-To")
	stdout, _, code = runAgainstHTTPServer(t, s.Server, t.TempDir(), []string{"mail", "draft", "--reply-id", "12488", "--reply-folder", "default0/Trash", "--body", "Hi", "--llm"})
	if code != 0 || !strings.Contains(stdout, `"to_source":"from"`) {
		t.Errorf("without Reply-To: code %d, stdout = %s", code, stdout)
	}
}

func TestMailDraftReplace_RefusesAMessageThatIsNotADraft(t *testing.T) {
	s := newReplyServer(t)
	s.original = earlierDraft()
	s.original["folder_id"], s.original["flags"] = "default0/INBOX", 32
	_, _, stderr, code := runReplaceDraft(t, s, "--folder", "default0/INBOX", "--body", "x")
	if code == 0 || !strings.Contains(stderr, "is not a draft") {
		t.Errorf("code %d, stderr = %s", code, stderr)
	}
	if r, _ := s.request("/ajax/mail", "new"); r != nil {
		t.Errorf("a draft was saved")
	}
	if r, _ := s.request("/ajax/mail", "update"); r != nil {
		t.Errorf("the message was moved")
	}
}

func TestDisplayName_DropsTheQuotesThatTheProviderKeeps(t *testing.T) {
	for raw, want := range map[string]string{
		`"Supplier Example S.L. Projects"`: "Supplier Example S.L. Projects",
		`"Say \"hi\""`:                     `Say "hi"`,
		` Ana   Partner `:                  "Ana Partner",
		`"`:                                `"`,
	} {
		if got := displayName(raw); got != want {
			t.Errorf("displayName(%q) = %q, want %q", raw, got, want)
		}
	}
	if got := headerAddress(mailAddress{Name: `"Supplier Example S.L."`, Address: "replies@supplier.example"}); got != `"Supplier Example S.L." <replies@supplier.example>` {
		t.Errorf("headerAddress = %s, want the name quoted once", got)
	}
}
