// ABOUTME: Human and LLM output rendering: compact deterministic JSON for LLM mode,
// ABOUTME: readable key/value tables for human mode, and error rendering in both modes.
package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

// Field is one human-readable name/value pair, rendered in declaration order.
type Field struct {
	Name  string
	Value string
}

// CompactJSON writes v as compact, deterministic JSON (no indentation, no HTML escaping)
// followed by a newline, for LLM mode.
func CompactJSON(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// KeyValueTable writes fields as an aligned "name  value" table for human mode.
func KeyValueTable(w io.Writer, fields []Field) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, f := range fields {
		fmt.Fprintf(tw, "%s\t%s\n", escapeTableCell(f.Name), escapeTableCell(f.Value))
	}
	return tw.Flush()
}

// Table writes header and rows as an aligned, tab-separated table for human mode.
func Table(w io.Writer, header []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, joinTabs(header))
	for _, row := range rows {
		fmt.Fprintln(tw, joinTabs(row))
	}
	return tw.Flush()
}

func joinTabs(cols []string) string {
	var buf bytes.Buffer
	for i, c := range cols {
		if i > 0 {
			buf.WriteByte('\t')
		}
		buf.WriteString(escapeTableCell(c))
	}
	return buf.String()
}

func escapeTableCell(value string) string {
	return SafeText(value)
}

// SafeText makes server-provided text safe to print on a terminal as one line. It
// escapes backslash, tab, CR and LF as \\, \t, \r and \n, and writes every other
// character that a terminal can interpret as \uXXXX: C0 controls, DEL, C1 controls
// (U+0080-U+009F, which include the 8-bit CSI) and the Unicode bidirectional controls
// that can reorder what the reader sees. Use it for every server value printed outside
// render's tables, which apply it already.
func SafeText(value string) string {
	var escaped strings.Builder
	for _, char := range value {
		switch char {
		case '\\':
			escaped.WriteString(`\\`)
		case '\t':
			escaped.WriteString(`\t`)
		case '\r':
			escaped.WriteString(`\r`)
		case '\n':
			escaped.WriteString(`\n`)
		default:
			if unsafeTerminalRune(char) {
				writeUnicodeEscape(&escaped, char)
			} else {
				escaped.WriteRune(char)
			}
		}
	}
	return escaped.String()
}

// SafeMultilineText escapes the same terminal controls as SafeText but keeps line breaks
// and tabs, drops carriage returns, and leaves backslashes as they are. Use it for free
// text such as a message body that is printed outside a table.
func SafeMultilineText(value string) string {
	var escaped strings.Builder
	for _, char := range value {
		switch {
		case char == '\n' || char == '\t':
			escaped.WriteRune(char)
		case char == '\r':
		case unsafeTerminalRune(char):
			writeUnicodeEscape(&escaped, char)
		default:
			escaped.WriteRune(char)
		}
	}
	return escaped.String()
}

// SafeJSON escapes, inside already encoded JSON, the C1 and bidirectional control
// characters that encoding/json leaves as raw UTF-8. Such characters can only occur in
// JSON strings, so the \uXXXX form decodes to the same value. It is for human-mode JSON
// dumps; machine (--llm) output is written unchanged.
func SafeJSON(data []byte) []byte {
	var escaped strings.Builder
	for _, char := range string(data) {
		if char >= 0x80 && unsafeTerminalRune(char) {
			writeUnicodeEscape(&escaped, char)
		} else {
			escaped.WriteRune(char)
		}
	}
	return []byte(escaped.String())
}

func unsafeTerminalRune(char rune) bool {
	switch {
	case char < 0x20, char == 0x7f:
		return true
	case char >= 0x80 && char <= 0x9f:
		return true
	case char == 0x200e, char == 0x200f:
		return true
	case char >= 0x202a && char <= 0x202e:
		return true
	case char >= 0x2066 && char <= 0x2069:
		return true
	}
	return false
}

func writeUnicodeEscape(b *strings.Builder, char rune) {
	const hex = "0123456789abcdef"
	b.WriteString(`\u`)
	b.WriteByte(hex[(char>>12)&0xf])
	b.WriteByte(hex[(char>>8)&0xf])
	b.WriteByte(hex[(char>>4)&0xf])
	b.WriteByte(hex[char&0xf])
}

// Error renders a structured error: compact {"error":{...}} JSON in LLM mode, a plain
// human-readable message otherwise. Callers must write this to stderr.
func Error(w io.Writer, err *cliErrors.Error, llm bool) error {
	if llm {
		return CompactJSON(w, struct {
			Error *cliErrors.Error `json:"error"`
		}{Error: err})
	}
	_, werr := fmt.Fprintf(w, "Error: %s (%s)\n", SafeText(err.Message), SafeText(err.Code))
	return werr
}
