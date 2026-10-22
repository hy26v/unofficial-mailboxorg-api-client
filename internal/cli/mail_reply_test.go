// ABOUTME: Tests for reply drafts and signatures: threading headers, recipients, subject, sender,
// ABOUTME: quote, the message-source fallback for Message-ID, and snippet signatures for draft and send.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// replyServer is a fake OX server with one original message, the sender list of
// owner@mailbox.org (alias admin@example.com), two signature snippets and Drafts.
type replyServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	bodies   map[*http.Request]string
	original map[string]any
	source   string
	jslob    map[string]any
}

func newReplyServer(t *testing.T) *replyServer {
	t.Helper()
	s := &replyServer{
		bodies: map[*http.Request]string{},
		original: map[string]any{
			"id": "12488", "folder_id": "default0/Trash", "subject": "Grant Application Confirmation",
			"from":          [][]any{{"Startups Team", "noreply@example.org"}},
			"to":            [][]any{{nil, "admin@example.com"}},
			"cc":            [][]any{{"Other Person", "other@example.net"}, {nil, "owner@mailbox.org"}},
			"sent_date":     1784851568000,
			"received_date": 1784851574000,
			"flags":         0,
			"headers": map[string]any{
				"Message-ID": "<abc@example.org>", "References": "<r1@example.org> <r2@example.org>",
				"Reply-To": "Startups <startups@example.org>",
			},
			"attachments": []any{map[string]any{"id": "1", "disp": "inline", "content_type": "text/plain",
				"content": "Thanks for applying.<br>We will reply."}},
		},
		jslob: map[string]any{"defaultSendAddress": "owner@mailbox.org", "defaultSignature": "5", "defaultReplyForwardSignature": "6"},
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies[r] = string(body)
		s.mu.Unlock()
		query := r.URL.Query()
		switch r.URL.Path + "?" + query.Get("action") {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/account?all":
			writeJSONResponse(w, map[string]any{"data": []any{
				[]any{0, "mailbox.org", "owner@mailbox.org", nil, "owner@mailbox.org, admin@example.com", nil, false},
			}})
		case "/ajax/user?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{"display_name": "Owner Name"}})
		case "/ajax/jslob?list":
			writeJSONResponse(w, map[string]any{"data": []any{map[string]any{"id": "io.ox/mail", "tree": s.jslob}}})
		case "/ajax/snippet?get":
			content := map[string]string{"5": "Owner Name\nexample.com", "6": `<p>Best regards,<br>Owner</p><p><a href="https://example.com/?utm_source=sig">Our site</a></p>`}[query.Get("id")]
			writeJSONResponse(w, map[string]any{"data": map[string]any{"id": query.Get("id"), "displayname": "sig", "content": content,
				"module": "io.ox/mail", "type": "signature", "shared": false, "createdby": 3}})
		case "/ajax/mail?get":
			if query.Get("src") == "1" {
				writeJSONResponse(w, map[string]any{"data": s.source})
				return
			}
			writeJSONResponse(w, map[string]any{"data": s.original})
		case "/ajax/mail?new":
			if r.Method == http.MethodPost {
				// mail send: the callback page that DoMultipartCallback reads.
				w.Header().Set("Content-Type", "text/html")
				_, _ = io.WriteString(w, `<html><script>callback_new({"data":"default0/Sent/9"})</script></html>`)
				return
			}
			writeJSONResponse(w, map[string]any{"data": map[string]any{"folder_id": "default0/Drafts", "id": "77"}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

// request returns the first request with this action on path, and its body.
func (s *replyServer) request(path, action string) (*http.Request, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.URL.Path == path && r.URL.Query().Get("action") == action {
			return r, s.bodies[r]
		}
	}
	return nil, ""
}

type draftOutput struct {
	ID        string   `json:"id"`
	From      string   `json:"from"`
	To        []string `json:"to"`
	Cc        []string `json:"cc"`
	Subject   string   `json:"subject"`
	InReplyTo string   `json:"in_reply_to"`
	Signature string   `json:"signature"`
}

func runReplyDraft(t *testing.T, s *replyServer, extra ...string) (draftOutput, string) {
	t.Helper()
	args := append([]string{"mail", "draft", "--reply-id", "12488", "--reply-folder", "default0/Trash", "--llm"}, extra...)
	stdout, stderr, code := runAgainstHTTPServer(t, s.Server, t.TempDir(), args)
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	var out draftOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("stdout = %q: %v", stdout, err)
	}
	_, mime := s.request("/ajax/mail", "new")
	return out, mime
}

func TestMailDraftReply_ThreadsAndAnswersTheReplyToAddress(t *testing.T) {
	s := newReplyServer(t)
	out, mime := runReplyDraft(t, s, "--body", "Hello")
	if out.InReplyTo != "<abc@example.org>" || out.Subject != "Re: Grant Application Confirmation" ||
		out.From != "Owner Name <admin@example.com>" || strings.Join(out.To, ",") != `"Startups" <startups@example.org>` || len(out.Cc) != 0 {
		t.Errorf("output = %+v", out)
	}
	for _, want := range []string{
		"From: \"Owner Name\" <admin@example.com>\r\n",
		"To: \"Startups\" <startups@example.org>\r\n",
		"Subject: Re: Grant Application Confirmation\r\n",
		"In-Reply-To: <abc@example.org>\r\n",
		"References: <r1@example.org>\r\n <r2@example.org>\r\n <abc@example.org>\r\n",
	} {
		if !strings.Contains(mime, want) {
			t.Errorf("draft MIME misses %q:\n%s", want, mime)
		}
	}
	if !strings.HasSuffix(mime, "\r\n\r\nHello") {
		t.Errorf("draft body is not just the given body:\n%s", mime)
	}
	if get, _ := s.request("/ajax/mail", "get"); get == nil || get.URL.Query().Get("unseen") != "true" || get.URL.Query().Get("folder") != "default0/Trash" {
		t.Errorf("original read = %v, want an unseen read of default0/Trash", get)
	}
}

func TestMailDraftReply_ReplyAllQuoteAndGivenValues(t *testing.T) {
	s := newReplyServer(t)
	out, mime := runReplyDraft(t, s, "--reply-all", "--quote", "--body", "Hello")
	if strings.Join(out.Cc, ",") != `"Other Person" <other@example.net>` {
		t.Errorf("cc = %q, want the other recipient without the account's own addresses", out.Cc)
	}
	wantBody := "Hello\r\n\r\nOn 2026-07-24T00:06:08Z, Startups Team <noreply@example.org> wrote:\r\n> Thanks for applying.\r\n> We will reply."
	if !strings.HasSuffix(mime, wantBody) {
		t.Errorf("draft MIME =\n%s\nwant the quote at the end:\n%s", mime, wantBody)
	}

	s = newReplyServer(t)
	out, _ = runReplyDraft(t, s, "--to", "startups@example.org", "--subject", "Follow-up", "--from", "owner@mailbox.org")
	if strings.Join(out.To, ",") != "startups@example.org" || out.Subject != "Follow-up" || out.From != "Owner Name <owner@mailbox.org>" || out.InReplyTo != "<abc@example.org>" {
		t.Errorf("given values were not kept: %+v", out)
	}
}

func TestMailDraftReply_ToTheAccountsOwnMessageGoesToItsRecipients(t *testing.T) {
	s := newReplyServer(t)
	s.original["from"] = [][]any{{"Owner Name", "admin@example.com"}}
	s.original["to"] = [][]any{{"Startups", "startups@example.org"}}
	s.original["headers"] = map[string]any{"Message-ID": "<sent@example.com>"}
	s.original["subject"] = "RE: Question"
	out, mime := runReplyDraft(t, s)
	if strings.Join(out.To, ",") != `"Startups" <startups@example.org>` || out.Subject != "RE: Question" || out.From != "Owner Name <admin@example.com>" {
		t.Errorf("output = %+v", out)
	}
	if !strings.Contains(mime, "References: <sent@example.com>\r\n") {
		t.Errorf("draft MIME =\n%s", mime)
	}
}

func TestMailDraftReply_ReadsTheSourceWhenHeadersHaveNoMessageID(t *testing.T) {
	s := newReplyServer(t)
	s.original["headers"] = map[string]any{"X-Other": "1"}
	s.source = "Message-ID: <src@example.org>\r\nIn-Reply-To: <parent@example.org>\r\nSubject: x\r\n\r\nbody"
	out, mime := runReplyDraft(t, s)
	if out.InReplyTo != "<src@example.org>" || !strings.Contains(mime, "References: <parent@example.org>\r\n <src@example.org>\r\n") {
		t.Errorf("output = %+v, MIME =\n%s", out, mime)
	}

	s = newReplyServer(t)
	s.original["headers"] = map[string]any{}
	s.source = "Subject: no id\r\n\r\nbody"
	_, stderr, code := runAgainstHTTPServer(t, s.Server, t.TempDir(), []string{"mail", "draft", "--reply-id", "12488", "--reply-folder", "default0/Trash", "--llm"})
	if code != 4 || !strings.Contains(stderr, "has no valid Message-ID header") {
		t.Errorf("code %d, stderr = %s", code, stderr)
	}
	if req, _ := s.request("/ajax/mail", "new"); req != nil {
		t.Error("a draft was saved without a Message-ID to answer")
	}
}

func TestMailDraftReply_ValidationAndDryRun(t *testing.T) {
	s := newReplyServer(t)
	for args, want := range map[string]string{
		"--reply-id 12488":              "--reply-id and --reply-folder must be given together",
		"--reply-folder default0/INBOX": "--reply-id and --reply-folder must be given together",
		"--quote --subject x":           "--reply-all and --quote need --reply-id and --reply-folder",
		"--signature  --subject x":      "--signature must be a snippet id",
	} {
		full := append([]string{"mail", "draft", "--llm"}, strings.Fields(args)...)
		if strings.HasPrefix(args, "--signature") {
			full = []string{"mail", "draft", "--llm", "--signature", " ", "--subject", "x"}
		}
		if _, stderr, code := runAgainstHTTPServer(t, s.Server, t.TempDir(), full); code != 4 || !strings.Contains(stderr, want) {
			t.Errorf("%s: code %d, stderr = %s, want %q", args, code, stderr, want)
		}
	}
	stdout, _, code := runAgainstHTTPServer(t, s.Server, t.TempDir(), []string{"mail", "draft", "--reply-id", "12488", "--reply-folder", "default0/Trash", "--signature", "default", "--dry-run", "--llm"})
	if want := `{"dry_run":true,"folder":"default0/Drafts","reply_folder":"default0/Trash","reply_id":"12488","signature":"default"}` + "\n"; code != 0 || stdout != want {
		t.Errorf("dry run = %q, want %q", stdout, want)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 0 {
		t.Errorf("validation or dry run sent %d requests", len(s.requests))
	}
}

func TestMailDraft_SignatureComesBeforeTheQuote(t *testing.T) {
	s := newReplyServer(t)
	out, mime := runReplyDraft(t, s, "--signature", "default", "--quote", "--body", "Hello")
	if out.Signature != "6" {
		t.Errorf("signature = %q, want the reply signature 6", out.Signature)
	}
	want := "Hello\r\n\r\nBest regards,\r\nOwner\r\nOur site (https://example.com/)\r\n\r\nOn "
	if !strings.Contains(mime, want) {
		t.Errorf("draft MIME =\n%s\nwant %q", mime, want)
	}

	s = newReplyServer(t)
	s.jslob = map[string]any{}
	_, stderr, code := runAgainstHTTPServer(t, s.Server, t.TempDir(), []string{"mail", "draft", "--signature", "default", "--subject", "x", "--llm"})
	if code != 4 || !strings.Contains(stderr, "no default signature is set") {
		t.Errorf("code %d, stderr = %s", code, stderr)
	}
}

func TestMailSend_AddsTheSignature(t *testing.T) {
	s := newReplyServer(t)
	_, stderr, code := runAgainstHTTPServer(t, s.Server, t.TempDir(), []string{"mail", "send", "--to", "a@example.org", "--subject", "x", "--body", "Hi", "--signature", "default", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	req, body := s.request("/ajax/mail", "new")
	if req == nil {
		t.Fatalf("no send request; stderr=%s", stderr)
	}
	if !strings.Contains(body, `Hi\n\nOwner Name\nexample.com`) {
		t.Errorf("send request body = %s, want the new-mail signature after the body", body)
	}
}
