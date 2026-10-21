// ABOUTME: Unit tests for strict mail inputs and responses, contact/task rows, deletes,
// ABOUTME: and settings-value decoders.
package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func rawResponseRow(t *testing.T, values ...any) []json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("marshaling row: %v", err)
	}
	var row []json.RawMessage
	if err := json.Unmarshal(encoded, &row); err != nil {
		t.Fatalf("decoding raw row: %v", err)
	}
	return row
}

func TestDecodeMailSendID_RequiresNonEmptyString(t *testing.T) {
	id, err := decodeMailSendID([]byte(`"default0/INBOX/999"`))
	if err != nil || id != "default0/INBOX/999" {
		t.Fatalf("decodeMailSendID(valid) = %q, %v", id, err)
	}

	for _, raw := range []string{`null`, `""`, `"   "`, `{}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := decodeMailSendID([]byte(raw)); err == nil || err.Code != "invalid_response" {
				t.Errorf("decodeMailSendID(%s) error = %v, want invalid_response", raw, err)
			}
		})
	}
}

func TestBuildMailSendInput_UsesStrictObjectSchema(t *testing.T) {
	valid := `{"to":["a@example.com"],"cc":[],"bcc":[],"subject":"hello","body":"body","attachments":[]}`
	msg, err := buildMailSendInput(valid, "", false, nil, nil, nil, "", false, "", false, nil)
	if err != nil {
		t.Fatalf("buildMailSendInput(valid) error = %v", err)
	}
	if len(msg.To) != 1 || msg.Subject != "hello" {
		t.Errorf("buildMailSendInput(valid) = %+v", msg)
	}

	for name, input := range map[string]string{
		"unknown field": `{"to":["a@example.com"],"subject":"hello","recipients":[]}`,
		"null":          `null`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := buildMailSendInput(input, "", false, nil, nil, nil, "", false, "", false, nil); err == nil {
				t.Fatalf("buildMailSendInput(%s) error = nil, want schema error", input)
			}
		})
	}
}

// decodeDefaultMailListRow decodes a row for mail list's default columns (id, subject, from,
// received date) through the same decoder the command uses.
func decodeDefaultMailListRow(row []json.RawMessage) (mailListItem, error) {
	return decodeMailListRowWithColumns(row, mailListColumnsFor(mailListOptions{}), mailListOptions{})
}

func TestDecodeMailListRow_RejectsNullReceivedDate(t *testing.T) {
	row := rawResponseRow(t, "1", "Subject", [][]string{{"Sender", "sender@example.com"}}, nil)
	if item, err := decodeDefaultMailListRow(row); err == nil {
		t.Fatalf("decodeDefaultMailListRow(null received date) = %+v, nil; want error", item)
	}
}

func TestDecodeMailListRow_RejectsNonPositiveReceivedDate(t *testing.T) {
	for _, date := range []int64{0, -1} {
		row := rawResponseRow(t, "1", "Subject", [][]string{{"Sender", "sender@example.com"}}, date)
		if item, err := decodeDefaultMailListRow(row); err == nil {
			t.Errorf("decodeDefaultMailListRow(received date %d) = %+v, nil; want error", date, item)
		}
	}
}

func TestDecodeMailListRow_RejectsNullSubject(t *testing.T) {
	row := rawResponseRow(t, "1", nil, [][]string{{"Sender", "sender@example.com"}}, int64(1784851568000))
	if item, err := decodeDefaultMailListRow(row); err == nil {
		t.Fatalf("decodeDefaultMailListRow(null subject) = %+v, nil; want error", item)
	}
}

func TestDecodeMailListRow_RejectsMalformedSenderPair(t *testing.T) {
	row := rawResponseRow(t, "1", "Subject", [][]string{{"Sender"}}, int64(1784851568000))
	if item, err := decodeDefaultMailListRow(row); err == nil {
		t.Fatalf("decodeDefaultMailListRow(short sender pair) = %+v, nil; want error", item)
	}
}

func TestDecodeMailGetRow_RejectsNegativeSize(t *testing.T) {
	row := rawResponseRow(t,
		"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
		-1, int64(1784851568000), int64(1784851574000), false, 0,
	)
	if result, err := decodeMailGetRow(row); err == nil {
		t.Fatalf("decodeMailGetRow(negative size) = %+v, nil; want error", result)
	}
}

func TestDecodeMailGetRow_RejectsNullSize(t *testing.T) {
	row := rawResponseRow(t,
		"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
		nil, int64(1784851568000), int64(1784851574000), false, 0,
	)
	if result, err := decodeMailGetRow(row); err == nil {
		t.Fatalf("decodeMailGetRow(null size) = %+v, nil; want error", result)
	}
}

func TestDecodeMailGetRow_AcceptsMissingDates(t *testing.T) {
	for name, row := range map[string][]json.RawMessage{
		"sent": rawResponseRow(t,
			"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
			1, nil, int64(1784851574000), false, 0,
		),
		"received": rawResponseRow(t,
			"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
			1, int64(1784851568000), nil, false, 0,
		),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := decodeMailGetRow(row)
			if err != nil {
				t.Fatalf("decodeMailGetRow(missing %s date) error = %v", name, err)
			}
			if name == "sent" && (result.SentDate != nil || result.ReceivedDate == nil) {
				t.Fatalf("decodeMailGetRow(missing sent date) dates = %v, %v", result.SentDate, result.ReceivedDate)
			}
			if name == "received" && (result.SentDate == nil || result.ReceivedDate != nil) {
				t.Fatalf("decodeMailGetRow(missing received date) dates = %v, %v", result.SentDate, result.ReceivedDate)
			}
		})
	}
}

func TestDecodeMailGetRow_RejectsNonPositiveDates(t *testing.T) {
	for name, row := range map[string][]json.RawMessage{
		"zero sent": rawResponseRow(t,
			"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
			1, int64(0), int64(1784851574000), false, 0,
		),
		"negative sent": rawResponseRow(t,
			"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
			1, int64(-1), int64(1784851574000), false, 0,
		),
		"zero received": rawResponseRow(t,
			"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
			1, int64(1784851568000), int64(0), false, 0,
		),
		"negative received": rawResponseRow(t,
			"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
			1, int64(1784851568000), int64(-1), false, 0,
		),
	} {
		t.Run(name, func(t *testing.T) {
			if result, err := decodeMailGetRow(row); err == nil {
				t.Fatalf("decodeMailGetRow(%s) = %+v, nil; want error", name, result)
			}
		})
	}
}

func TestDecodeMailGetRow_RejectsNullAttachmentFlag(t *testing.T) {
	row := rawResponseRow(t,
		"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
		1, int64(1784851568000), int64(1784851574000), nil, 0,
	)
	if result, err := decodeMailGetRow(row); err == nil {
		t.Fatalf("decodeMailGetRow(null attachment flag) = %+v, nil; want error", result)
	}
}

func TestDecodeMailGetRow_RejectsNullSubject(t *testing.T) {
	row := rawResponseRow(t,
		"7", "default0/INBOX", nil, []any{}, []any{}, []any{},
		1, int64(1784851568000), int64(1784851574000), false, 0,
	)
	if result, err := decodeMailGetRow(row); err == nil {
		t.Fatalf("decodeMailGetRow(null subject) = %+v, nil; want error", result)
	}
}

func TestDecodeMailGetRow_RejectsInvalidFlags(t *testing.T) {
	for name, flags := range map[string]any{
		"null":     nil,
		"negative": -1,
	} {
		t.Run(name, func(t *testing.T) {
			row := rawResponseRow(t,
				"7", "default0/INBOX", "subject", []any{}, []any{}, []any{},
				1, int64(1784851568000), int64(1784851574000), false, flags,
			)
			if result, err := decodeMailGetRow(row); err == nil {
				t.Fatalf("decodeMailGetRow(%s flags) = %+v, nil; want error", name, result)
			}
		})
	}
}

func TestDecodeMailAddresses_RejectsMalformedPairWidths(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"short": []byte(`[["Sender"]]`),
		"wide":  []byte(`[["Sender","sender@example.com","unexpected"]]`),
	} {
		t.Run(name, func(t *testing.T) {
			if addresses, err := decodeMailAddresses(raw); err == nil {
				t.Fatalf("decodeMailAddresses(%s) = %+v, nil; want exact two-column pairs", raw, addresses)
			}
		})
	}
}

func TestDecodeMailAddresses_RejectsMissingAddresses(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{
		"null":       []byte(`[["Sender",null]]`),
		"empty":      []byte(`[["Sender",""]]`),
		"whitespace": []byte(`[["Sender","  "]]`),
	} {
		t.Run(name, func(t *testing.T) {
			if addresses, err := decodeMailAddresses(raw); err == nil {
				t.Fatalf("decodeMailAddresses(%s) = %+v, nil; want missing address rejected", raw, addresses)
			}
		})
	}
}

func TestDecodeMailMoveResult_UsesReturnedIDAndPreservesLegacyFallbacks(t *testing.T) {
	result, err := decodeMailMoveResult([]byte(`{"folder_id":"archive","id":"99"}`), "7", "archive")
	if err != nil || result.ID != "99" || result.FolderID != "archive" {
		t.Fatalf("decodeMailMoveResult(server id) = %+v, %v", result, err)
	}

	result, err = decodeMailMoveResult([]byte(`{}`), "7", "archive")
	if err != nil || result.ID != "7" || result.FolderID != "archive" {
		t.Fatalf("decodeMailMoveResult(legacy response) = %+v, %v", result, err)
	}
}

func TestDecodeMailMoveResult_RejectsWhitespaceReturnedID(t *testing.T) {
	if _, err := decodeMailMoveResult([]byte(`{"id":" "}`), "7", "archive"); err == nil || err.Code != "invalid_response" {
		t.Fatalf("decodeMailMoveResult(whitespace id) error = %v, want invalid_response", err)
	}
}

func TestDecodeMailCopyResult_RequiresNewID(t *testing.T) {
	result, err := decodeMailCopyResult([]byte(`{"id":"42"}`), "archive")
	if err != nil || result.ID != "42" || result.FolderID != "archive" {
		t.Fatalf("decodeMailCopyResult(valid) = %+v, %v", result, err)
	}
	if _, err := decodeMailCopyResult([]byte(`{"folder_id":"archive"}`), "archive"); err == nil || err.Code != "invalid_response" {
		t.Fatalf("decodeMailCopyResult(missing id) error = %v, want invalid_response", err)
	}
}

func TestRowToContact_ValidatesShapeAndTypes(t *testing.T) {
	ct, err := rowToContact(rawResponseRow(t, "2", "Display", "", "", ""))
	if err != nil {
		t.Fatalf("rowToContact(valid optional blanks) error = %v", err)
	}
	if ct.ID != "2" || ct.DisplayName != "Display" {
		t.Errorf("rowToContact(valid) = %+v", ct)
	}

	for name, row := range map[string][]json.RawMessage{
		"short":    rawResponseRow(t, "2", "Display"),
		"bad type": rawResponseRow(t, 2, "Display", "", "", ""),
		"empty id": rawResponseRow(t, "", "Display", "", "", ""),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := rowToContact(row); err == nil {
				t.Fatalf("rowToContact(%s) error = nil", name)
			}
		})
	}
}

func TestDecodeTaskListRow_ValidatesShapeAndTypes(t *testing.T) {
	item, err := decodeTaskListRow(rawResponseRow(t, 4, "Task", nil, 1))
	if err != nil {
		t.Fatalf("decodeTaskListRow(valid null end date) error = %v", err)
	}
	if item.ID != "4" || item.Title != "Task" || item.EndDate != nil || item.Status == nil || *item.Status != 1 {
		t.Errorf("decodeTaskListRow(valid) = %+v", item)
	}
	item, err = decodeTaskListRow(rawResponseRow(t, 4, "Task", nil, nil))
	if err != nil {
		t.Fatalf("decodeTaskListRow(valid null status) error = %v", err)
	}
	if item.Status != nil {
		t.Errorf("decodeTaskListRow(null status).Status = %v, want nil", *item.Status)
	}

	for name, row := range map[string][]json.RawMessage{
		"short":         rawResponseRow(t, 4, "Task"),
		"null id":       rawResponseRow(t, nil, "Task", nil, 1),
		"bad title":     rawResponseRow(t, 4, 5, nil, 1),
		"bad end date":  rawResponseRow(t, 4, "Task", "tomorrow", 1),
		"zero end date": rawResponseRow(t, 4, "Task", 0, 1),
		"bad status":    rawResponseRow(t, 4, "Task", nil, "done"),
		"status zero":   rawResponseRow(t, 4, "Task", nil, 0),
		"status six":    rawResponseRow(t, 4, "Task", nil, 6),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTaskListRow(row); err == nil {
				t.Fatalf("decodeTaskListRow(%s) error = nil", name)
			}
		})
	}
}

func TestDecodeTaskListRow_RejectsNullTitle(t *testing.T) {
	row := rawResponseRow(t, 4, nil, nil, 1)
	if item, err := decodeTaskListRow(row); err == nil {
		t.Fatalf("decodeTaskListRow(null title) = %+v, nil; want error", item)
	}
}

func TestDecodeFileListItem_RejectsEmptyID(t *testing.T) {
	row := rawResponseRow(t, "", "36", "report.pdf", "report.pdf", "application/pdf", 1024)
	if item, err := decodeFileListItem(row); err == nil {
		t.Fatalf("decodeFileListItem(empty id) = %+v, nil; want error", item)
	}
}

func TestDecodeFileListItem_RejectsEmptyFolderID(t *testing.T) {
	row := rawResponseRow(t, "36/2", "", "report.pdf", "report.pdf", "application/pdf", 1024)
	if item, err := decodeFileListItem(row); err == nil {
		t.Fatalf("decodeFileListItem(empty folder id) = %+v, nil; want error", item)
	}
}

func TestDecodeFileListItem_RejectsNegativeSize(t *testing.T) {
	row := rawResponseRow(t, "36/2", "36", "report.pdf", "report.pdf", "application/pdf", -1)
	if item, err := decodeFileListItem(row); err == nil {
		t.Fatalf("decodeFileListItem(negative size) = %+v, nil; want error", item)
	}
}

func TestDecodeFileListItem_RejectsNullTitle(t *testing.T) {
	row := rawResponseRow(t, "36/2", "36", nil, "report.pdf", "application/pdf", 1024)
	if item, err := decodeFileListItem(row); err == nil {
		t.Fatalf("decodeFileListItem(null title) = %+v, nil; want error", item)
	}
}

func TestDecodeFileListItem_RejectsNullFilename(t *testing.T) {
	row := rawResponseRow(t, "36/2", "36", "report.pdf", nil, "application/pdf", 1024)
	if item, err := decodeFileListItem(row); err == nil {
		t.Fatalf("decodeFileListItem(null filename) = %+v, nil; want error", item)
	}
}

func TestDecodeFileListItem_RejectsNullMIMEType(t *testing.T) {
	row := rawResponseRow(t, "36/2", "36", "report.pdf", "report.pdf", nil, 1024)
	if item, err := decodeFileListItem(row); err == nil {
		t.Fatalf("decodeFileListItem(null MIME type) = %+v, nil; want error", item)
	}
}

func TestDecodeFileListItem_RejectsNullSize(t *testing.T) {
	row := rawResponseRow(t, "36/2", "36", "report.pdf", "report.pdf", "application/pdf", nil)
	if item, err := decodeFileListItem(row); err == nil {
		t.Fatalf("decodeFileListItem(null size) = %+v, nil; want error", item)
	}
}

func TestDecodeFolderListItem_RejectsEmptyID(t *testing.T) {
	row := rawResponseRow(t, "", "Calendar", "calendar", 1, false)
	if item, err := decodeFolderListItem(row); err == nil {
		t.Fatalf("decodeFolderListItem(empty id) = %+v, nil; want error", item)
	}
}

func TestDecodeFolderListItem_RejectsNullTitle(t *testing.T) {
	row := rawResponseRow(t, "default0/Calendar", nil, "calendar", 1, false)
	if item, err := decodeFolderListItem(row); err == nil {
		t.Fatalf("decodeFolderListItem(null title) = %+v, nil; want error", item)
	}
}

func TestDecodeFolderListItem_RejectsNullModule(t *testing.T) {
	row := rawResponseRow(t, "default0/Calendar", "Calendar", nil, 1, false)
	if item, err := decodeFolderListItem(row); err == nil {
		t.Fatalf("decodeFolderListItem(null module) = %+v, nil; want error", item)
	}
}

func TestDecodeFolderListItem_RejectsNullType(t *testing.T) {
	row := rawResponseRow(t, "default0/Calendar", "Calendar", "calendar", nil, false)
	if item, err := decodeFolderListItem(row); err == nil {
		t.Fatalf("decodeFolderListItem(null type) = %+v, nil; want error", item)
	}
}

func TestDecodeFolderListItem_RejectsNullSubfolderFlag(t *testing.T) {
	row := rawResponseRow(t, "default0/Calendar", "Calendar", "calendar", 1, nil)
	if item, err := decodeFolderListItem(row); err == nil {
		t.Fatalf("decodeFolderListItem(null subfolder flag) = %+v, nil; want error", item)
	}
}

func TestDecodeAttachmentRows_RejectsNegativeSizes(t *testing.T) {
	for name, raw := range map[string]string{
		"object": `[{"id":1,"filename":"a.txt","file_size":-1,"file_mimetype":"text/plain"}]`,
		"tuple":  `[[1,"a.txt",-1,"text/plain"]]`,
	} {
		t.Run(name, func(t *testing.T) {
			if rows, err := decodeAttachmentRows([]byte(raw)); err == nil {
				t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want error", raw, rows)
			}
		})
	}
}

func TestDecodeAttachmentRows_RejectsNullTupleSize(t *testing.T) {
	raw := []byte(`[[1,"a.txt",null,"text/plain"]]`)
	if rows, err := decodeAttachmentRows(raw); err == nil {
		t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want null size rejected", raw, rows)
	}
}

func TestDecodeAttachmentRows_RejectsUnexpectedExtraColumns(t *testing.T) {
	raw := []byte(`[[1,"a.txt",4,"text/plain","unexpected"]]`)
	if rows, err := decodeAttachmentRows(raw); err == nil {
		t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want exact four-column schema", raw, rows)
	}
}

func TestDecodeAttachmentRows_RejectsNullTupleFilename(t *testing.T) {
	raw := []byte(`[[1,null,4,"text/plain"]]`)
	if rows, err := decodeAttachmentRows(raw); err == nil {
		t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want null filename rejected", raw, rows)
	}
}

func TestDecodeAttachmentRows_RejectsBlankFilenames(t *testing.T) {
	for name, raw := range map[string]string{
		"object": `[{"id":1,"filename":"   ","file_size":4,"file_mimetype":"text/plain"}]`,
		"tuple":  `[[1,"   ",4,"text/plain"]]`,
	} {
		t.Run(name, func(t *testing.T) {
			if rows, err := decodeAttachmentRows([]byte(raw)); err == nil {
				t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want blank filename rejected", raw, rows)
			}
		})
	}
}

func TestDecodeAttachmentRows_RejectsNullTupleMIMEType(t *testing.T) {
	raw := []byte(`[[1,"a.txt",4,null]]`)
	if rows, err := decodeAttachmentRows(raw); err == nil {
		t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want null MIME type rejected", raw, rows)
	}
}

func TestDecodeAttachmentRows_RejectsBlankMIMETypes(t *testing.T) {
	for name, raw := range map[string]string{
		"object": `[{"id":1,"filename":"a.txt","file_size":4,"file_mimetype":"   "}]`,
		"tuple":  `[[1,"a.txt",4,"   "]]`,
	} {
		t.Run(name, func(t *testing.T) {
			if rows, err := decodeAttachmentRows([]byte(raw)); err == nil {
				t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want blank MIME type rejected", raw, rows)
			}
		})
	}
}

func TestDecodeAttachmentRows_RejectsObjectWithoutSize(t *testing.T) {
	raw := []byte(`[{"id":1,"filename":"a.txt","file_mimetype":"text/plain"}]`)
	if rows, err := decodeAttachmentRows(raw); err == nil {
		t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want missing size rejected", raw, rows)
	}
}

func TestDecodeAttachmentRows_RejectsNullObjectFilename(t *testing.T) {
	raw := []byte(`[{"id":1,"filename":null,"file_size":4,"file_mimetype":"text/plain"}]`)
	if rows, err := decodeAttachmentRows(raw); err == nil {
		t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want null filename rejected", raw, rows)
	}
}

func TestDecodeAttachmentRows_RejectsNullObjectMIMEType(t *testing.T) {
	raw := []byte(`[{"id":1,"filename":"a.txt","file_size":4,"file_mimetype":null}]`)
	if rows, err := decodeAttachmentRows(raw); err == nil {
		t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want null MIME type rejected", raw, rows)
	}
}

func TestDeleteRefusalError_PreservesLegacyAcceptedResponseShapes(t *testing.T) {
	if err := deleteRefusalError([]byte(`[]`), "task 4"); err != nil {
		t.Errorf("empty refusal list error = %v, want nil", err)
	}
	if err := deleteRefusalError([]byte(`{}`), "task 4"); err != nil {
		t.Errorf("legacy object success error = %v, want nil", err)
	}
	err := deleteRefusalError([]byte(`[{"id":"4"}]`), "task 4")
	if err == nil || err.Code != "api_error" || !strings.Contains(err.Message, "not deleted") {
		t.Errorf("non-empty refusal list error = %v, want api_error", err)
	}
}

func TestChronosDeleteResultError_DecodesPerItemContract(t *testing.T) {
	success := []byte(`[{
		"folderId":"cal://0/32",
		"id":"evt-1",
		"deleted":[{"folder":"cal://0/32","id":"evt-1"}]
	}]`)
	if err := chronosDeleteResultError(success, "evt-1", "cal://0/32"); err != nil {
		t.Fatalf("successful CalendarResults error = %v, want nil", err)
	}

	failed := []byte(`[{
		"folderId":"cal://0/32",
		"id":"evt-1",
		"error":{
			"error":"event changed since it was read",
			"code":"CAL-4020",
			"categories":"TRY_AGAIN",
			"error_id":"req-delete-1"
		}
	}]`)
	err := chronosDeleteResultError(failed, "evt-1", "cal://0/32")
	if err == nil {
		t.Fatal("nested CalendarResult error = nil")
	}
	if err.Code != "CAL-4020" || err.Message != "event changed since it was read" ||
		!err.Retryable || err.RequestID != "req-delete-1" || err.HTTPStatus != 200 {
		t.Errorf("nested CalendarResult error = %+v, want preserved provider fields", err)
	}

	for name, data := range map[string][]byte{
		"not an array": []byte(`{}`),
		"empty array":  []byte(`[]`),
	} {
		t.Run(name, func(t *testing.T) {
			err := chronosDeleteResultError(data, "evt-1", "cal://0/32")
			if err == nil || err.Code != "invalid_response" {
				t.Errorf("chronosDeleteResultError(%s) = %v, want invalid_response", data, err)
			}
		})
	}
}

func TestSettingsValue_DistinguishesMissingFromExplicitNull(t *testing.T) {
	tree := map[string]json.RawMessage{
		"null_value": json.RawMessage(`null`),
		"name":       json.RawMessage(`"thread"`),
	}
	value, err := settingsValue(tree, "null_value")
	if err != nil || value != nil {
		t.Fatalf("settingsValue(explicit null) = %#v, %v, want nil value without error", value, err)
	}
	value, err = settingsValue(tree, "name")
	if err != nil || value != "thread" {
		t.Fatalf("settingsValue(name) = %#v, %v", value, err)
	}
	if _, err := settingsValue(tree, "missing"); err == nil || err.Code != "not_found" {
		t.Fatalf("settingsValue(missing) error = %v, want not_found", err)
	}
}
