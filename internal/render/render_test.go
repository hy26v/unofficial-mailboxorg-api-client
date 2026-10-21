// ABOUTME: Tests for the human and LLM render helpers: compact deterministic JSON,
// ABOUTME: human key/value tables, and error rendering in both modes.
package render

import (
	"bytes"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

func TestCompactJSON_NoIndentationNoTrailingSpace(t *testing.T) {
	var buf bytes.Buffer
	err := CompactJSON(&buf, map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("CompactJSON failed: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "  ") {
		t.Errorf("output contains indentation: %q", got)
	}
	if got != `{"a":"b"}`+"\n" {
		t.Errorf("got %q", got)
	}
}

func TestCompactJSON_DoesNotEscapeHTML(t *testing.T) {
	var buf bytes.Buffer
	CompactJSON(&buf, map[string]string{"url": "https://a.com/x&y"})
	got := buf.String()
	if strings.Contains(got, `\u0026`) {
		t.Errorf("HTML-escaped output: %q", got)
	}
	if !strings.Contains(got, "x&y") {
		t.Errorf("expected literal &, got %q", got)
	}
}

func TestTable_HumanReadable(t *testing.T) {
	var buf bytes.Buffer
	Table(&buf, []string{"id", "title"}, [][]string{{"1", "Buy milk"}, {"2", "Call bank"}})
	got := buf.String()
	if !strings.Contains(got, "id") || !strings.Contains(got, "title") {
		t.Errorf("got %q, want header present", got)
	}
	if !strings.Contains(got, "Buy milk") || !strings.Contains(got, "Call bank") {
		t.Errorf("got %q, want row values present", got)
	}
}

func TestTable_NoRows_HeaderOnly(t *testing.T) {
	var buf bytes.Buffer
	Table(&buf, []string{"id", "title"}, nil)
	got := buf.String()
	if !strings.Contains(got, "id") || !strings.Contains(got, "title") {
		t.Errorf("got %q, want header present", got)
	}
}

func TestTable_EscapesCellControlCharacters(t *testing.T) {
	var buf bytes.Buffer
	if err := Table(&buf, []string{"id", "title"}, [][]string{{"1", "first\tcolumn\r\nforged\x00row"}}); err != nil {
		t.Fatalf("Table() error = %v", err)
	}

	got := buf.String()
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("table has %d physical lines, want header plus one row: %q", strings.Count(got, "\n"), got)
	}
	if !strings.Contains(got, `first\tcolumn\r\nforged\u0000row`) {
		t.Fatalf("table = %q, want escaped control characters", got)
	}
}

func TestKeyValueTable_HumanReadable(t *testing.T) {
	var buf bytes.Buffer
	KeyValueTable(&buf, []Field{{Name: "email", Value: "a@b.com"}, {Name: "user_id", Value: "42"}})
	got := buf.String()
	if !strings.Contains(got, "email") || !strings.Contains(got, "a@b.com") {
		t.Errorf("got %q, want fields present", got)
	}
}

func TestKeyValueTable_EscapesCellControlCharacters(t *testing.T) {
	var buf bytes.Buffer
	if err := KeyValueTable(&buf, []Field{{Name: "subject", Value: "first\nforged\tfield"}}); err != nil {
		t.Fatalf("KeyValueTable() error = %v", err)
	}

	got := buf.String()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, `first\nforged\tfield`) {
		t.Fatalf("table did not preserve one physical record: %q", got)
	}
}

func TestErrorLLM_CompactJSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	e := &cliErrors.Error{Code: "auth_failed", Message: "bad creds", HTTPStatus: 200}
	Error(&buf, e, true)
	got := buf.String()
	if !strings.HasPrefix(got, `{"error":`) {
		t.Errorf("got %q, want {\"error\":... envelope", got)
	}
}

func TestErrorHuman_ReadableMessage(t *testing.T) {
	var buf bytes.Buffer
	e := &cliErrors.Error{Code: "auth_failed", Message: "bad creds", HTTPStatus: 200}
	Error(&buf, e, false)
	got := buf.String()
	if !strings.Contains(got, "bad creds") {
		t.Errorf("got %q, want message present", got)
	}
	if strings.Contains(got, `{"error"`) {
		t.Errorf("human error output should not be JSON: %q", got)
	}
}

func TestSafeText_EscapesC1AndBidiControls(t *testing.T) {
	input := "a\u009b31mb\u202ec\u2066d\u200fe\x1bf\tg\nh\\i ü"
	want := `a\u009b31mb\u202ec\u2066d\u200fe\u001bf\tg\nh\\i ü`
	if got := SafeText(input); got != want {
		t.Errorf("SafeText(%q) = %q, want %q", input, got, want)
	}
}

func TestTable_EscapesC1AndBidiControls(t *testing.T) {
	var buf bytes.Buffer
	Table(&buf, []string{"name"}, [][]string{{"x\u009by\u202ez"}})
	if strings.ContainsAny(buf.String(), "\u009b\u202e") || !strings.Contains(buf.String(), `x\u009by\u202ez`) {
		t.Errorf("table = %q, want C1 and bidi controls escaped", buf.String())
	}
}

func TestSafeJSON_EscapesOnlyTerminalControlsAndStaysEquivalent(t *testing.T) {
	input := []byte("{\n  \"a\": \"x\u009by\u202ez ü\"\n}")
	got := SafeJSON(input)
	want := "{\n  \"a\": \"x\\u009by\\u202ez ü\"\n}"
	if string(got) != want {
		t.Errorf("SafeJSON = %q, want %q", got, want)
	}
}

func TestErrorHuman_EscapesServerControlCharacters(t *testing.T) {
	var buf bytes.Buffer
	Error(&buf, &cliErrors.Error{Code: "X-1", Message: "bad\u009b2J\u202eevil\nError: fake"}, false)
	got := buf.String()
	if strings.ContainsAny(got, "\u009b\u202e") || strings.Count(got, "\n") != 1 {
		t.Errorf("Error human output = %q, want controls escaped on one line", got)
	}
}
