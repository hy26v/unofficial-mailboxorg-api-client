// ABOUTME: Shared token-efficient output transforms layered over commands' compact JSON.
// ABOUTME: Supports field projection plus JSON, JSONL, TSV, and headerless values.
package render

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// StructuredFormat is a machine-readable output representation.
type StructuredFormat string

const (
	FormatJSON   StructuredFormat = "json"
	FormatJSONL  StructuredFormat = "jsonl"
	FormatTSV    StructuredFormat = "tsv"
	FormatValues StructuredFormat = "values"
)

// ParseFormat normalizes a user-facing structured format name.
func ParseFormat(value string) (StructuredFormat, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "json":
		return FormatJSON, nil
	case "jsonl", "ndjson":
		return FormatJSONL, nil
	case "tsv":
		return FormatTSV, nil
	case "value", "values":
		return FormatValues, nil
	default:
		return "", fmt.Errorf("unknown format %q (choose json, jsonl, tsv, or values)", value)
	}
}

// TransformJSON converts one compact JSON document into format. A top-level object
// containing a single array is treated as a named collection for row formats, so
// {"messages":[...]} becomes one JSONL/TSV/value row per message. selectFields accepts
// dotted object paths and removes every unselected field before rendering.
func TransformJSON(w io.Writer, input []byte, format StructuredFormat, selectFields []string) error {
	var root any
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return fmt.Errorf("command did not return structured JSON: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return fmt.Errorf("command returned invalid trailing JSON: %w", err)
		}
		return fmt.Errorf("command returned more than one JSON document")
	}

	projected, err := projectRoot(root, selectFields)
	if err != nil {
		return err
	}

	switch format {
	case FormatJSON:
		return encodeCompact(w, keepIncompleteListMetadata(root, projected))
	case FormatJSONL:
		return encodeJSONL(w, projected, selectFields)
	case FormatTSV:
		return encodeDelimited(w, projected, selectFields, true)
	case FormatValues:
		return encodeDelimited(w, projected, selectFields, false)
	default:
		return fmt.Errorf("unsupported structured format %q", format)
	}
}

func encodeCompact(w io.Writer, value any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(value)
}

func projectRoot(root any, fields []string) (any, error) {
	if len(fields) == 0 {
		return root, nil
	}

	if selectorsApplyToRoot(root, fields) {
		projected, found, err := projectValue(root, fields)
		if err != nil {
			return nil, err
		}
		for i, ok := range found {
			if !ok {
				return nil, fmt.Errorf("selected field %q is not present in the result", fields[i])
			}
		}
		return projected, nil
	}

	if wrapper, items, ok := objectCollection(root); ok {
		projected, err := projectItems(items, fields)
		if err != nil {
			return nil, err
		}
		return map[string]any{wrapper: projected}, nil
	}

	if items, ok := root.([]any); ok {
		return projectItems(items, fields)
	}

	projected, found, err := projectValue(root, fields)
	if err != nil {
		return nil, err
	}
	for i, ok := range found {
		if !ok {
			return nil, fmt.Errorf("selected field %q is not present in the result", fields[i])
		}
	}
	return projected, nil
}

// incompleteListMetadata are the collection fields that tell a caller how to get the rest
// of a list. A JSON projection keeps them when has_more or truncated is true, so a
// selection never hides that a list is incomplete.
var incompleteListMetadata = []string{"truncated", "has_more", "available", "next_offset", "next_cursor"}

func keepIncompleteListMetadata(root, projected any) any {
	source, ok := root.(map[string]any)
	if !ok {
		return projected
	}
	target, ok := projected.(map[string]any)
	if !ok || source["has_more"] != true && source["truncated"] != true {
		return projected
	}
	if wrapper, _, isCollection := objectCollection(root); !isCollection || target[wrapper] == nil {
		return projected
	}
	for _, name := range incompleteListMetadata {
		if value, present := source[name]; present {
			if _, selected := target[name]; !selected {
				target[name] = value
			}
		}
	}
	return target
}

func selectorsApplyToRoot(root any, fields []string) bool {
	obj, ok := root.(map[string]any)
	if !ok {
		return false
	}
	for _, field := range fields {
		top := strings.SplitN(field, ".", 2)[0]
		if _, exists := obj[top]; !exists {
			return false
		}
	}
	return true
}

func projectItems(items []any, fields []string) ([]any, error) {
	projected := make([]any, len(items))
	foundAny := make([]bool, len(fields))
	for i, item := range items {
		value, found, err := projectValue(item, fields)
		if err != nil {
			return nil, err
		}
		projected[i] = value
		for j := range found {
			foundAny[j] = foundAny[j] || found[j]
		}
	}
	for i, ok := range foundAny {
		if !ok && len(items) > 0 {
			return nil, fmt.Errorf("selected field %q is not present in the result", fields[i])
		}
	}
	return projected, nil
}

func projectValue(value any, fields []string) (map[string]any, []bool, error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("cannot select object fields from %T result", value)
	}

	result := make(map[string]any, len(fields))
	found := make([]bool, len(fields))
	for i, field := range fields {
		selected, ok := lookupPath(obj, field)
		if ok {
			result[field] = selected
			found[i] = true
		} else {
			result[field] = nil
		}
	}
	return result, found, nil
}

func lookupPath(obj map[string]any, path string) (any, bool) {
	var current any = obj
	for _, part := range strings.Split(path, ".") {
		switch typed := current.(type) {
		case map[string]any:
			next, ok := typed[part]
			if !ok {
				return nil, false
			}
			current = next
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, false
			}
			current = typed[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// objectCollection finds a single array field in a collection envelope. Scalar
// pagination metadata such as count or truncated is allowed beside it. Other fields
// mean this is a detail object that happens to contain an array (for example a group
// with members), so it must remain one row.
func objectCollection(root any) (string, []any, bool) {
	obj, ok := root.(map[string]any)
	if !ok {
		return "", nil, false
	}

	var collectionName string
	var collectionItems []any
	found := false
	for name, value := range obj {
		items, isArray := value.([]any)
		if !isArray {
			continue
		}
		if found {
			return "", nil, false
		}
		collectionName = name
		collectionItems = items
		found = true
	}
	if !found {
		return "", nil, false
	}
	for name := range obj {
		if name == collectionName || isCollectionMetadata(name) {
			continue
		}
		return "", nil, false
	}
	return collectionName, collectionItems, found
}

func isCollectionMetadata(name string) bool {
	switch name {
	case "count", "total", "available", "truncated", "has_more", "offset", "limit",
		"next_offset", "next_cursor", "moved", "resumed", "checkpoint",
		"dry_run", "preflight":
		return true
	default:
		return false
	}
}

func rowValues(root any, selected []string) []any {
	if name, items, ok := objectCollection(root); ok && !containsString(selected, name) {
		return items
	}
	if items, ok := root.([]any); ok {
		return items
	}
	return []any{root}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func encodeJSONL(w io.Writer, root any, selected []string) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, row := range rowValues(root, selected) {
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	return nil
}

func encodeDelimited(w io.Writer, root any, selected []string, includeHeader bool) error {
	rows := rowValues(root, selected)
	columns := append([]string(nil), selected...)
	if len(columns) == 0 {
		columnSet := map[string]struct{}{}
		for _, row := range rows {
			if obj, ok := row.(map[string]any); ok {
				for name := range obj {
					columnSet[name] = struct{}{}
				}
			}
		}
		for name := range columnSet {
			columns = append(columns, name)
		}
		sort.Strings(columns)
		if len(columns) == 0 {
			columns = []string{"value"}
		}
	}

	bw := bufio.NewWriter(w)
	if includeHeader {
		header := make([]string, len(columns))
		for i, column := range columns {
			header[i] = formatCell(column)
		}
		if _, err := bw.WriteString(strings.Join(header, "\t") + "\n"); err != nil {
			return err
		}
	}
	for _, row := range rows {
		cells := make([]string, len(columns))
		if obj, ok := row.(map[string]any); ok {
			for i, column := range columns {
				cells[i] = formatCell(obj[column])
			}
		} else if len(columns) == 1 {
			cells[0] = formatCell(row)
		} else {
			return fmt.Errorf("cannot render %T result with %d selected fields", row, len(columns))
		}
		if _, err := bw.WriteString(strings.Join(cells, "\t") + "\n"); err != nil {
			return err
		}
	}
	return bw.Flush()
}

func formatCell(value any) string {
	var text string
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		text = typed
	case json.Number:
		text = typed.String()
	case bool:
		text = strconv.FormatBool(typed)
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			text = fmt.Sprint(typed)
		} else {
			text = string(data)
		}
	}

	return escapeTableCell(text)
}
