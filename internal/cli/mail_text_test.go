// ABOUTME: Tests for the plain-text cleanup of mail read bodies: link modes, tracking-parameter
// ABOUTME: removal, tag attributes and the detection and conversion of provider display HTML.
package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCleanTrackingURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.org/a?utm_source=x&id=7&_hsenc=p2A&_hsmi=9#top": "https://example.org/a?id=7#top",
		"https://example.org/a?UTM_Medium=x&mc_eid=1":                    "https://example.org/a",
		"https://example.org/a?q=a%20b&fbclid=z":                         "https://example.org/a?q=a%20b",
		"https://example.org/a":                                          "https://example.org/a",
		"https://example.org/a?id=1&&trk=x":                              "https://example.org/a?id=1",
		"mailto:a@example.org?utm_source=x":                              "mailto:a@example.org?utm_source=x",
	} {
		if got := cleanTrackingURL(in); got != want {
			t.Errorf("cleanTrackingURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCleanTextURLs_KeepsSentencePunctuation(t *testing.T) {
	in := "See https://example.org/x?utm_campaign=y. Or <https://example.org/z?id=1&gclid=2>, thanks"
	want := "See https://example.org/x. Or <https://example.org/z?id=1>, thanks"
	if got := cleanTextURLs(in); got != want {
		t.Errorf("cleanTextURLs = %q, want %q", got, want)
	}
}

func TestHTMLTagAttribute(t *testing.T) {
	for tag, want := range map[string]string{
		`<a href="https://e.org/?a=1&amp;b=2">`: "https://e.org/?a=1&amp;b=2",
		`<a class='x' HREF='y'>`:                "y",
		`<a href=plain title="t">`:              "plain",
		`<a title="href=no">`:                   "",
		`<a data-href="no" href = "yes">`:       "yes",
	} {
		if got := htmlTagAttribute(tag, "href"); got != want {
			t.Errorf("htmlTagAttribute(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestHTMLToTextWithLinks(t *testing.T) {
	in := `<p>Write to <a href="mailto:startups@example.org">startups@example.org</a> or ` +
		`<a href="mailto:help@example.org?subject=Hi">our team</a>.</p>` +
		`<p><a href="https://example.org/apply?id=3&amp;utm_source=mail&amp;_hsenc=p2">Apply now</a></p>` +
		`<a href="https://example.org/logo"><img src="logo.png"></a>` +
		`<p><a href="https://example.org/">https://example.org/</a> <a href="#top">top</a></p>` +
		`<p><a href="https://example.org/n?id=1&amp;utm_source=x">https://example.org/n?id=1&amp;utm_source=x</a></p>`
	for mode, want := range map[mailLinkMode]string{
		mailLinksClean: "Write to startups@example.org or our team (help@example.org).\nApply now (https://example.org/apply?id=3)\nhttps://example.org/ top\nhttps://example.org/n?id=1&utm_source=x",
		mailLinksNone:  "Write to startups@example.org or our team.\nApply now\nhttps://example.org/ top\nhttps://example.org/n?id=1&utm_source=x",
		mailLinksFull:  "Write to startups@example.org or our team (help@example.org?subject=Hi).\nApply now (https://example.org/apply?id=3&utm_source=mail&_hsenc=p2)\n (https://example.org/logo)https://example.org/ top\nhttps://example.org/n?id=1&utm_source=x",
	} {
		if got := htmlToTextWithLinks(in, mode); got != want {
			t.Errorf("%s:\n got %q\nwant %q", mode, got, want)
		}
	}
}

func TestLooksLikeDisplayHTML(t *testing.T) {
	for in, want := range map[string]bool{
		"Hello<br>world":                        true,
		"Hello<BR />world":                      true,
		`see <a href="https://e.org">e.org</a>`: true,
		"plain text with <https://e.org> link":  false,
		"Tom &amp; Jerry":                       false,
	} {
		if got := looksLikeDisplayHTML(in); got != want {
			t.Errorf("looksLikeDisplayHTML(%q) = %v, want %v", in, got, want)
		}
	}
}

// The provider's text view can return a text/plain part that is display HTML. mail read
// converts it to real plain text and shows each link once, without tracking parameters.
func TestMailRead_ConvertsDisplayHTMLInATextPart(t *testing.T) {
	displayPart := map[string]any{"id": "1", "disp": "inline", "content_type": "text/plain", "content": "Hi,<br>we&rsquo;ve received your application.<br><br>" +
		`Questions? <a href="mailto:startups@example.org">startups@example.org</a><br>` +
		`<a href="https://example.org/blog?utm_source=hs&amp;_hsenc=p2ANqtz&amp;_hsmi=1">Read our blog</a>`}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"default clean": {nil, "Hi,\nwe\u2019ve received your application.\n\nQuestions? startups@example.org\nRead our blog (https://example.org/blog)"},
		"none":          {[]string{"--links", "none"}, "Hi,\nwe\u2019ve received your application.\n\nQuestions? startups@example.org\nRead our blog"},
		"full":          {[]string{"--links", "full"}, "Hi,\nwe\u2019ve received your application.\n\nQuestions? startups@example.org\nRead our blog (https://example.org/blog?utm_source=hs&_hsenc=p2ANqtz&_hsmi=1)"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newMailReadServer(t, mailReadMessage(displayPart))
			defer srv.Close()
			args := append([]string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"}, tc.args...)
			stdout, stderr, code := run(t, srv, mailReadEnv, args)
			if code != 0 {
				t.Fatalf("exit code = %d; stderr=%s", code, stderr)
			}
			var result mailReadResult
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("stdout = %s: %v", stdout, err)
			}
			if result.Body != tc.want {
				t.Errorf("body = %q, want %q", result.Body, tc.want)
			}
		})
	}
}

func TestMailRead_CleansURLsInRealPlainText(t *testing.T) {
	part := map[string]any{"id": "1", "disp": "inline", "content_type": "text/plain", "content": "Track: <https://example.org/p?id=1&utm_medium=email>\nTom &amp; Jerry"}
	srv := newMailReadServer(t, mailReadMessage(part))
	defer srv.Close()
	stdout, _, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--format", "values", "--select", "body"})
	if want := "Track: <https://example.org/p?id=1>\nTom &amp; Jerry"; code != 0 || !strings.Contains(stdout, "https://example.org/p?id=1>") || strings.Contains(stdout, "utm_medium") || !strings.Contains(stdout, "&amp;") {
		t.Errorf("code %d, stdout = %q, want %q", code, stdout, want)
	}
}

func TestMailRead_LinksValidation(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart))
	defer srv.Close()
	for args, want := range map[string]string{
		"--links short":                   "--links must be clean, full or none",
		"--body-format html --links none": "--links applies only to --body-format text",
	} {
		full := append([]string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"}, strings.Fields(args)...)
		if _, stderr, code := run(t, srv, mailReadEnv, full); code != 4 || !strings.Contains(stderr, want) {
			t.Errorf("%s: code %d, stderr = %q, want %q", args, code, stderr, want)
		}
	}
}
