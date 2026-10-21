// ABOUTME: The "files" resource (OX infostore module): list, get (metadata + optional
// ABOUTME: content download to a local path), create (upload), update, and delete.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"math"
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

const (
	filesDefaultLimit = 50
	// filesDefaultFolderPreview stands for the default folder in a dry run, which sends no
	// request and so cannot know the folder id.
	filesDefaultFolderPreview = "default folder (resolved at run time)"
	// filesListColumns are the OX infostore field codes requested from infostore.all, in the
	// order the response rows carry them: object id, folder id, title, filename, mimetype,
	// file size.
	filesListColumns = "1,20,700,702,703,704"
)

func init() {
	registerResource("files", runFiles)
	operations.Register(operations.Operation{
		Name:       "files.list",
		Resource:   "files",
		Action:     "list",
		Summary:    "List files in an infostore folder (defaults to the private infostore folder)",
		Keywords:   []string{"drive", "documents", "infostore", "storage"},
		Method:     http.MethodGet,
		Endpoint:   "/infostore",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"folder": {Type: "string", Required: false, Description: "folder id; defaults to the private infostore folder, read from GET /config/folder/infostore at run time"},
			"limit":  {Type: "int", Required: false, Description: "maximum number of files to return (default 50)"},
		},
		Output: operations.OutputSpec{
			Type:         "list",
			Fields:       []string{"id", "folder_id", "title", "filename", "mimetype", "size"},
			ScalarFields: []string{"id", "folder_id", "title", "filename", "mimetype", "size"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "files.get",
		Resource:   "files",
		Action:     "get",
		Summary:    "Get one file's metadata, optionally downloading its content to a local path",
		Method:     http.MethodGet,
		Endpoint:   "/infostore",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "string", Required: true, Description: "file id, e.g. \"36/2\""},
			"save-to": {Type: "string", Required: false, Description: "local path to save the file's content to; content is never printed to stdout"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "title", "filename", "mimetype", "size", "description", "last_modified", "saved_to", "bytes_written"},
			ScalarFields: []string{"id", "folder_id", "title", "filename", "mimetype", "size", "description", "last_modified", "saved_to", "bytes_written"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "files.create",
		Resource:   "files",
		Action:     "create",
		Summary:    "Upload a local file into an infostore folder (defaults to the private infostore folder)",
		Method:     http.MethodPost,
		Endpoint:   "/infostore",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"file":        {Type: "string", Required: true, Description: "local path of the file to upload"},
			"folder":      {Type: "string", Required: false, Description: "destination folder id; defaults to the private infostore folder, read from GET /config/folder/infostore at run time (a dry run shows folder_id null)"},
			"title":       {Type: "string", Required: false, Description: "file title; defaults to the local file's base name"},
			"description": {Type: "string", Required: false, Description: "file description"},
			"dry-run":     {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "title"},
			ScalarFields: []string{"id", "folder_id", "title"},
			DryRunFields: []string{"dry_run", "folder_id", "title", "filename"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "files.update",
		Resource:   "files",
		Action:     "update",
		Summary:    "Update metadata fields on an existing file",
		Method:     http.MethodPut,
		Endpoint:   "/infostore",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":          {Type: "string", Required: true, Description: "file id, e.g. \"36/2\""},
			"title":       {Type: "string", Required: false, Description: "new title"},
			"description": {Type: "string", Required: false, Description: "new description"},
			"filename":    {Type: "string", Required: false, Description: "new filename"},
			"dry-run":     {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "updated", "timestamp"},
			ScalarFields: []string{"id", "updated", "timestamp"},
			DryRunFields: []string{"dry_run", "id", "fields"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "files.delete",
		Resource:    "files",
		Action:      "delete",
		Summary:     "Delete a file (destructive; requires --confirm <id>)",
		Method:      http.MethodPut,
		Endpoint:    "/infostore",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "string", Required: true, Description: "file id, e.g. \"36/2\""},
			"confirm": {Type: "string", Required: true, Description: "must exactly repeat the file id being deleted"},
			"dry-run": {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "deleted"},
			ScalarFields: []string{"id", "deleted"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
}

func runFiles(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("files requires an action, e.g. \"files list\""))
	}

	switch args[0] {
	case "list":
		return runFilesList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runFilesGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runFilesCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runFilesUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runFilesDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown files action: "+args[0]))
	}
}

type fileListItem struct {
	ID       string `json:"id"`
	FolderID string `json:"folder_id"`
	Title    string `json:"title"`
	Filename string `json:"filename"`
	MimeType string `json:"mimetype"`
	Size     int64  `json:"size"`
}

// resolveDefaultFilesFolder reads the user's private infostore folder id with
// GET /config/folder/infostore. The id differs between accounts, so it is never assumed.
func resolveDefaultFilesFolder(ctx context.Context, c *client.Client, sess client.Session) (string, *cliErrors.Error) {
	data, err := c.Do(ctx, http.MethodGet, "/config/folder/infostore", "", sess, nil)
	if err != nil {
		cause := toCLIError(err, "api_error")
		resolved := *cause
		resolved.Message = "could not read the default infostore folder from /config/folder/infostore (" + cause.Message + "); pass --folder <id>"
		return "", &resolved
	}
	folder, ok := jsonIDString(data)
	if !ok || strings.TrimSpace(folder) != folder {
		return "", &cliErrors.Error{Code: "invalid_response", Message: "the default infostore folder from /config/folder/infostore is not a folder id; pass --folder <id>"}
	}
	return folder, nil
}

// filesFolderRule rejects an empty --folder while the arguments are parsed: an empty value
// must not silently mean the default folder.
func filesFolderRule() flagRule {
	return checkedFlag("folder", func(value string) *cliErrors.Error {
		if strings.TrimSpace(value) == "" {
			return invalidArgument("--folder must not be empty; omit --folder to use the default folder")
		}
		return nil
	})
}

func runFilesList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("files.list", args,
		filesFolderRule(),
		intFlag("limit", 1, math.MaxInt, "--limit must be a positive integer"))
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	folder := f.String("folder")
	limit := filesDefaultLimit
	if n, ok := f.Int("limit"); ok {
		limit = n
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}
	if folder == "" {
		folder, cerr = resolveDefaultFilesFolder(ctx, c, sess)
		if cerr != nil {
			return exitWithError(stderr, llm, cerr)
		}
	}

	params := url.Values{"folder": {folder}, "columns": {filesListColumns}}
	data, err := c.Do(ctx, http.MethodGet, "/infostore", "all", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode files response"})
	}

	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}

	items := make([]fileListItem, 0, len(rows))
	for _, row := range rows {
		item, err := decodeFileListItem(row)
		if err != nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode files response"})
		}
		if item.FolderID != folder {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "files response contained an item from a different folder"})
		}
		items = append(items, item)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Files     []fileListItem `json:"files"`
			Truncated bool           `json:"truncated"`
		}{Files: items, Truncated: truncated})
		return 0
	}

	header := []string{"id", "folder_id", "title", "filename", "mimetype", "size"}
	tableRows := make([][]string, 0, len(items))
	for _, it := range items {
		tableRows = append(tableRows, []string{it.ID, it.FolderID, it.Title, it.Filename, it.MimeType, strconv.FormatInt(it.Size, 10)})
	}
	render.Table(stdout, header, tableRows)
	writeTruncationNotice(stdout, truncated, limit)
	return 0
}

func decodeFileListItem(row []json.RawMessage) (fileListItem, error) {
	if len(row) != 6 {
		return fileListItem{}, &cliErrors.Error{Code: "invalid_response", Message: "unexpected number of files columns in response"}
	}
	var item fileListItem
	if err := json.Unmarshal(row[0], &item.ID); err != nil {
		return fileListItem{}, err
	}
	if strings.TrimSpace(item.ID) == "" {
		return fileListItem{}, &cliErrors.Error{Code: "invalid_response", Message: "file row did not include an id"}
	}
	if err := json.Unmarshal(row[1], &item.FolderID); err != nil {
		return fileListItem{}, err
	}
	if strings.TrimSpace(item.FolderID) == "" {
		return fileListItem{}, &cliErrors.Error{Code: "invalid_response", Message: "file row did not include a folder id"}
	}
	var title *string
	if err := json.Unmarshal(row[2], &title); err != nil {
		return fileListItem{}, err
	}
	if title == nil {
		return fileListItem{}, &cliErrors.Error{Code: "invalid_response", Message: "file row contained a null title"}
	}
	item.Title = *title
	var filename *string
	if err := json.Unmarshal(row[3], &filename); err != nil {
		return fileListItem{}, err
	}
	if filename == nil {
		return fileListItem{}, &cliErrors.Error{Code: "invalid_response", Message: "file row contained a null filename"}
	}
	item.Filename = *filename
	var mimeType *string
	if err := json.Unmarshal(row[4], &mimeType); err != nil {
		return fileListItem{}, err
	}
	if mimeType == nil {
		return fileListItem{}, &cliErrors.Error{Code: "invalid_response", Message: "file row contained a null MIME type"}
	}
	item.MimeType = *mimeType
	var size *int64
	if err := json.Unmarshal(row[5], &size); err != nil {
		return fileListItem{}, err
	}
	if size == nil {
		return fileListItem{}, &cliErrors.Error{Code: "invalid_response", Message: "file row contained a null size"}
	}
	if *size < 0 {
		return fileListItem{}, &cliErrors.Error{Code: "invalid_response", Message: "file row contained a negative size"}
	}
	item.Size = *size
	return item, nil
}

// fileMeta is the OX infostore metadata object returned by infostore?action=get.
type fileMeta struct {
	ID              string `json:"id"`
	FolderID        string `json:"folder_id"`
	Title           string `json:"title"`
	TitlePresent    bool   `json:"-"`
	Filename        string `json:"filename"`
	FilenamePresent bool   `json:"-"`
	MimeType        string `json:"file_mimetype"`
	MimeTypePresent bool   `json:"-"`
	Size            int64  `json:"file_size"`
	SizePresent     bool   `json:"-"`
	Description     string `json:"description"`
	LastModified    int64  `json:"last_modified"`
}

func fetchFileMeta(ctx context.Context, c *client.Client, sess client.Session, id string) (fileMeta, error) {
	data, err := c.Do(ctx, http.MethodGet, "/infostore", "get", sess, url.Values{"id": {id}})
	if err != nil {
		return fileMeta{}, err
	}
	var raw struct {
		ID           string  `json:"id"`
		FolderID     string  `json:"folder_id"`
		Title        *string `json:"title"`
		Filename     *string `json:"filename"`
		MimeType     *string `json:"file_mimetype"`
		Size         *int64  `json:"file_size"`
		Description  string  `json:"description"`
		LastModified int64   `json:"last_modified"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || strings.TrimSpace(raw.ID) == "" || raw.ID != id ||
		strings.TrimSpace(raw.FolderID) == "" || (raw.Size != nil && *raw.Size < 0) || raw.LastModified <= 0 {
		return fileMeta{}, &cliErrors.Error{Code: "invalid_response", Message: "could not decode file metadata response"}
	}
	var title string
	if raw.Title != nil {
		title = *raw.Title
	}
	var filename string
	if raw.Filename != nil {
		filename = *raw.Filename
	}
	var mimeType string
	if raw.MimeType != nil {
		mimeType = *raw.MimeType
	}
	var size int64
	if raw.Size != nil {
		size = *raw.Size
	}
	return fileMeta{
		ID: raw.ID, FolderID: raw.FolderID, Title: title, TitlePresent: raw.Title != nil,
		Filename: filename, FilenamePresent: raw.Filename != nil,
		MimeType: mimeType, MimeTypePresent: raw.MimeType != nil, Size: size, SizePresent: raw.Size != nil,
		Description: raw.Description, LastModified: raw.LastModified,
	}, nil
}

func runFilesGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("files.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id, saveTo := f.String("id"), f.String("save-to")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	meta, err := fetchFileMeta(ctx, c, sess, id)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	if !meta.SizePresent {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "file metadata response did not include a size"})
	}
	if !meta.TitlePresent {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "file metadata response did not include a title"})
	}
	if !meta.FilenamePresent {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "file metadata response did not include a filename"})
	}
	if !meta.MimeTypePresent {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "file metadata response did not include a MIME type"})
	}

	result := struct {
		ID           string `json:"id"`
		FolderID     string `json:"folder_id"`
		Title        string `json:"title"`
		Filename     string `json:"filename"`
		MimeType     string `json:"mimetype"`
		Size         int64  `json:"size"`
		Description  string `json:"description"`
		LastModified int64  `json:"last_modified"`
		SavedTo      string `json:"saved_to,omitempty"`
		// A pointer, not an omitempty int: a document can legitimately be empty, and dropping
		// the count for exactly that document hides the one case where it carries information.
		BytesWritten *int `json:"bytes_written,omitempty"`
	}{
		ID: meta.ID, FolderID: meta.FolderID, Title: meta.Title, Filename: meta.Filename,
		MimeType: meta.MimeType, Size: meta.Size, Description: meta.Description, LastModified: meta.LastModified,
	}

	if saveTo != "" {
		content, err := c.DoRaw(ctx, http.MethodGet, "/infostore", "document", sess, url.Values{"id": {id}})
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		if int64(len(content)) != meta.Size {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "downloaded file size did not match its metadata"})
		}
		if err := writePrivateFile(saveTo, content); err != nil {
			return exitWithError(stderr, llm, invalidArgument("could not write --save-to file: "+err.Error()))
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
		{Name: "id", Value: result.ID},
		{Name: "folder_id", Value: result.FolderID},
		{Name: "title", Value: result.Title},
		{Name: "filename", Value: result.Filename},
		{Name: "mimetype", Value: result.MimeType},
		{Name: "size", Value: strconv.FormatInt(result.Size, 10)},
	}
	if result.SavedTo != "" {
		fields = append(fields, render.Field{Name: "saved_to", Value: result.SavedTo}, render.Field{Name: "bytes_written", Value: strconv.Itoa(*result.BytesWritten)})
	}
	render.KeyValueTable(stdout, fields)
	return 0
}

func runFilesCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("files.create", args, filesFolderRule())
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	folder := f.String("folder")
	path, title, description := f.String("file"), f.String("title"), f.String("description")

	if path == "" {
		return exitWithError(stderr, llm, invalidArgument("--file is required"))
	}

	file, openErr := os.Open(path)
	if openErr != nil {
		return exitWithError(stderr, llm, invalidArgument("--file does not exist or is not a readable file: "+path))
	}
	defer file.Close()
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		return exitWithError(stderr, llm, invalidArgument("--file is not a regular readable file: "+path))
	}

	filename := filepath.Base(path)
	if title == "" {
		title = filename
	}

	if dryRun {
		// A dry run sends no request, so without --folder the default folder id is not
		// known yet: folder_id is null in JSON and described in human output.
		var previewFolder *string
		humanFolder := filesDefaultFolderPreview
		if folder != "" {
			previewFolder = &folder
			humanFolder = folder
		}
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun   bool    `json:"dry_run"`
				Folder   *string `json:"folder_id"`
				Title    string  `json:"title"`
				Filename string  `json:"filename"`
			}{DryRun: true, Folder: previewFolder, Title: title, Filename: filename})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "folder_id", Value: humanFolder},
			{Name: "title", Value: title},
			{Name: "filename", Value: filename},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}
	if folder == "" {
		folder, cerr = resolveDefaultFilesFolder(ctx, c, sess)
		if cerr != nil {
			return exitWithError(stderr, llm, cerr)
		}
	}

	contentType := mime.TypeByExtension(filepath.Ext(filename))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	meta := map[string]any{"folder_id": folder, "title": title, "filename": filename}
	if description != "" {
		meta["description"] = description
	}

	data, err := c.DoMultipart(ctx, "/infostore", "new", sess, nil, meta, filename, contentType, file)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var newID string
	if err := json.Unmarshal(data, &newID); err != nil || strings.TrimSpace(newID) == "" {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode files create response"})
	}

	result := struct {
		ID       string `json:"id"`
		FolderID string `json:"folder_id"`
		Title    string `json:"title"`
	}{ID: newID, FolderID: folder, Title: title}

	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: result.ID},
		{Name: "folder_id", Value: result.FolderID},
		{Name: "title", Value: result.Title},
	})
	return 0
}

func runFilesUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("files.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	title, titleSet := f.Lookup("title")
	description, descriptionSet := f.Lookup("description")
	filename, filenameSet := f.Lookup("filename")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if !titleSet && !descriptionSet && !filenameSet {
		return exitWithError(stderr, llm, invalidArgument("at least one of --title, --description, --filename is required"))
	}

	body := map[string]any{}
	if titleSet {
		body["title"] = title
	}
	if descriptionSet {
		body["description"] = description
	}
	if filenameSet {
		body["filename"] = filename
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool           `json:"dry_run"`
				ID     string         `json:"id"`
				Fields map[string]any `json:"fields"`
			}{DryRun: true, ID: id, Fields: body})
			return 0
		}
		fieldsJSON, _ := json.Marshal(body)
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "id", Value: id},
			{Name: "fields", Value: string(fieldsJSON)},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	meta, err := fetchFileMeta(ctx, c, sess, id)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	params := url.Values{"id": {id}, "timestamp": {strconv.FormatInt(meta.LastModified, 10)}}
	data, newTimestamp, err := c.DoJSONWithTimestamp(ctx, http.MethodPut, "/infostore", "update", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	var updatedID string
	if err := json.Unmarshal(data, &updatedID); err != nil || strings.TrimSpace(updatedID) == "" || updatedID != id {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "files update response did not match requested file " + id})
	}

	result := struct {
		ID        string `json:"id"`
		Updated   bool   `json:"updated"`
		Timestamp int64  `json:"timestamp"`
	}{ID: id, Updated: true, Timestamp: newTimestamp}

	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: result.ID},
		{Name: "updated", Value: strconv.FormatBool(result.Updated)},
		{Name: "timestamp", Value: strconv.FormatInt(result.Timestamp, 10)},
	})
	return 0
}

func runFilesDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("files.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id, confirm := f.String("id"), f.String("confirm")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("deleting a file requires --confirm <id>, repeating the exact file id"))
	}
	if confirm != id {
		return exitWithError(stderr, llm, invalidArgument("--confirm value does not match --id; refusing to delete"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool   `json:"dry_run"`
				ID     string `json:"id"`
			}{DryRun: true, ID: id})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "id", Value: id},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, cerr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	meta, err := fetchFileMeta(ctx, c, sess, id)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	params := url.Values{"timestamp": {strconv.FormatInt(meta.LastModified, 10)}}
	body := []map[string]string{{"id": id, "folder": meta.FolderID}}
	data, err := c.DoJSON(ctx, http.MethodPut, "/infostore", "delete", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var failed []json.RawMessage
	if err := json.Unmarshal(data, &failed); err != nil || failed == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode files delete response"})
	}
	if len(failed) > 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "api_error", Message: "delete was rejected by the server"})
	}

	result := struct {
		ID      string `json:"id"`
		Deleted bool   `json:"deleted"`
	}{ID: id, Deleted: true}

	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: result.ID},
		{Name: "deleted", Value: strconv.FormatBool(result.Deleted)},
	})
	return 0
}
