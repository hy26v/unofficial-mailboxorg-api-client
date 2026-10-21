// ABOUTME: Bounded fuzz/property coverage for structured JSON projection and rendering.
// ABOUTME: It guards strict documents, exact numbers, valid output, and selector behavior.
package render

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func FuzzTransformJSON(f *testing.F) {
	for _, seed := range []struct {
		input    string
		selector string
		format   uint8
	}{
		{`{"items":[{"id":"1","zero":0,"null":null}],"count":1,"truncated":false}`, "id,zero,null", 0},
		{`{"sequence":9007199254740993}`, "", 0},
		{`{"rows":[{"title":"line\nbreak","value":0}]}`, "title,value", 2},
		{`[{"id":"1"},{"id":"2"}]`, "id", 1},
		{`{"owner":{"emails":["a@example.test"]}}`, "owner.emails.0", 3},
		{`{"id":"1"} {"id":"2"}`, "", 0},
		{`null`, "missing", 0},
		{`not json`, "", 0},
	} {
		f.Add([]byte(seed.input), seed.selector, seed.format)
	}

	formats := [...]StructuredFormat{FormatJSON, FormatJSONL, FormatTSV, FormatValues}
	f.Fuzz(func(t *testing.T, input []byte, selectorText string, formatSeed uint8) {
		if len(input) > 64<<10 || len(selectorText) > 512 {
			t.Skip()
		}

		selectors := make([]string, 0, 8)
		for _, selector := range strings.Split(selectorText, ",") {
			if selector == "" {
				continue
			}
			if len(selector) > 96 || len(selectors) == cap(selectors) {
				t.Skip()
			}
			selectors = append(selectors, selector)
		}
		format := formats[int(formatSeed)%len(formats)]

		var output bytes.Buffer
		err := TransformJSON(&output, input, format, selectors)
		inputRoot, inputErr := decodeOneJSON(input)
		if err != nil {
			return
		}
		if inputErr != nil {
			t.Fatalf("invalid or multi-document input became success: %q", input)
		}

		switch format {
		case FormatJSON:
			outputRoot, outputErr := decodeOneJSON(output.Bytes())
			if outputErr != nil {
				t.Fatalf("JSON transform produced invalid output %q: %v", output.Bytes(), outputErr)
			}
			if len(selectors) == 0 && !reflect.DeepEqual(outputRoot, inputRoot) {
				t.Fatalf("unprojected JSON changed value or exact number: got %#v want %#v", outputRoot, inputRoot)
			}
		case FormatJSONL:
			scanner := bufio.NewScanner(bytes.NewReader(output.Bytes()))
			scanner.Buffer(make([]byte, 1024), 256<<10)
			for scanner.Scan() {
				if _, lineErr := decodeOneJSON(scanner.Bytes()); lineErr != nil {
					t.Fatalf("JSONL transform produced invalid row %q: %v", scanner.Bytes(), lineErr)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("could not scan JSONL output: %v", err)
			}
		case FormatTSV, FormatValues:
			if bytes.ContainsRune(output.Bytes(), '\x00') {
				t.Fatalf("delimited output contained a raw NUL: %q", output.Bytes())
			}
		}
	})
}

func decodeOneJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return value, nil
}
