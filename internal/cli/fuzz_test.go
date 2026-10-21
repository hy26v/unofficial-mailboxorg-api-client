// ABOUTME: Bounded fuzz/property targets for CLI JSON, positional, and mutation decoders.
// ABOUTME: They guard exact numbers, required fields, identity matching, and false success.
package cli

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func FuzzJSONInputAndBatchParsing(f *testing.F) {
	f.Add(uint8(0), []byte(`{"sequence":9007199254740993,"null":null,"zero":0}`))
	f.Add(uint8(0), []byte(`{"nested":{"value":1e100}}`))
	f.Add(uint8(1), []byte(`[{"resource":"quota","action":"get","params":{"type":"filestore","sequence":9007199254740993}}]`))
	f.Add(uint8(1), []byte(`[{"resource":"user","action":"get","params":{}}]`))
	f.Add(uint8(2), []byte(`9007199254740993`))
	f.Add(uint8(2), []byte(`{"zero":0,"null":null}`))
	f.Add(uint8(0), []byte(`{"id":1} {"id":2}`))
	f.Add(uint8(1), []byte(`null`))
	f.Add(uint8(2), []byte(`not json`))

	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		if len(data) > 64<<10 || bytes.HasPrefix(data, []byte("@")) {
			t.Skip()
		}
		raw := string(data)

		switch kind % 3 {
		case 0:
			value, err := parseInputValue(raw)
			if err == nil {
				assertExactJSONNumberTree(t, value)
			}
		case 1:
			requests, err := parseBatchInput(raw)
			if err != nil {
				return
			}
			for _, request := range requests {
				assertExactJSONNumberTree(t, request.Params)
			}
			plans, err := planBatchRequests(requests)
			if err != nil {
				return
			}
			for _, plan := range plans {
				request := plan.Provider
				assertExactJSONNumberTree(t, request)
				module, moduleOK := request["module"].(string)
				action, actionOK := request["action"].(string)
				if !moduleOK || !actionOK || strings.TrimSpace(module) == "" || strings.TrimSpace(action) == "" {
					t.Fatalf("translated batch request lost module/action identity: %#v", request)
				}
			}
		case 2:
			value, err := decodeSettingsJSONValue(data)
			if err == nil {
				assertExactJSONNumberTree(t, value)
			}
		}
	})
}

func assertExactJSONNumberTree(t *testing.T, root any) {
	t.Helper()
	encoded, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("successfully parsed value could not be marshaled: %v", err)
	}
	stack := []any{root}
	for nodes := 0; len(stack) > 0; nodes++ {
		if nodes > 100000 {
			t.Fatal("parsed JSON tree exceeded bounded node count")
		}
		value := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch typed := value.(type) {
		case map[string]any:
			for _, child := range typed {
				stack = append(stack, child)
			}
		case []any:
			stack = append(stack, typed...)
		case json.Number:
			spelling := typed.String()
			if spelling == "" || !bytes.Contains(encoded, []byte(spelling)) {
				t.Fatalf("exact JSON number %q was not preserved in %s", spelling, encoded)
			}
		case float32, float64:
			t.Fatalf("JSON number silently became binary floating point: %#v", typed)
		}
	}
}

func FuzzHighRiskPositionalDecoders(f *testing.F) {
	f.Add(uint8(0), []byte(`["m1","subject",[["Sender","sender@example.test"]],1700000000000]`))
	f.Add(uint8(1), []byte(`["m1","default0/INBOX","subject",[],[],[],0,null,null,false,0]`))
	f.Add(uint8(2), []byte(`[1,"task",null,null]`))
	f.Add(uint8(3), []byte(`["36/1","36","title","file.txt","text/plain",0]`))
	f.Add(uint8(4), []byte(`["36","Documents","infostore",21,false]`))
	f.Add(uint8(5), []byte(`[[1,"file.txt",0,"text/plain"]]`))
	f.Add(uint8(5), []byte(`[{"id":1,"filename":"file.txt","file_size":0,"file_mimetype":"text/plain"}]`))
	f.Add(uint8(0), []byte(`null`))
	f.Add(uint8(3), []byte(`not json`))

	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		var row []json.RawMessage
		if err := json.Unmarshal(data, &row); err != nil {
			row = []json.RawMessage{json.RawMessage(data)}
		}

		switch kind % 6 {
		case 0:
			item, err := decodeDefaultMailListRow(row)
			if err == nil {
				if strings.TrimSpace(item.ID) == "" {
					t.Fatal("mail list decoder accepted a blank id")
				}
				if _, err := time.Parse(time.RFC3339, item.Date); err != nil {
					t.Fatalf("mail list decoder emitted invalid date %q", item.Date)
				}
			}
		case 1:
			item, err := decodeMailGetRow(row)
			if err == nil {
				if item.Size < 0 {
					t.Fatal("mail get decoder accepted a negative size")
				}
				for _, date := range []*string{item.SentDate, item.ReceivedDate} {
					if date != nil {
						if _, err := time.Parse(time.RFC3339, *date); err != nil {
							t.Fatalf("mail get decoder emitted invalid date %q", *date)
						}
					}
				}
				for _, addresses := range [][]mailAddress{item.From, item.To, item.Cc} {
					for _, address := range addresses {
						if strings.TrimSpace(address.Address) == "" {
							t.Fatal("mail get decoder accepted a blank address")
						}
					}
				}
			}
		case 2:
			item, err := decodeTaskListRow(row)
			if err == nil {
				id := json.Number(item.ID)
				parsedID, idErr := id.Int64()
				if idErr != nil || parsedID <= 0 {
					t.Fatalf("task decoder emitted invalid id %q", item.ID)
				}
				if item.Status != nil && (*item.Status < 1 || *item.Status > 5) {
					t.Fatalf("task decoder emitted invalid status %d", *item.Status)
				}
				if item.EndDate != nil && *item.EndDate <= 0 {
					t.Fatalf("task decoder emitted invalid end date %d", *item.EndDate)
				}
			}
		case 3:
			item, err := decodeFileListItem(row)
			if err == nil && (strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.FolderID) == "" || item.Size < 0) {
				t.Fatalf("file decoder accepted ambiguous required fields: %#v", item)
			}
		case 4:
			item, err := decodeFolderListItem(row)
			if err == nil && strings.TrimSpace(item.ID) == "" {
				t.Fatal("folder decoder accepted a blank id")
			}
		case 5:
			items, err := decodeAttachmentRows(data)
			if err == nil {
				for _, item := range items {
					if item.ID <= 0 || strings.TrimSpace(item.Filename) == "" || item.FileSize < 0 || strings.TrimSpace(item.FileMimeType) == "" {
						t.Fatalf("attachment decoder accepted ambiguous required fields: %#v", item)
					}
				}
			}
		}
	})
}

func FuzzMutationIdentityAndRefusalDecoders(f *testing.F) {
	f.Add(uint8(0), []byte(`[7]`))
	f.Add(uint8(0), []byte(`[]`))
	f.Add(uint8(1), []byte(`{"updated":[{"id":"evt-1"}],"deleted":[],"conflicts":[]}`))
	f.Add(uint8(1), []byte(`{"updated":[{"id":"other"}]}`))
	f.Add(uint8(2), []byte(`[{"folderId":"cal://0/32","id":"evt-1","deleted":[{"folder":"cal://0/32","id":"evt-1"}]}]`))
	f.Add(uint8(3), []byte(`{"url":"https://share.example.test/token","entity":9,"is_new":true}`))
	f.Add(uint8(4), []byte(`{"folder_id":"trash","id":"moved"}`))
	f.Add(uint8(4), []byte(`{"id":" "}`))
	f.Add(uint8(5), []byte(`{"folder_id":"trash","id":"copied"}`))
	f.Add(uint8(6), []byte(`{"id":"mail-1"}`))
	f.Add(uint8(6), []byte(`null`))
	f.Add(uint8(0), []byte(`not json`))

	f.Fuzz(func(t *testing.T, kind uint8, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		switch kind % 7 {
		case 0:
			if err := accountDeleteResultError(data, 7); err == nil && !exactAccountDeleteConfirmation(data, 7) {
				t.Fatalf("account delete decoder produced false success for %s", data)
			}
		case 1:
			if err := eventUpdateResultError(data, "evt-1"); err == nil && !expectedEventUpdateSuccess(data, "evt-1") {
				t.Fatalf("event update decoder produced false success for %s", data)
			}
		case 2:
			if err := chronosDeleteResultError(data, "evt-1", "cal://0/32"); err == nil &&
				!expectedChronosDeleteSuccess(data, "evt-1", "cal://0/32") {
				t.Fatalf("chronos delete decoder produced false success for %s", data)
			}
		case 3:
			if link, ok := decodeShareLink(data); ok {
				parsed, err := url.Parse(link.URL)
				if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
					strings.TrimSpace(link.URL) != link.URL || link.Entity <= 0 {
					t.Fatalf("share decoder accepted invalid link: %#v", link)
				}
			}
		case 4:
			result, err := decodeMailMoveResult(data, "source", "trash")
			if err == nil && (strings.TrimSpace(result.ID) == "" || result.FolderID != "trash") {
				t.Fatalf("mail move decoder produced ambiguous identity: %#v", result)
			}
		case 5:
			result, err := decodeMailCopyResult(data, "trash")
			if err == nil && (strings.TrimSpace(result.ID) == "" || result.FolderID != "trash") {
				t.Fatalf("mail copy decoder produced ambiguous identity: %#v", result)
			}
		case 6:
			if err := validateMailFlagResult(data, "mail-1"); err == nil && !expectedMailFlagSuccess(data, "mail-1") {
				t.Fatalf("mail flag decoder produced false success for %s", data)
			}
		}
	})
}

func exactAccountDeleteConfirmation(data []byte, requestedID int64) bool {
	var deleted []json.Number
	if err := json.Unmarshal(data, &deleted); err != nil || len(deleted) != 1 {
		return false
	}
	id, err := deleted[0].Int64()
	return err == nil && id == requestedID
}

func expectedEventUpdateSuccess(data []byte, requestedID string) bool {
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return false
	}
	switch typed := result.(type) {
	case nil:
		return true
	case map[string]any:
		if len(typed) == 0 || typed["id"] == requestedID {
			return true
		}
		updated, ok := typed["updated"].([]any)
		if !ok {
			return false
		}
		for _, raw := range updated {
			if event, ok := raw.(map[string]any); ok && event["id"] == requestedID {
				return true
			}
		}
	}
	return false
}

func expectedChronosDeleteSuccess(data []byte, eventID, folder string) bool {
	var results []struct {
		FolderID string `json:"folderId"`
		ID       string `json:"id"`
		Deleted  []struct {
			Folder string `json:"folder"`
			ID     string `json:"id"`
		} `json:"deleted"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &results); err != nil {
		return false
	}
	for _, result := range results {
		if result.ID != eventID || result.FolderID != folder ||
			(len(result.Error) > 0 && !bytes.Equal(bytes.TrimSpace(result.Error), []byte("null"))) {
			continue
		}
		for _, deleted := range result.Deleted {
			if deleted.ID == eventID && (deleted.Folder == "" || deleted.Folder == folder) {
				return true
			}
		}
	}
	return false
}

func expectedMailFlagSuccess(data []byte, expectedID string) bool {
	if strings.TrimSpace(string(data)) == "" {
		return true
	}
	var result *struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return false
	}
	return result == nil || result.ID == nil || *result.ID == expectedID
}
