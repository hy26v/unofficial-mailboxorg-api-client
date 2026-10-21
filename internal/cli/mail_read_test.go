// ABOUTME: CLI tests for "mail read" and "mail attachment": exact mail?action=get and attachment
// ABOUTME: requests, body selection and conversion, seen-flag preservation, truncation, and file output.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

var mailReadEnv = map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

func mailReadMessage(parts ...map[string]any) map[string]any {
	return map[string]any{
		"id": "7", "folder_id": "default0/INBOX", "subject": "Quarterly invoice",
		"from":          [][]any{{"Billing", "billing@example.org"}},
		"to":            [][]any{{nil, "fixture-user@example.invalid"}},
		"cc":            []any{},
		"bcc":           nil,
		"sent_date":     1784851568000,
		"received_date": 1784851574000,
		"size":          4096,
		"flags":         0,
		"headers":       map[string]any{"X-Secret-Header": "must not appear"},
		"attachments":   parts,
	}
}

func newMailReadServer(t *testing.T, message map[string]any) *testutil.Server {
	t.Helper()
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "get", testutil.Response{Body: map[string]any{"data": message}})
	return srv
}

var (
	plainBodyPart = map[string]any{"id": "1", "disp": "inline", "content_type": "text/plain", "size": 30, "content": "Hello,\r\nyour invoice is attached."}
	pdfPart       = map[string]any{"id": "2", "disp": "attachment", "content_type": "application/pdf; name=invoice.pdf", "filename": "invoice.pdf", "size": 2048, "content": nil}
)

func TestMailRead_LLMReturnsBodyAndAttachmentsWithoutMarkingSeen(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart, pdfPart))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var result mailReadResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v; %s", err, stdout)
	}
	if result.ID != "7" || result.Folder != "default0/INBOX" || result.Subject != "Quarterly invoice" ||
		len(result.From) != 1 || result.From[0].Address != "billing@example.org" || len(result.Bcc) != 0 ||
		result.ReceivedDate == nil || *result.ReceivedDate != "2026-07-24T00:06:14Z" || result.Seen || result.Flagged {
		t.Errorf("metadata = %+v", result)
	}
	if result.BodyFormat != "text" || result.Body != "Hello,\nyour invoice is attached." || result.BodyTruncated || result.BodyBytes != len(result.Body) {
		t.Errorf("body = %q format=%q truncated=%v bytes=%d", result.Body, result.BodyFormat, result.BodyTruncated, result.BodyBytes)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].ID != "2" || result.Attachments[0].Filename != "invoice.pdf" ||
		result.Attachments[0].ContentType != "application/pdf" || *result.Attachments[0].Size != 2048 || result.Attachments[0].Disposition != "attachment" {
		t.Errorf("attachments = %+v", result.Attachments)
	}
	if strings.Contains(stdout, "X-Secret-Header") {
		t.Errorf("stdout exposed raw headers: %s", stdout)
	}

	req := srv.Requests()[1]
	query := req.URL.Query()
	if req.Method != "GET" || len(query) != 7 || query.Get("action") != "get" || query.Get("session") != "sess-1" || query.Get("timezone") != "UTC" ||
		query.Get("folder") != "default0/INBOX" || query.Get("id") != "7" || query.Get("view") != "text" || query.Get("unseen") != "true" {
		t.Errorf("read request = %s %s, want exact get query with unseen=true", req.Method, req.URL)
	}
}

func TestMailRead_MarkSeenOmitsUnseen(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--mark-seen", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if srv.Requests()[1].URL.Query().Has("unseen") {
		t.Errorf("--mark-seen request still sent unseen: %s", srv.Requests()[1].URL)
	}
	if !strings.Contains(stdout, `"seen":true`) {
		t.Errorf("stdout = %s, want seen true after --mark-seen", stdout)
	}
}

func TestMailRead_HTMLBodies(t *testing.T) {
	htmlPart := map[string]any{"id": "1", "disp": "inline", "content_type": "text/html", "content": "<html><head><style>p{}</style></head><body><p>Total:&nbsp;<b>42&euro;</b></p><script>x()</script><div>Thanks</div></body></html>"}

	t.Run("text view converts an unconverted HTML part", func(t *testing.T) {
		srv := newMailReadServer(t, mailReadMessage(htmlPart))
		defer srv.Close()
		stdout, _, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"})
		var result mailReadResult
		if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil {
			t.Fatalf("code=%d stdout=%s", code, stdout)
		}
		if result.BodyFormat != "text" || result.Body != "Total: 42€\nThanks" {
			t.Errorf("body = %q format=%q", result.Body, result.BodyFormat)
		}
	})

	t.Run("html view keeps HTML", func(t *testing.T) {
		srv := newMailReadServer(t, mailReadMessage(htmlPart))
		defer srv.Close()
		stdout, _, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--body-format", "html", "--llm"})
		var result mailReadResult
		if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil {
			t.Fatalf("code=%d stdout=%s", code, stdout)
		}
		if result.BodyFormat != "html" || !strings.Contains(result.Body, "<b>42&euro;</b>") {
			t.Errorf("body = %q format=%q", result.Body, result.BodyFormat)
		}
		if srv.Requests()[1].URL.Query().Get("view") != "html" {
			t.Errorf("view = %q, want html", srv.Requests()[1].URL.Query().Get("view"))
		}
	})

	t.Run("html view of a plain message reports text", func(t *testing.T) {
		srv := newMailReadServer(t, mailReadMessage(plainBodyPart))
		defer srv.Close()
		stdout, _, _ := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--body-format", "html", "--llm"})
		if !strings.Contains(stdout, `"body_format":"text"`) {
			t.Errorf("stdout = %s, want body_format text", stdout)
		}
	})
}

func TestMailRead_TruncatesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("a", 1023) + "€tail"
	srv := newMailReadServer(t, mailReadMessage(map[string]any{"id": "1", "content_type": "text/plain", "content": long}))
	defer srv.Close()

	stdout, _, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--max-body-bytes", "1024", "--llm"})
	var result mailReadResult
	if code != 0 || json.Unmarshal([]byte(stdout), &result) != nil {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
	if !result.BodyTruncated || result.Body != strings.Repeat("a", 1023) || result.BodyBytes != len(long) {
		t.Errorf("truncated=%v len(body)=%d bytes=%d", result.BodyTruncated, len(result.Body), result.BodyBytes)
	}
}

func TestMailRead_HumanOutputEscapesTerminalControls(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(map[string]any{"id": "1", "content_type": "text/plain", "content": "line one\n\x1b[2Jcleared‮reversed"}, pdfPart))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	if strings.ContainsRune(stdout, 0x1b) || strings.ContainsRune(stdout, 0x202e) {
		t.Errorf("human output kept a terminal control: %q", stdout)
	}
	for _, want := range []string{"Quarterly invoice", "invoice.pdf", "line one\n\\u001b[2Jcleared\\u202ereversed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q: %s", want, stdout)
		}
	}
}

func TestMailRead_RejectsMismatchedMessage(t *testing.T) {
	message := mailReadMessage(plainBodyPart)
	message["id"] = "8"
	srv := newMailReadServer(t, message)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"})
	if code == 0 || stdout != "" || !strings.Contains(stderr, "did not match the requested message") {
		t.Errorf("code=%d stdout=%q stderr=%q, want invalid_response", code, stdout, stderr)
	}
}

func TestMailRead_Validation(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"missing id":            {[]string{"mail", "read", "--folder", "default0/INBOX", "--llm"}, "--id is required"},
		"missing folder":        {[]string{"mail", "read", "--id", "7", "--llm"}, "--folder is required"},
		"bad format":            {[]string{"mail", "read", "--id", "7", "--folder", "f", "--body-format", "raw", "--llm"}, "--body-format must be text or html"},
		"small cap":             {[]string{"mail", "read", "--id", "7", "--folder", "f", "--max-body-bytes", "10", "--llm"}, "--max-body-bytes must be an integer between 1024"},
		"unknown flag":          {[]string{"mail", "read", "--id", "7", "--folder", "f", "--raw", "--llm"}, "unknown flag: --raw"},
		"attachment no save-to": {[]string{"mail", "attachment", "--id", "7", "--folder", "f", "--attachment", "2", "--llm"}, "--save-to is required"},
		"attachment no id":      {[]string{"mail", "attachment", "--id", "7", "--folder", "f", "--save-to", "x", "--llm"}, "--attachment is required"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := run(t, srv, mailReadEnv, tc.args)
			if code == 0 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Errorf("code=%d stdout=%q stderr=%q, want %q", code, stdout, stderr, tc.want)
			}
		})
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("validation made %d requests, want none", len(srv.Requests()))
	}
}

func TestMailAttachment_SavesOwnerOnlyFile(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart, pdfPart))
	defer srv.Close()
	srv.Handle("/ajax/mail", "attachment", testutil.Response{Body: []byte("%PDF-1.7 fixture")})
	saveTo := filepath.Join(t.TempDir(), "invoice.pdf")

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2", "--save-to", saveTo, "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	want := `{"id":"7","folder":"default0/INBOX","attachment":"2","filename":"invoice.pdf","content_type":"application/pdf","size":2048,"saved_to":"` + saveTo + `","bytes_written":16}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %s, want %s", stdout, want)
	}
	content, err := os.ReadFile(saveTo)
	if err != nil || string(content) != "%PDF-1.7 fixture" {
		t.Errorf("saved content = %q, err = %v", content, err)
	}
	if info, err := os.Stat(saveTo); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("saved file mode = %v, err = %v; want 0600", info.Mode().Perm(), err)
	}

	reqs := srv.Requests()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want login, read, download", len(reqs))
	}
	query := reqs[2].URL.Query()
	if reqs[2].Method != "GET" || len(query) != 6 || query.Get("action") != "attachment" || query.Get("folder") != "default0/INBOX" ||
		query.Get("id") != "7" || query.Get("attachment") != "2" || query.Get("save") != "1" || query.Get("session") != "sess-1" {
		t.Errorf("download request = %s %s, want exact attachment query", reqs[2].Method, reqs[2].URL)
	}
	if reqs[1].URL.Query().Get("unseen") != "true" {
		t.Errorf("attachment lookup marked the message seen: %s", reqs[1].URL)
	}
}

func TestMailAttachment_UnknownAttachmentIsNotDownloaded(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart, pdfPart))
	defer srv.Close()
	saveTo := filepath.Join(t.TempDir(), "x")

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "1", "--save-to", saveTo, "--llm"})
	if code == 0 || stdout != "" || !strings.Contains(stderr, `"not_found"`) {
		t.Errorf("code=%d stdout=%q stderr=%q, want not_found (part 1 is the body)", code, stdout, stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") == "attachment" {
			t.Errorf("unknown attachment was downloaded: %s", req.URL)
		}
	}
	if _, err := os.Stat(saveTo); !os.IsNotExist(err) {
		t.Errorf("save-to file exists after failure: %v", err)
	}
}

func TestMailAttachment_ProviderErrorWritesNothing(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(pdfPart))
	defer srv.Close()
	srv.Handle("/ajax/mail", "attachment", testutil.Response{Body: map[string]any{"error": "Attachment not found", "code": "MSG-0049", "categories": "USER_INPUT"}})
	saveTo := filepath.Join(t.TempDir(), "x")

	_, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2", "--save-to", saveTo, "--llm"})
	if code == 0 || !strings.Contains(stderr, "MSG-0049") {
		t.Errorf("code=%d stderr=%q, want provider error", code, stderr)
	}
	if _, err := os.Stat(saveTo); !os.IsNotExist(err) {
		t.Errorf("save-to file exists after provider error: %v", err)
	}
}

func TestMailRead_AcceptsNumericProviderIDs(t *testing.T) {
	message := mailReadMessage(plainBodyPart, map[string]any{"id": 3, "disp": "attachment", "content_type": "image/png", "filename": "logo.png", "size": 10})
	message["id"] = 7
	srv := newMailReadServer(t, message)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"id":"7"`) || !strings.Contains(stdout, `"attachments":[{"id":"3","filename":"logo.png"`) {
		t.Errorf("stdout = %s, want string ids for numeric provider ids", stdout)
	}
}

// The part layout below is the shape mailbox.org returned for a multipart/mixed message in
// the text view: the HTML alternative of the body appears as a contentless attachment with a
// generated Part_<id>.html name, next to real attachments, one of which also has a generated
// name. Only the HTML alternative is the body; everything else is a real attachment.
func TestMailRead_HidesTheTextViewHTMLAlternative(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(
		map[string]any{"id": "1.1", "disp": "inline", "content_type": "text/plain", "size": 20, "content": "See the invitation.", "sanitized": true},
		map[string]any{"id": "1.2", "disp": "attachment", "content_type": "text/html", "filename": "Part_1.2.html", "size": 2900, "content": nil},
		map[string]any{"id": "1.3", "disp": "attachment", "content_type": "text/calendar; charset=UTF-8; method=REQUEST", "filename": "Part_1.3.ics", "size": 1900, "content": nil},
		map[string]any{"id": "2", "disp": "attachment", "content_type": "text/html", "filename": "report.html", "size": 800, "content": nil},
		map[string]any{"id": "3", "disp": "attachment", "content_type": "application/pdf", "filename": "agenda.pdf", "size": 25244, "content": nil},
	))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	var result mailReadResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v", err)
	}
	ids := []string{}
	for _, attachment := range result.Attachments {
		ids = append(ids, attachment.ID+":"+attachment.Filename)
	}
	if strings.Join(ids, ",") != "1.3:Part_1.3.ics,2:report.html,3:agenda.pdf" {
		t.Errorf("attachments = %v, want the calendar, the named HTML file, and the PDF only", ids)
	}
	if result.Body != "See the invitation." {
		t.Errorf("body = %q", result.Body)
	}
}

func TestDescribe_LLM_MailReadIsAReadWithAWriteFlag(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.read", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	// Reading changes nothing; only --mark-seen does, so discovery marks that flag.
	if !strings.Contains(stdout, `"effect":"read","idempotent":true`) {
		t.Errorf("stdout = %s, want operation effect read and idempotent true", stdout)
	}
	if !strings.Contains(stdout, `"mark-seen":{"type":"bool","required":false,"effect":"write"}`) {
		t.Errorf("stdout = %s, want --mark-seen marked with effect write", stdout)
	}
	assertGolden(t, "testdata/describe_mail_read_llm.golden", stdout)
}

func TestHTMLToText_KeepsTextAroundAngleBracketsAndDropsOnlyTags(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"comparison text":           {"<p>3<5 but 7>2</p>", "3<5 but 7>2"},
		"lone less-than at end":     {"a <", "a <"},
		"quoted attribute with >":   {`<a title="x > y" href='a>b'>link</a> after`, "link after"},
		"apostrophe in bare value":  {`<p data-x=it's>kept</p>`, "kept"},
		"script and style dropped":  {"<style>p>a{}</style>A<script>if (a<b) { x('</p>') }</script>B", "AB"},
		"upper-case hidden element": {"<SCRIPT type=x>bad()</SCRIPT>ok", "ok"},
		"longer end-tag name":       {"<title>t</titles>still hidden</title>shown", "shown"},
		"entities decoded once":     {"&amp;lt;b&amp;gt; &lt;i&gt;", "&lt;b&gt; <i>"},
		"escaped tag stays text":    {"&lt;script&gt;alert(1)&lt;/script&gt;", "<script>alert(1)</script>"},
		"comments and doctype":      {"<!DOCTYPE html><!-- hidden > still hidden -->x<?xml version=\"1.0\"?>y", "xy"},
		"line breaks":               {"one<br>two<br/>three</div>four</P>five", "one\ntwo\nthree\nfour\nfive"},
		"unterminated tag kept":     {`ok <b class="x`, `ok <b class="x`},
		"non-ascii text":            {"<b>İstanbul</b> <i>café</i>", "İstanbul café"},
		"closed head hidden":        {"<html><head><meta charset=utf-8><title>T</title></head><body><p>Hi</p></body></html>", "Hi"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := htmlToText(tc.in); got != tc.want {
				t.Errorf("htmlToText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A missing end tag or comment end must not hide the rest of the body.
func TestHTMLToText_MissingEndTagsKeepBodyText(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"unclosed head ends at body":         {"<html><head><meta charset=utf-8><body><p>Hello world</p>", "Hello world"},
		"unclosed head ends at block tag":    {"<head><meta charset=utf-8><link rel=x><p>Hello</p>", "Hello"},
		"unclosed head ends at text":         {"<head><meta charset=utf-8>\n  Hello", "Hello"},
		"unclosed head at end of input":      {"<head><meta charset=utf-8>  ", ""},
		"head end tags inside head ignored":  {"<head><noscript></noscript></head>x", "x"},
		"unclosed title before body":         {"<head><title>Subject<body>Text</body>", "Text"},
		"unclosed title before head end":     {"<head><title>Subject</head><body>Text", "Text"},
		"unclosed style before body":         {"<style>p{color:red}<body><p>Shown</p>", "Shown"},
		"unclosed script without body":       {"<p>A</p><script>x", "A\nx"},
		"unclosed title in head without end": {"<head><title>Only title", "Only title"},
		"unterminated comment drops opener":  {"Before<!-- not closed <b>After</b>", "Before not closed After"},
		"second head is not hidden":          {"<head></head><body><head>x</head>y", "xy"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := htmlToText(tc.in); got != tc.want {
				t.Errorf("htmlToText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// forwardedMessage returns a mail read response whose message carries one forwarded
// message (nested_msgs) with its own body and a PDF attachment with id 2.2, plus one
// further message attached inside it.
func forwardedMessage() map[string]any {
	message := mailReadMessage(plainBodyPart, map[string]any{"id": "2", "disp": "attachment", "content_type": "message/rfc822", "filename": "Original.eml", "size": 9000})
	message["nested_msgs"] = []any{map[string]any{
		"subject":       "Original \u202einvoice",
		"from":          [][]any{{"Vendor", "vendor@example.com"}},
		"to":            [][]any{{nil, "billing@example.org"}},
		"cc":            []any{},
		"sent_date":     1784851000000,
		"received_date": 1784851100000,
		"attachments": []any{
			map[string]any{"id": "2.1", "disp": "inline", "content_type": "text/plain", "content": "nested body text"},
			map[string]any{"id": "2.2", "disp": "attachment", "content_type": "application/pdf", "filename": "original.pdf", "size": 1200},
		},
		"nested_msgs": []any{map[string]any{
			"subject": "Inner", "from": [][]any{{nil, "inner@example.net"}},
			"attachments": []any{map[string]any{"disp": "attachment", "content_type": "image/png", "filename": "no-id.png", "size": 5}},
		}},
	}}
	return message
}

func TestMailRead_ShowsNestedMessages(t *testing.T) {
	srv := newMailReadServer(t, forwardedMessage())
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	var result mailReadResult
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v; %s", err, stdout)
	}
	if len(result.NestedMessages) != 1 {
		t.Fatalf("nested_messages = %+v, want one", result.NestedMessages)
	}
	nested := result.NestedMessages[0]
	if nested.Subject != "Original \u202einvoice" || len(nested.From) != 1 || nested.From[0].Address != "vendor@example.com" ||
		nested.SentDate == nil || *nested.SentDate != "2026-07-23T23:56:40Z" || nested.ReceivedDate == nil {
		t.Errorf("nested headers = %+v", nested)
	}
	if len(nested.Attachments) != 1 || nested.Attachments[0].ID != "2.2" || nested.Attachments[0].Filename != "original.pdf" {
		t.Errorf("nested attachments = %+v, want only original.pdf with id 2.2", nested.Attachments)
	}
	if len(nested.NestedMessages) != 1 || nested.NestedMessages[0].Subject != "Inner" ||
		len(nested.NestedMessages[0].Attachments) != 1 || nested.NestedMessages[0].Attachments[0].ID != "" {
		t.Errorf("inner nested message = %+v, want subject Inner and one attachment without id", nested.NestedMessages)
	}
	if strings.Contains(stdout, "nested body text") || result.Body != "Hello,\nyour invoice is attached." {
		t.Errorf("body = %q; the nested body must not join the message body", result.Body)
	}

	plain := newMailReadServer(t, mailReadMessage(plainBodyPart))
	defer plain.Close()
	stdout, _, _ = run(t, plain, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"})
	if !strings.Contains(stdout, `"nested_messages":[]`) {
		t.Errorf("stdout = %s, want an empty nested_messages array", stdout)
	}
}

func TestMailRead_HumanOutputShowsNestedMessages(t *testing.T) {
	srv := newMailReadServer(t, forwardedMessage())
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	for _, want := range []string{"nested_message", "vendor@example.com", "2026-07-23T23:56:40Z", "2.2", "original.pdf", "1.1", "Inner", "no-id.png"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("human output missing %q: %s", want, stdout)
		}
	}
	if strings.ContainsRune(stdout, 0x202e) {
		t.Errorf("human output kept a bidi override from the nested subject: %q", stdout)
	}
}

func TestMailAttachment_AcceptsANestedMessageAttachmentID(t *testing.T) {
	srv := newMailReadServer(t, forwardedMessage())
	defer srv.Close()
	srv.Handle("/ajax/mail", "attachment", testutil.Response{Body: []byte("%PDF-nested")})
	saveTo := filepath.Join(t.TempDir(), "original.pdf")

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2.2", "--save-to", saveTo, "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"filename":"original.pdf"`) {
		t.Errorf("stdout = %s, want the nested attachment's file name", stdout)
	}
	req := srv.Requests()[len(srv.Requests())-1]
	query := req.URL.Query()
	if query.Get("action") != "attachment" || query.Get("attachment") != "2.2" || query.Get("id") != "7" || query.Has("allow_nested_messages") {
		t.Errorf("download request = %s, want the documented attachment request for id 2.2", req.URL)
	}
}

// The download names only the outer message, so an attachment id that mail read lists
// more than once must be refused instead of saving a possibly different part.
func TestMailAttachment_RejectsAnAmbiguousAttachmentID(t *testing.T) {
	nestedWith := func(attachmentID, filename string) map[string]any {
		return map[string]any{
			"subject": "Forwarded " + filename, "from": [][]any{{nil, "vendor@example.com"}},
			"attachments": []any{map[string]any{"id": attachmentID, "disp": "attachment", "content_type": "application/pdf", "filename": filename, "size": 10}},
		}
	}
	outerAndNested := mailReadMessage(plainBodyPart, pdfPart)
	outerAndNested["nested_msgs"] = []any{nestedWith("2", "nested.pdf")}
	twoNested := mailReadMessage(plainBodyPart)
	twoNested["nested_msgs"] = []any{nestedWith("3.1", "first.pdf"), nestedWith("3.1", "second.pdf")}

	for name, message := range map[string]map[string]any{
		"outer and nested message": outerAndNested,
		"two nested messages":      twoNested,
	} {
		t.Run(name, func(t *testing.T) {
			attachmentID := "2"
			if name == "two nested messages" {
				attachmentID = "3.1"
			}
			srv := newMailReadServer(t, message)
			defer srv.Close()
			srv.Handle("/ajax/mail", "attachment", testutil.Response{Body: []byte("%PDF-wrong")})
			saveTo := filepath.Join(t.TempDir(), "out.pdf")

			stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", attachmentID, "--save-to", saveTo, "--llm"})
			if code != 4 || stdout != "" || !strings.Contains(stderr, "ambiguous") {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want an ambiguous-id invalid_argument error", code, stdout, stderr)
			}
			for _, req := range srv.Requests() {
				if req.URL.Query().Get("action") == "attachment" {
					t.Fatalf("an ambiguous id was downloaded: %s", req.URL)
				}
			}
			if _, err := os.Stat(saveTo); !os.IsNotExist(err) {
				t.Fatalf("--save-to file exists or stat failed: %v", err)
			}
		})
	}
}
