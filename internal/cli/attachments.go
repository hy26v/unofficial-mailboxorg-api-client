// ABOUTME: The "attachments" resource (OX attachment module): create, list, get/download,
// ABOUTME: and delete file attachments on contacts, tasks, and events.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	registerResource("attachments", runAttachments)

	moduleArg := operations.ArgSpec{Type: "int", Required: true, Description: "OX numeric module code: 1 for events, 4 for tasks, or 7 for contacts"}
	objectIDArg := operations.ArgSpec{Type: "int", Required: true, Description: "id of the object (contact/task/event) the attachment is attached to"}
	folderArg := operations.ArgSpec{Type: "string", Required: true, Description: "folder id containing the attached object"}

	operations.Register(operations.Operation{
		Name:       "attachments.create",
		Resource:   "attachments",
		Action:     "create",
		Summary:    "Attach a local file to a contact, task, or event",
		Method:     http.MethodPost,
		Endpoint:   "/attachment",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"module":    moduleArg,
			"object-id": objectIDArg,
			"folder":    folderArg,
			"file":      {Type: "string", Required: true, Description: "path to the local file to attach"},
			"comment":   {Type: "string", Required: false, Description: "optional attachment comment"},
			"dry-run":   {Type: "bool", Required: false, Description: "validate and show the upload without sending it"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "module", "object_id", "folder", "file", "bytes", "comment"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "attachments.list",
		Resource:   "attachments",
		Action:     "list",
		Summary:    "List attachments on a contact/task/event",
		Method:     http.MethodGet,
		Endpoint:   "/attachment",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"module":    moduleArg,
			"object-id": objectIDArg,
			"folder":    folderArg,
		},
		Output: operations.OutputSpec{
			Type:         "list",
			Fields:       []string{"id", "filename", "file_size", "file_mimetype"},
			ScalarFields: []string{"id", "filename", "file_size", "file_mimetype"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "attachments.get",
		Resource:   "attachments",
		Action:     "get",
		Summary:    "Get one attachment's metadata, optionally downloading its content to a local path",
		Method:     http.MethodGet,
		Endpoint:   "/attachment",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"module":    moduleArg,
			"object-id": objectIDArg,
			"folder":    folderArg,
			"id":        {Type: "int", Required: true, Description: "attachment id"},
			"save-to":   {Type: "string", Required: false, Description: "local path to save the attachment's content to; content is never printed to stdout"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "filename", "file_size", "file_mimetype", "comment", "saved_to", "bytes_written"},
			ScalarFields: []string{"id", "filename", "file_size", "file_mimetype", "comment", "saved_to", "bytes_written"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "attachments.delete",
		Resource:    "attachments",
		Action:      "delete",
		Summary:     "Delete an attachment (destructive; requires --confirm <id>)",
		Method:      http.MethodPut,
		Endpoint:    "/attachment",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"module":    moduleArg,
			"object-id": objectIDArg,
			"folder":    folderArg,
			"id":        {Type: "int", Required: true, Description: "attachment id"},
			"confirm":   {Type: "string", Required: true, Description: "must exactly repeat the attachment id being deleted"},
			"dry-run":   {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "deleted"},
			ScalarFields: []string{"id", "deleted"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
}

func runAttachments(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		err := &cliErrors.Error{Code: "invalid_argument", Message: "attachments requires an action, e.g. \"attachments list\""}
		writeErr(stderr, false, err)
		return cliErrors.ExitCodeFor(err)
	}

	switch args[0] {
	case "create":
		return runAttachmentsCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "list":
		return runAttachmentsList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runAttachmentsGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runAttachmentsDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		llm := hasFlag(args, "--llm")
		err := &cliErrors.Error{Code: "invalid_argument", Message: "unknown attachments action: " + args[0]}
		writeErr(stderr, llm, err)
		return cliErrors.ExitCodeFor(err)
	}
}

type attachmentItem struct {
	ID           int    `json:"id"`
	Filename     string `json:"filename,omitempty"`
	FileSize     int64  `json:"file_size"`
	FileMimeType string `json:"file_mimetype,omitempty"`
	Comment      string `json:"comment,omitempty"`
}

func runAttachmentsCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, target, cerr := parseAttachmentFlags("attachments.create", args)
	if cerr != nil {
		return exitWithError(stderr, f.LLM(), cerr)
	}
	llm := f.LLM()
	module, objectID, folder := target.module, target.objectID, target.folder
	filePath := f.String("file")
	comment := f.String("comment")
	dryRun := f.Bool("dry-run")
	if filePath == "" {
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: "--file is required"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	moduleID, err := strconv.Atoi(module)
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: "--module must be an integer"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	attachedID, err := strconv.Atoi(objectID)
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: "--object-id must be an integer"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	file, err := os.Open(filePath)
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: "could not open --file: " + err.Error()}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		message := "--file must be a regular file"
		if err != nil {
			message = "could not stat --file: " + err.Error()
		}
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: message}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	if dryRun {
		result := struct {
			DryRun  bool   `json:"dry_run"`
			Module  int    `json:"module"`
			Object  int    `json:"object_id"`
			Folder  string `json:"folder"`
			File    string `json:"file"`
			Bytes   int64  `json:"bytes"`
			Comment string `json:"comment,omitempty"`
		}{
			DryRun: true, Module: moduleID, Object: attachedID, Folder: folder,
			File: filepath.Base(filePath), Bytes: info.Size(), Comment: comment,
		}
		if llm {
			render.CompactJSON(stdout, result)
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "module", Value: module},
			{Name: "object_id", Value: objectID},
			{Name: "folder", Value: folder},
			{Name: "file", Value: result.File},
			{Name: "bytes", Value: strconv.FormatInt(result.Bytes, 10)},
		})
		return 0
	}

	meta := map[string]any{
		"folder":   folder,
		"attached": attachedID,
		"module":   moduleID,
	}
	if comment != "" {
		meta["comment"] = comment
	}
	contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(filePath)))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	data, err := c.DoMultipartCallback(ctx, "/attachment", "attach", sess, nil, meta, []client.Attachment{{
		FieldName: "file_0", Filename: filepath.Base(filePath), ContentType: contentType, Content: file,
	}})
	if err != nil {
		cerr := toCLIError(err, "api_error")
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	id, err := decodeCreatedAttachmentID(data)
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_response", Message: err.Error()}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	result := struct {
		ID int `json:"id"`
	}{ID: id}
	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: strconv.Itoa(id)}})
	return 0
}

func decodeCreatedAttachmentID(data []byte) (int, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return 0, fmt.Errorf("could not decode attachment create response")
	}
	if len(items) == 0 {
		return 0, fmt.Errorf("attachment upload returned no attachment id")
	}
	if len(items) != 1 {
		return 0, fmt.Errorf("attachment upload returned multiple attachment ids")
	}
	var id int
	if err := json.Unmarshal(items[0], &id); err == nil && id > 0 {
		return id, nil
	}
	var idString string
	if err := json.Unmarshal(items[0], &idString); err == nil {
		parsed, convErr := strconv.Atoi(idString)
		if convErr == nil && parsed > 0 {
			return parsed, nil
		}
	}
	var object struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(items[0], &object); err == nil && object.ID > 0 {
		return object.ID, nil
	}
	return 0, fmt.Errorf("attachment upload returned an invalid attachment id")
}

// attachmentTarget is the object an attachments command works on: the module code, the
// object id and the folder that every attachments command requires.
type attachmentTarget struct {
	module   string
	objectID string
	folder   string
}

// parseAttachmentFlags parses the flags of an attachments command and checks the
// --module, --object-id and --folder flags that every attachments command shares. The
// returned flags are usable also when the error is non-nil: LLM() tells how to write it.
func parseAttachmentFlags(operation string, args []string) (commandFlags, attachmentTarget, *cliErrors.Error) {
	f, ferr := parseCommandFlags(operation, args)
	if ferr != nil {
		return f, attachmentTarget{}, ferr
	}
	target := attachmentTarget{module: f.String("module"), objectID: f.String("object-id"), folder: f.String("folder")}
	return f, target, requireAttachmentCommon(target)
}

func requireAttachmentCommon(target attachmentTarget) *cliErrors.Error {
	switch {
	case target.module == "":
		return invalidArgument("--module is required")
	case target.objectID == "":
		return invalidArgument("--object-id is required")
	case target.folder == "":
		return invalidArgument("--folder is required")
	}
	moduleID, err := strconv.Atoi(target.module)
	if err != nil || (moduleID != 1 && moduleID != 4 && moduleID != 7) {
		return invalidArgument("--module must be 1 (events), 4 (tasks), or 7 (contacts)")
	}
	if attachedID, err := strconv.Atoi(target.objectID); err != nil || attachedID <= 0 {
		return invalidArgument("--object-id must be a positive integer")
	}
	return nil
}

func parseAttachmentID(id string) (int, *cliErrors.Error) {
	if id == "" {
		return 0, &cliErrors.Error{Code: "invalid_argument", Message: "--id is required"}
	}
	idNum, err := strconv.Atoi(id)
	if err != nil || idNum <= 0 {
		return 0, &cliErrors.Error{Code: "invalid_argument", Message: "--id must be a positive integer"}
	}
	return idNum, nil
}

func runAttachmentsList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, target, cerr := parseAttachmentFlags("attachments.list", args)
	if cerr != nil {
		return exitWithError(stderr, f.LLM(), cerr)
	}
	llm := f.LLM()
	module, objectID, folder := target.module, target.objectID, target.folder

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	// action=all returns positional column tuples and requires an explicit columns list:
	// common id=1, filename=803, file_size=804, file_mimetype=805.
	params := url.Values{
		"module": {module}, "attached": {objectID}, "folder": {folder},
		"columns": {"1,803,804,805"},
	}
	data, err := c.Do(ctx, http.MethodGet, "/attachment", "all", sess, params)
	if err != nil {
		writeErr(stderr, llm, toCLIError(err, "api_error"))
		return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
	}

	items, err := decodeAttachmentRows(data)
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_response", Message: "could not decode attachments response"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	if items == nil {
		items = []attachmentItem{}
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Attachments []attachmentItem `json:"attachments"`
		}{Attachments: items})
		return 0
	}

	header := []string{"id", "filename", "file_size", "file_mimetype"}
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, []string{strconv.Itoa(it.ID), it.Filename, strconv.FormatInt(it.FileSize, 10), it.FileMimeType})
	}
	render.Table(stdout, header, rows)
	return 0
}

func decodeAttachmentRows(data []byte) ([]attachmentItem, error) {
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err != nil {
		return nil, err
	}
	if rawItems == nil {
		return nil, fmt.Errorf("attachment collection was null")
	}
	items := make([]attachmentItem, 0, len(rawItems))
	for _, raw := range rawItems {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return nil, fmt.Errorf("empty attachment row")
		}

		if trimmed[0] == '{' {
			var object struct {
				ID           int     `json:"id"`
				Filename     *string `json:"filename"`
				FileSize     *int64  `json:"file_size"`
				FileMimeType *string `json:"file_mimetype"`
				Comment      string  `json:"comment,omitempty"`
			}
			if err := json.Unmarshal(trimmed, &object); err != nil {
				return nil, err
			}
			if object.ID <= 0 {
				return nil, fmt.Errorf("attachment object row has no valid id")
			}
			if object.Filename == nil || strings.TrimSpace(*object.Filename) == "" {
				return nil, fmt.Errorf("attachment object row has no filename")
			}
			if object.FileSize == nil {
				return nil, fmt.Errorf("attachment object row has no file size")
			}
			if *object.FileSize < 0 {
				return nil, fmt.Errorf("attachment object row has a negative file size")
			}
			if object.FileMimeType == nil || strings.TrimSpace(*object.FileMimeType) == "" {
				return nil, fmt.Errorf("attachment object row has no MIME type")
			}
			items = append(items, attachmentItem{
				ID: object.ID, Filename: *object.Filename, FileSize: *object.FileSize,
				FileMimeType: *object.FileMimeType, Comment: object.Comment,
			})
			continue
		}

		var row []json.RawMessage
		if err := json.Unmarshal(raw, &row); err != nil || len(row) != 4 {
			return nil, fmt.Errorf("invalid attachment column row")
		}
		var item attachmentItem
		var id *int
		if err := json.Unmarshal(row[0], &id); err != nil {
			return nil, err
		}
		if id == nil || *id <= 0 {
			return nil, fmt.Errorf("attachment column row has no valid id")
		}
		item.ID = *id
		var filename *string
		if err := json.Unmarshal(row[1], &filename); err != nil {
			return nil, err
		}
		if filename == nil || strings.TrimSpace(*filename) == "" {
			return nil, fmt.Errorf("attachment column row has a null filename")
		}
		item.Filename = *filename
		var fileSize *int64
		if err := json.Unmarshal(row[2], &fileSize); err != nil {
			return nil, err
		}
		if fileSize == nil {
			return nil, fmt.Errorf("attachment column row has no file size")
		}
		if *fileSize < 0 {
			return nil, fmt.Errorf("attachment column row has a negative file size")
		}
		item.FileSize = *fileSize
		var mimeType *string
		if err := json.Unmarshal(row[3], &mimeType); err != nil {
			return nil, err
		}
		if mimeType == nil || strings.TrimSpace(*mimeType) == "" {
			return nil, fmt.Errorf("attachment column row has a null MIME type")
		}
		item.FileMimeType = *mimeType
		items = append(items, item)
	}
	return items, nil
}

func runAttachmentsGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, target, cerr := parseAttachmentFlags("attachments.get", args)
	if cerr != nil {
		return exitWithError(stderr, f.LLM(), cerr)
	}
	llm := f.LLM()
	module, objectID, folder := target.module, target.objectID, target.folder
	id := f.String("id")
	saveTo := f.String("save-to")
	idNum, cerr := parseAttachmentID(id)
	if cerr != nil {
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	params := url.Values{"module": {module}, "attached": {objectID}, "folder": {folder}, "id": {id}}
	data, err := c.Do(ctx, http.MethodGet, "/attachment", "get", sess, params)
	if err != nil {
		writeErr(stderr, llm, toCLIError(err, "api_error"))
		return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
	}

	var rawItem struct {
		ID           int     `json:"id"`
		Filename     *string `json:"filename"`
		FileSize     *int64  `json:"file_size"`
		FileMimeType *string `json:"file_mimetype"`
		Comment      string  `json:"comment,omitempty"`
	}
	if err := json.Unmarshal(data, &rawItem); err != nil || rawItem.ID <= 0 || rawItem.ID != idNum ||
		rawItem.Filename == nil || strings.TrimSpace(*rawItem.Filename) == "" || rawItem.FileSize == nil ||
		*rawItem.FileSize < 0 || rawItem.FileMimeType == nil || strings.TrimSpace(*rawItem.FileMimeType) == "" {
		cerr := &cliErrors.Error{Code: "invalid_response", Message: "could not decode attachment response"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	item := attachmentItem{
		ID: rawItem.ID, Filename: *rawItem.Filename, FileSize: *rawItem.FileSize,
		FileMimeType: *rawItem.FileMimeType, Comment: rawItem.Comment,
	}

	result := struct {
		ID       int    `json:"id"`
		Filename string `json:"filename,omitempty"`
		// The two sizes are measurements, not optional text: zero is a real answer and the
		// fields are documented, so omitting them makes --select fail on an empty attachment.
		FileSize     int64  `json:"file_size"`
		FileMimeType string `json:"file_mimetype,omitempty"`
		Comment      string `json:"comment"`
		SavedTo      string `json:"saved_to,omitempty"`
		BytesWritten *int   `json:"bytes_written,omitempty"`
	}{ID: item.ID, Filename: item.Filename, FileSize: item.FileSize, FileMimeType: item.FileMimeType, Comment: item.Comment}

	if saveTo != "" {
		content, err := c.DoRaw(ctx, http.MethodGet, "/attachment", "document", sess, params)
		if err != nil {
			writeErr(stderr, llm, toCLIError(err, "api_error"))
			return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
		}
		if int64(len(content)) != item.FileSize {
			cerr := &cliErrors.Error{Code: "invalid_response", Message: "downloaded attachment size did not match its metadata"}
			writeErr(stderr, llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
		if err := writePrivateFile(saveTo, content); err != nil {
			cerr := &cliErrors.Error{Code: "invalid_argument", Message: "could not write --save-to file: " + err.Error()}
			writeErr(stderr, llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
		written := len(content)
		result.SavedTo = saveTo
		result.BytesWritten = &written
	}

	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}

	fields := []render.Field{
		{Name: "id", Value: strconv.Itoa(result.ID)},
		{Name: "filename", Value: result.Filename},
		{Name: "file_size", Value: strconv.FormatInt(result.FileSize, 10)},
		{Name: "file_mimetype", Value: result.FileMimeType},
	}
	if result.SavedTo != "" {
		fields = append(fields, render.Field{Name: "saved_to", Value: result.SavedTo}, render.Field{Name: "bytes_written", Value: strconv.Itoa(*result.BytesWritten)})
	}
	render.KeyValueTable(stdout, fields)
	return 0
}

func runAttachmentsDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, target, cerr := parseAttachmentFlags("attachments.delete", args)
	if cerr != nil {
		return exitWithError(stderr, f.LLM(), cerr)
	}
	llm := f.LLM()
	module, objectID, folder := target.module, target.objectID, target.folder
	id := f.String("id")
	confirm := f.String("confirm")
	dryRun := f.Bool("dry-run")
	idNum, cerr := parseAttachmentID(id)
	if cerr != nil {
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	if confirm == "" {
		err := &cliErrors.Error{Code: "invalid_argument", Message: "deleting an attachment requires --confirm <id>, repeating the exact attachment id"}
		writeErr(stderr, llm, err)
		return cliErrors.ExitCodeFor(err)
	}
	if confirm != id {
		err := &cliErrors.Error{Code: "invalid_argument", Message: "--confirm value does not match --id; refusing to delete"}
		writeErr(stderr, llm, err)
		return cliErrors.ExitCodeFor(err)
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool `json:"dry_run"`
				ID     int  `json:"id"`
			}{DryRun: true, ID: idNum})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "id", Value: strconv.Itoa(idNum)},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	params := url.Values{"module": {module}, "attached": {objectID}, "folder": {folder}}
	body := []int{idNum}
	_, err := c.DoJSON(ctx, http.MethodPut, "/attachment", "detach", sess, params, body)
	if err != nil {
		writeErr(stderr, llm, toCLIError(err, "api_error"))
		return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
	}

	result := struct {
		ID      int  `json:"id"`
		Deleted bool `json:"deleted"`
	}{ID: idNum, Deleted: true}

	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: strconv.Itoa(result.ID)},
		{Name: "deleted", Value: strconv.FormatBool(result.Deleted)},
	})
	return 0
}
