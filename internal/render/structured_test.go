// ABOUTME: Tests for token-efficient transformations of existing compact JSON output.
// ABOUTME: Covers field projection plus JSONL, TSV, and headerless value modes.
package render

import (
	"bytes"
	"strings"
	"testing"
)

func TestTransformJSON_JSONLUnwrapsSingleCollectionAndSelectsFields(t *testing.T) {
	input := []byte(`{"messages":[{"id":"1","subject":"Hello","body":"long"},{"id":"2","subject":"World","body":"longer"}]}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatJSONL, []string{"id", "subject"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	got := out.String()
	want := "{\"id\":\"1\",\"subject\":\"Hello\"}\n{\"id\":\"2\",\"subject\":\"World\"}\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Contains(got, "body") || strings.Contains(got, "messages") {
		t.Fatalf("projection retained omitted wrapper/fields: %q", got)
	}
}

func TestTransformJSON_TSVUsesSelectionOrderAndEscapesCells(t *testing.T) {
	input := []byte(`{"rows":[{"id":1,"title":"hello\tworld"},{"id":2,"title":"line\nbreak"}]}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatTSV, []string{"title", "id"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	want := "title\tid\nhello\\tworld\t1\nline\\nbreak\t2\n"
	if got := out.String(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_TSVEscapesHeaderControlCharacters(t *testing.T) {
	input := []byte(`{"rows":[{"line\nbreak":"x","tab\tname":"y","nul\u0000name":"z"}]}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatTSV, nil)
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	want := "line\\nbreak\tnul\\u0000name\ttab\\tname\nx\tz\ty\n"
	if got := out.String(); got != want {
		t.Fatalf("got %q, want escaped one-line header %q", got, want)
	}
}

func TestTransformJSON_ValuesOmitsHeader(t *testing.T) {
	input := []byte(`{"commands":[{"name":"quota.get"},{"name":"whoami"}]}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatValues, []string{"name"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	if got, want := out.String(), "quota.get\nwhoami\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_JSONPreservesCollectionWrapperWhenSelecting(t *testing.T) {
	input := []byte(`{"messages":[{"id":"1","subject":"Hello"}]}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatJSON, []string{"id"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	if got, want := out.String(), "{\"messages\":[{\"id\":\"1\"}]}\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_MetadataWrappedCollectionBecomesRows(t *testing.T) {
	input := []byte(`{"items":[{"id":"1","subject":"Hello"},{"id":"2","subject":"World"}],"count":2,"truncated":false}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatTSV, []string{"id", "subject"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	if got, want := out.String(), "id\tsubject\n1\tHello\n2\tWorld\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_PaginationAndCheckpointMetadataRemainRowWrappers(t *testing.T) {
	for name, input := range map[string]string{
		"pagination": `{"items":[{"id":"1"},{"id":"2"}],"count":2,"has_more":true,"next_offset":2,"next_cursor":"offset:2"}`,
		"checkpoint": `{"items":[{"status":"moved"}],"count":1,"moved":1,"resumed":0,"checkpoint":"moves.jsonl"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := TransformJSON(&out, []byte(input), FormatJSONL, nil); err != nil {
				t.Fatalf("TransformJSON failed: %v", err)
			}
			if strings.Contains(out.String(), `"items"`) {
				t.Fatalf("metadata wrapper was not unwrapped: %s", out.String())
			}
		})
	}
}

func TestTransformJSON_JSONProjectionDropsUnselectedCollectionMetadata(t *testing.T) {
	input := []byte(`{"tasks":[{"id":"1","title":"Test"}],"truncated":false}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatJSON, []string{"id"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	if got, want := out.String(), "{\"tasks\":[{\"id\":\"1\"}]}\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_DetailObjectArrayIsNotTreatedAsCollection(t *testing.T) {
	input := []byte(`{"id":4,"display_name":"Team","members":[2,3]}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatTSV, []string{"id", "display_name"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	if got, want := out.String(), "id\tdisplay_name\n4\tTeam\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_DetailObjectStaysOneJSONLRowWithoutSelection(t *testing.T) {
	input := []byte(`{"id":4,"display_name":"Team","members":[2,3]}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatJSONL, nil)
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	if got, want := out.String(), "{\"display_name\":\"Team\",\"id\":4,\"members\":[2,3]}\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_SelectsRootArrayFieldAsOneValue(t *testing.T) {
	input := []byte(`{"capabilities":["mail","calendar"]}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatValues, []string{"capabilities"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	if got, want := out.String(), "[\"mail\",\"calendar\"]\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_NestedSelection(t *testing.T) {
	input := []byte(`{"id":"1","owner":{"email":"a@example.test","name":"A"}}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatTSV, []string{"id", "owner.email"})
	if err != nil {
		t.Fatalf("TransformJSON failed: %v", err)
	}

	if got, want := out.String(), "id\towner.email\n1\ta@example.test\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTransformJSON_UnknownSelectionReturnsError(t *testing.T) {
	input := []byte(`{"id":"1"}`)
	var out bytes.Buffer

	err := TransformJSON(&out, input, FormatJSON, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error = %v, want missing-field error", err)
	}
}

func TestTransformJSON_RejectsMultipleDocuments(t *testing.T) {
	var out bytes.Buffer
	err := TransformJSON(&out, []byte(`{"id":"1"} {"id":"2"}`), FormatJSON, nil)
	if err == nil || !strings.Contains(err.Error(), "more than one JSON document") {
		t.Fatalf("error = %v, want multiple-document error", err)
	}
}

func TestParseFormat_AcceptsAliasesAndRejectsUnknown(t *testing.T) {
	for input, want := range map[string]StructuredFormat{
		"json":   FormatJSON,
		"jsonl":  FormatJSONL,
		"ndjson": FormatJSONL,
		"tsv":    FormatTSV,
		"value":  FormatValues,
		"values": FormatValues,
	} {
		got, err := ParseFormat(input)
		if err != nil {
			t.Errorf("ParseFormat(%q): %v", input, err)
		} else if got != want {
			t.Errorf("ParseFormat(%q) = %q, want %q", input, got, want)
		}
	}

	if _, err := ParseFormat("yaml"); err == nil {
		t.Fatal("ParseFormat(yaml) succeeded, want error")
	}
}
