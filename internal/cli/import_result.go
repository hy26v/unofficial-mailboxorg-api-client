// ABOUTME: Shared result handling for the contacts and events import commands: one report
// ABOUTME: that lists every stored item and every failed item, plus the partial-failure exit.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

// importIncompleteCode is the error code an import reports on stderr when at least one
// entry of the file failed. The stdout report still lists every stored entry so a caller
// can verify or clean up the side effects that did happen.
const importIncompleteCode = "import_incomplete"

// importResult is one element of an import response's "data" array: either a created
// object's reference, or a per-item validation error the server accepted the request but
// rejected that object's content for. Both the vCard and the iCal import return this shape.
type importResult struct {
	ID           string `json:"id"`
	FolderID     string `json:"folder_id"`
	LastModified int64  `json:"last_modified"`
	Error        string `json:"error"`
	Code         string `json:"code"`
}

// importOutput reports the whole import. id and folder_id repeat the first stored item
// (null when nothing was stored) for callers that import one object. items lists every
// stored object and failed lists every entry the server rejected or answered with an
// unusable result. index is the 0-based position of the entry in the server response,
// which follows the order of the objects in the file. One index can be in both lists when
// the server stored the object but its result did not match the request.
type importOutput struct {
	ID          *string         `json:"id"`
	FolderID    *string         `json:"folder_id"`
	Items       []importItem    `json:"items"`
	Count       int             `json:"count"`
	Failed      []importFailure `json:"failed"`
	FailedCount int             `json:"failed_count"`
}

type importItem struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	FolderID string `json:"folder_id"`
}

type importFailure struct {
	Index   int    `json:"index"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// newImportOutput sorts each server result into the stored and failed lists. A result
// with an id was stored; a result with an error, without an id, or for a folder other than
// the requested one is a failure. A result with both an error and an id (for example a
// warning about content the server stored anyway) is in both lists, so a caller can find
// and check or clean up the stored object.
func newImportOutput(results []importResult, requestedFolder, noun string) importOutput {
	out := importOutput{Items: []importItem{}, Failed: []importFailure{}}
	for i, r := range results {
		if r.Error != "" {
			code := strings.TrimSpace(r.Code)
			if code == "" {
				code = "api_error"
			}
			if strings.TrimSpace(r.ID) != "" {
				out.Items = append(out.Items, importItem{Index: i, ID: r.ID, FolderID: r.FolderID})
			}
			out.Failed = append(out.Failed, importFailure{Index: i, Code: code, Message: r.Error})
			continue
		}
		id := strings.TrimSpace(r.ID)
		if id == "" {
			out.Failed = append(out.Failed, importFailure{Index: i, Code: "invalid_response", Message: noun + " import result did not include an id"})
			continue
		}
		out.Items = append(out.Items, importItem{Index: i, ID: r.ID, FolderID: r.FolderID})
		switch {
		case strings.TrimSpace(r.FolderID) == "":
			out.Failed = append(out.Failed, importFailure{Index: i, Code: "invalid_response", Message: noun + " import result did not include a folder_id"})
		case r.FolderID != requestedFolder:
			out.Failed = append(out.Failed, importFailure{Index: i, Code: "invalid_response", Message: fmt.Sprintf("%s import result is in folder %s, not the requested folder %s", noun, r.FolderID, requestedFolder)})
		}
	}
	if len(out.Items) > 0 {
		out.ID = stringPointer(out.Items[0].ID)
		out.FolderID = stringPointer(out.Items[0].FolderID)
	}
	out.Count = len(out.Items)
	out.FailedCount = len(out.Failed)
	return out
}

// importIncompleteError summarizes the failed entries for stderr, or returns nil when every
// entry was stored as requested.
func importIncompleteError(out importOutput, total int, noun string) *cliErrors.Error {
	if len(out.Failed) == 0 {
		return nil
	}
	first := out.Failed[0]
	message := fmt.Sprintf("%s import incomplete: %d of %d entries failed and %d were stored; see the failed and items lists on stdout. First failure (entry %d, %s): %s",
		noun, len(out.Failed), total, len(out.Items), first.Index, first.Code, first.Message)
	return &cliErrors.Error{Code: importIncompleteCode, Message: message}
}

// emitImportOutput writes the import report to stdout in both output modes and, when any
// entry failed, writes an import_incomplete error to stderr and returns its exit code.
func emitImportOutput(stdout, stderr io.Writer, llm bool, results []importResult, requestedFolder, noun string) int {
	out := newImportOutput(results, requestedFolder, noun)
	if llm {
		render.CompactJSON(stdout, out)
	} else {
		id, folderID := "", ""
		if out.ID != nil {
			id, folderID = *out.ID, *out.FolderID
		}
		itemsJSON, _ := json.Marshal(out.Items)
		failedJSON, _ := json.Marshal(out.Failed)
		render.KeyValueTable(stdout, []render.Field{
			{Name: "id", Value: id},
			{Name: "folder_id", Value: folderID},
			{Name: "count", Value: strconv.Itoa(out.Count)},
			{Name: "items", Value: string(itemsJSON)},
			{Name: "failed_count", Value: strconv.Itoa(out.FailedCount)},
			{Name: "failed", Value: string(failedJSON)},
		})
	}
	if cerr := importIncompleteError(out, len(results), noun); cerr != nil {
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	return 0
}
