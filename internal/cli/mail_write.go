// ABOUTME: The "mail move", "mail copy", "mail flag", and "mail delete" commands: change a
// ABOUTME: message's folder, duplicate it into another folder, toggle its seen/flagged state,
// ABOUTME: and delete it (destructive, confirm-gated).
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
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

// OX mail message flag bits (IMAP-derived): SEEN and FLAGGED are the only ones exposed
// by mail.flag; the others (ANSWERED, DELETED, DRAFT, ...) are not surfaced.
const (
	mailFlagSeen    = 32
	mailFlagFlagged = 8

	mailDraftDefaultFolder = "default0/Drafts"
	mailTrashDefaultFolder = "default0/Trash"
)

func init() {
	operations.Register(operations.Operation{
		Name:       "mail.move",
		Resource:   "mail",
		Action:     "move",
		Summary:    "Move a message to a different folder",
		Keywords:   []string{"trash", "archive", "soft delete", "file away"},
		Method:     http.MethodPut,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":                 {Type: "string", Required: true, Description: "message id"},
			"folder":             {Type: "string", Required: true, Description: "current folder id"},
			"to":                 {Type: "string", Required: true, Description: "destination folder id"},
			"dry-run":            {Type: "bool", Required: false, Description: "show the exact normalized request without authentication or mutation"},
			"preflight":          {Type: "bool", Required: false, Description: "authenticate and validate source/destination without mutation"},
			"verify":             {Type: "bool", Required: false, Description: "read before and after the move and verify the stable fingerprint"},
			"expect-fingerprint": {Type: "string", Required: false, Description: "move only if safe metadata matches this sha256 fingerprint"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"source", "destination", "verified", "fingerprint", "dry_run", "preflight", "request"},
			ScalarFields: []string{"verified", "fingerprint", "dry_run", "preflight"},
			DryRunFields: []string{"dry_run", "source", "destination", "verified", "fingerprint", "request"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "mail.copy",
		Resource:   "mail",
		Action:     "copy",
		Summary:    "Copy a message to a different folder",
		Method:     http.MethodPut,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "string", Required: true, Description: "message id"},
			"folder":  {Type: "string", Required: true, Description: "current folder id"},
			"to":      {Type: "string", Required: true, Description: "destination folder id"},
			"dry-run": {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder"},
			ScalarFields: []string{"id", "folder"},
			DryRunFields: []string{"dry_run", "id", "folder"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "mail.flag",
		Resource:   "mail",
		Action:     "flag",
		Summary:    "Set a message's seen and/or flagged state",
		Keywords:   []string{"unread", "read", "seen", "star", "important"},
		Method:     http.MethodPut,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":     {Type: "string", Required: true, Description: "message id"},
			"folder": {Type: "string", Required: true, Description: "folder id"},
			// Not "bool": these are tri-state (leave alone / set / clear), so each takes an
			// explicit true or false value rather than being a bare flag.
			"seen":    {Type: "string", Required: false, Description: "\"true\" to mark as read, \"false\" to mark as unread; omit to leave unchanged"},
			"flagged": {Type: "string", Required: false, Description: "\"true\" to set the flagged/starred state, \"false\" to clear it; omit to leave unchanged"},
			"dry-run": {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"updated"},
			ScalarFields: []string{"updated"},
			DryRunFields: []string{"dry_run", "id", "seen", "flagged"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "mail.delete",
		Resource:    "mail",
		Action:      "delete",
		Summary:     "Delete a message (moves to trash, or permanently deletes if already in trash)",
		Method:      http.MethodPut,
		Endpoint:    "/mail",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "string", Required: true, Description: "message id"},
			"folder":  {Type: "string", Required: true, Description: "folder id"},
			"confirm": {Type: "string", Required: true, Description: "must exactly repeat --id to confirm deletion"},
			"dry-run": {Type: "bool", Required: false, Description: "validate only, do not send the delete request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"deleted", "id"},
			ScalarFields: []string{"deleted", "id"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "mail.send",
		Resource:   "mail",
		Action:     "send",
		Summary:    "Send an email, optionally with file attachments",
		Keywords:   []string{"email", "deliver", "compose", "reply"},
		Method:     http.MethodPost,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"to":      {Type: "string", Required: false, Description: "recipient address, repeatable"},
			"cc":      {Type: "string", Required: false, Description: "cc address, repeatable"},
			"bcc":     {Type: "string", Required: false, Description: "bcc address, repeatable"},
			"from":    {Type: "string", Required: false, Description: "sender address from mail.senders, optionally \"Name <address>\"; default: the account's default sender"},
			"subject": {Type: "string", Required: false, Description: "message subject"},
			"body":    {Type: "string", Required: false, Description: "message body (plain text)"},
			"attach":  {Type: "string", Required: false, Description: "path to a file to attach, repeatable"},
			"input":   {Type: "string", Required: false, Description: "structured input: a JSON object with from/to/cc/bcc/subject/body/attachments, or @file.json to read it from a file"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show the message that would be sent, without sending it"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"sent", "id", "from", "to", "subject"},
			ScalarFields: []string{"sent", "id", "from", "subject"},
			DryRunFields: []string{"dry_run", "from", "to", "cc", "bcc", "subject", "attachments"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "mail.draft",
		Resource:   "mail",
		Action:     "draft",
		Summary:    "Save a message as a draft in the Drafts folder without sending it",
		Keywords:   []string{"compose", "write", "prepare", "reply"},
		Method:     http.MethodPut,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"to":      {Type: "string", Required: false, Description: "recipient address, repeatable"},
			"cc":      {Type: "string", Required: false, Description: "cc address, repeatable"},
			"bcc":     {Type: "string", Required: false, Description: "bcc address, repeatable"},
			"from":    {Type: "string", Required: false, Description: "sender address from mail.senders, optionally \"Name <address>\"; default: the account's default sender"},
			"subject": {Type: "string", Required: false, Description: "message subject"},
			"body":    {Type: "string", Required: false, Description: "message body (plain text)"},
			"input":   {Type: "string", Required: false, Description: "structured input: a JSON object with from/to/cc/bcc/subject/body, or @file.json to read it from a file"},
			"folder":  {Type: "string", Required: false, Description: "destination folder id (default \"default0/Drafts\")"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show the draft that would be saved, without saving it"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder", "from"},
			ScalarFields: []string{"id", "folder", "from"},
			DryRunFields: []string{"dry_run", "from", "to", "cc", "bcc", "subject", "folder"},
		},
	})
}

// mailSendInput is the structured shape accepted via mail.send's --input flag and built
// from --to/--cc/--bcc/--subject/--body/--attach flags; flags override --input per field.
type mailSendInput struct {
	From        string   `json:"from,omitempty"`
	To          []string `json:"to,omitempty"`
	Cc          []string `json:"cc,omitempty"`
	Bcc         []string `json:"bcc,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	Body        string   `json:"body,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

// buildMailSendInput merges structured --input with --to/--cc/--bcc/--subject/--body/--attach
// flags into one mailSendInput, with any flag that was actually supplied overriding the
// corresponding --input field.
func buildMailSendInput(inputValue, from string, fromSet bool, to, cc, bcc []string, subject string, subjectSet bool, body string, bodySet bool, attach []string) (mailSendInput, error) {
	result := mailSendInput{}
	if inputValue != "" {
		parsed, err := parseInputValue(inputValue)
		if err != nil {
			return mailSendInput{}, err
		}
		if parsed == nil {
			return mailSendInput{}, fmt.Errorf("--input must be a JSON object, not null")
		}
		allowed := map[string]bool{
			"from": true, "to": true, "cc": true, "bcc": true, "subject": true, "body": true, "attachments": true,
		}
		for field := range parsed {
			if !allowed[field] {
				return mailSendInput{}, fmt.Errorf("--input contains unknown mail field %q", field)
			}
		}
		raw, err := json.Marshal(parsed)
		if err != nil {
			return mailSendInput{}, err
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return mailSendInput{}, fmt.Errorf("--input is not a valid mail send object: %w", err)
		}
	}
	if fromSet {
		result.From = from
	}
	if len(to) > 0 {
		result.To = to
	}
	if len(cc) > 0 {
		result.Cc = cc
	}
	if len(bcc) > 0 {
		result.Bcc = bcc
	}
	if subjectSet {
		result.Subject = subject
	}
	if bodySet {
		result.Body = body
	}
	if len(attach) > 0 {
		result.Attachments = attach
	}
	return result, nil
}

func decodeMailSendID(data []byte) (string, *cliErrors.Error) {
	var id string
	if err := json.Unmarshal(data, &id); err != nil {
		return "", &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail send response"}
	}
	if strings.TrimSpace(id) == "" {
		return "", &cliErrors.Error{Code: "invalid_response", Message: "mail send response did not include a message id"}
	}
	return id, nil
}

type mailLocationResult struct {
	FolderID string `json:"folder_id"`
	ID       string `json:"id"`
}

type mailMoveRequest struct {
	Method   string            `json:"method"`
	Endpoint string            `json:"endpoint"`
	Action   string            `json:"action"`
	Params   map[string]string `json:"params"`
	Body     map[string]string `json:"body"`
}

type mailMovePlan struct {
	SourceFolder      string
	SourceID          string
	DestinationFolder string
	Request           mailMoveRequest
}

type mailMoveLocation struct {
	Folder string  `json:"folder"`
	ID     *string `json:"id"`
}

type mailMoveOutput struct {
	DryRun      bool             `json:"dry_run,omitempty"`
	Preflight   bool             `json:"preflight,omitempty"`
	Source      mailMoveLocation `json:"source"`
	Destination mailMoveLocation `json:"destination"`
	Verified    bool             `json:"verified"`
	Fingerprint string           `json:"fingerprint,omitempty"`
	Request     *mailMoveRequest `json:"request,omitempty"`
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}

func newMailMovePlan(id, folder, destination string) (mailMovePlan, error) {
	id = strings.TrimSpace(id)
	folder = strings.TrimSpace(folder)
	destination = strings.TrimSpace(destination)
	if id == "" || folder == "" || destination == "" {
		return mailMovePlan{}, fmt.Errorf("id, folder, and destination must not be blank")
	}
	if folder == destination {
		return mailMovePlan{}, fmt.Errorf("--folder and --to must identify different folders")
	}
	request := mailMoveRequest{
		Method:   http.MethodPut,
		Endpoint: "/mail",
		Action:   "update",
		Params:   map[string]string{"folder": folder, "id": id},
		Body:     map[string]string{"folder_id": destination},
	}
	return mailMovePlan{SourceFolder: folder, SourceID: id, DestinationFolder: destination, Request: request}, nil
}

func newMailMoveOutput(plan mailMovePlan) mailMoveOutput {
	return mailMoveOutput{
		Source:      mailMoveLocation{Folder: plan.SourceFolder, ID: stringPointer(plan.SourceID)},
		Destination: mailMoveLocation{Folder: plan.DestinationFolder},
	}
}

// olderFingerprintHint explains a fingerprint mismatch that is not a changed message. mbx
// now reads mail dates in UTC, so a fingerprint or checkpoint made by an older mbx
// version does not match on an account whose time zone is not UTC.
const olderFingerprintHint = "a fingerprint made by an older mbx version (before mail dates were read in UTC) does not match; get a new one with mail get"

func validMailFingerprint(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range strings.TrimPrefix(value, "sha256:") {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func preflightMailMove(ctx context.Context, c *client.Client, sess client.Session, plan mailMovePlan, expectedFingerprint string) (mailMoveOutput, *cliErrors.Error) {
	result, sourceErr := inspectMailMoveSource(ctx, c, sess, plan, expectedFingerprint)
	if sourceErr != nil {
		return mailMoveOutput{}, sourceErr
	}
	destination, err := getFolder(ctx, c, sess, plan.DestinationFolder)
	if err != nil {
		return mailMoveOutput{}, toCLIError(err, "api_error")
	}
	if destination.Module != "mail" {
		return mailMoveOutput{}, &cliErrors.Error{Code: "invalid_argument", Message: "destination folder is not a mail folder: " + plan.DestinationFolder}
	}
	return result, nil
}

func inspectMailMoveSource(ctx context.Context, c *client.Client, sess client.Session, plan mailMovePlan, expectedFingerprint string) (mailMoveOutput, *cliErrors.Error) {
	message, err := fetchMailMetadata(ctx, c, sess, plan.SourceID, plan.SourceFolder)
	if err != nil {
		return mailMoveOutput{}, toCLIError(err, "api_error")
	}
	if expectedFingerprint != "" && message.Fingerprint != expectedFingerprint {
		return mailMoveOutput{}, &cliErrors.Error{Code: "precondition_failed", Message: fmt.Sprintf("mail fingerprint mismatch for %s/%s: expected %s, got %s; %s", plan.SourceFolder, plan.SourceID, expectedFingerprint, message.Fingerprint, olderFingerprintHint)}
	}
	result := newMailMoveOutput(plan)
	result.Fingerprint = message.Fingerprint
	return result, nil
}

func executeMailMove(ctx context.Context, c *client.Client, sess client.Session, plan mailMovePlan, verify bool, expectedFingerprint string) (mailMoveOutput, *cliErrors.Error) {
	return executeMailMoveWithResolvedDestination(ctx, c, sess, plan, verify, expectedFingerprint, false)
}

func executeMailMoveWithResolvedDestination(ctx context.Context, c *client.Client, sess client.Session, plan mailMovePlan, verify bool, expectedFingerprint string, destinationResolved bool) (mailMoveOutput, *cliErrors.Error) {
	result := newMailMoveOutput(plan)
	beforeFingerprint := ""
	if verify || expectedFingerprint != "" {
		var preflight mailMoveOutput
		var err *cliErrors.Error
		if destinationResolved {
			preflight, err = inspectMailMoveSource(ctx, c, sess, plan, expectedFingerprint)
		} else {
			preflight, err = preflightMailMove(ctx, c, sess, plan, expectedFingerprint)
		}
		if err != nil {
			return mailMoveOutput{}, err
		}
		result.Fingerprint = preflight.Fingerprint
		beforeFingerprint = preflight.Fingerprint
	}

	params := url.Values{"folder": {plan.SourceFolder}, "id": {plan.SourceID}}
	data, err := c.DoJSON(ctx, plan.Request.Method, plan.Request.Endpoint, plan.Request.Action, sess, params, plan.Request.Body)
	if err != nil {
		return mailMoveOutput{}, toCLIError(err, "api_error")
	}
	moved, responseErr := decodeMailMoveResult(data, plan.SourceID, plan.DestinationFolder)
	if responseErr != nil {
		return mailMoveOutput{}, responseErr
	}
	result.Destination.ID = stringPointer(moved.ID)

	if verify {
		// A verification_failed error comes with the partial result: the move completed,
		// so Destination.ID is set and Fingerprint holds the source fingerprint.
		after, verifyErr := fetchMailMetadata(ctx, c, sess, moved.ID, moved.FolderID)
		if verifyErr != nil {
			return result, &cliErrors.Error{Code: "verification_failed", Message: fmt.Sprintf("move completed but destination verification failed for %s/%s: %s", moved.FolderID, moved.ID, verifyErr.Error())}
		}
		if after.Fingerprint != beforeFingerprint {
			return result, &cliErrors.Error{Code: "verification_failed", Message: fmt.Sprintf("move completed but destination fingerprint differs: source %s, destination %s", beforeFingerprint, after.Fingerprint)}
		}
		result.Verified = true
		result.Fingerprint = after.Fingerprint
	}
	return result, nil
}

func renderMailMoveOutput(stdout io.Writer, llm bool, result mailMoveOutput) {
	if llm {
		render.CompactJSON(stdout, result)
		return
	}
	destinationID := ""
	if result.Destination.ID != nil {
		destinationID = *result.Destination.ID
	}
	fields := []render.Field{
		{Name: "source_folder", Value: result.Source.Folder},
		{Name: "source_id", Value: *result.Source.ID},
		{Name: "destination_folder", Value: result.Destination.Folder},
		{Name: "destination_id", Value: destinationID},
		{Name: "verified", Value: strconv.FormatBool(result.Verified)},
	}
	if result.DryRun {
		fields = append([]render.Field{{Name: "dry_run", Value: "true"}}, fields...)
	}
	if result.Preflight {
		fields = append([]render.Field{{Name: "preflight", Value: "true"}}, fields...)
	}
	if result.Fingerprint != "" {
		fields = append(fields, render.Field{Name: "fingerprint", Value: result.Fingerprint})
	}
	render.KeyValueTable(stdout, fields)
}

func decodeMailMoveResult(data []byte, originalID, destination string) (mailLocationResult, *cliErrors.Error) {
	var result mailLocationResult
	if err := json.Unmarshal(data, &result); err != nil {
		return mailLocationResult{}, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail move response"}
	}
	if result.FolderID == "" {
		result.FolderID = destination
	} else if result.FolderID != destination {
		return mailLocationResult{}, &cliErrors.Error{Code: "invalid_response", Message: "mail move response did not match the requested destination"}
	}
	if result.ID == "" {
		result.ID = originalID
	} else if strings.TrimSpace(result.ID) == "" {
		return mailLocationResult{}, &cliErrors.Error{Code: "invalid_response", Message: "mail move response did not include a message id"}
	}
	return result, nil
}

func decodeMailCopyResult(data []byte, destination string) (mailLocationResult, *cliErrors.Error) {
	var result mailLocationResult
	if err := json.Unmarshal(data, &result); err != nil {
		return mailLocationResult{}, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail copy response"}
	}
	if result.FolderID == "" {
		result.FolderID = destination
	} else if result.FolderID != destination {
		return mailLocationResult{}, &cliErrors.Error{Code: "invalid_response", Message: "mail copy response did not match the requested destination"}
	}
	if strings.TrimSpace(result.ID) == "" {
		return mailLocationResult{}, &cliErrors.Error{Code: "invalid_response", Message: "mail copy response did not include the new message id"}
	}
	return result, nil
}

func validateMailFlagResult(data []byte, expectedID string) *cliErrors.Error {
	// OX editing actions may return no data for a successful update. When mail update does
	// identify the affected message, however, it must be the message the caller requested.
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	var result struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail flag response"}
	}
	if result.ID != nil && *result.ID != expectedID {
		return &cliErrors.Error{Code: "invalid_response", Message: "mail flag response did not match the requested message"}
	}
	return nil
}

// mailAddressPairs converts a list of email addresses into the [personal, address] pairs
// the OX mail JSON body expects for "to"/"cc"/"bcc".
func mailAddressPairs(addrs []string) [][2]*string {
	pairs := make([][2]*string, len(addrs))
	for i, a := range addrs {
		addr := a
		pairs[i] = [2]*string{nil, &addr}
	}
	return pairs
}

func runMailSend(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, msg, cerr := parseMailMessageFlags("mail.send", args)
	if cerr != nil {
		return exitWithError(stderr, f.LLM(), cerr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")

	if len(msg.To) == 0 {
		return exitWithError(stderr, llm, invalidArgument("--to is required (or --input with a non-empty \"to\" array)"))
	}
	if msg.Subject == "" {
		return exitWithError(stderr, llm, invalidArgument("--subject is required (or --input with a non-empty \"subject\")"))
	}
	if err := validateRecipientAddresses(msg); err != nil {
		return exitWithError(stderr, llm, invalidArgument(err.Error()))
	}

	for _, path := range msg.Attachments {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return exitWithError(stderr, llm, invalidArgument("--attach file not found: "+path))
		}
		if !info.Mode().IsRegular() {
			return exitWithError(stderr, llm, invalidArgument("--attach must be a regular file: "+path))
		}
	}

	if dryRun {
		renderMailPreview(stdout, llm, mailPreview{
			From: fromDisplay(msg.From), To: msg.To, Cc: msg.Cc, Bcc: msg.Bcc, Subject: msg.Subject,
			Attachments: attachmentNames(msg.Attachments),
		})
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, cerr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	from, cerr := resolveMessageFrom(ctx, c, sess, msg.From)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}
	meta := map[string]any{
		"to":      mailAddressPairs(msg.To),
		"subject": msg.Subject,
		"attachments": []map[string]any{
			{"content_type": "text/plain", "content": msg.Body, "disp": "inline"},
		},
	}
	if from != nil {
		meta["from"] = [][]any{{nullableString(from.Name), from.Address}}
	}
	if len(msg.Cc) > 0 {
		meta["cc"] = mailAddressPairs(msg.Cc)
	}
	if len(msg.Bcc) > 0 {
		meta["bcc"] = mailAddressPairs(msg.Bcc)
	}

	var files []client.Attachment
	var openedFiles []*os.File
	defer func() {
		for _, f := range openedFiles {
			f.Close()
		}
	}()
	attachmentsMeta := meta["attachments"].([]map[string]any)
	for i, path := range msg.Attachments {
		f, openErr := os.Open(path)
		if openErr != nil {
			cerr := &cliErrors.Error{Code: "invalid_argument", Message: "could not open --attach file: " + path}
			writeErr(stderr, llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
		openedFiles = append(openedFiles, f)

		fieldName := fmt.Sprintf("file_%d", i)
		filename := filepath.Base(path)
		contentType := mime.TypeByExtension(filepath.Ext(filename))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		files = append(files, client.Attachment{
			FieldName:   fieldName,
			Filename:    filename,
			ContentType: contentType,
			Content:     f,
		})
		attachmentsMeta = append(attachmentsMeta, map[string]any{
			"content_type": contentType,
			"disp":         "attachment",
			"filename":     filename,
			"id":           fieldName,
		})
	}
	meta["attachments"] = attachmentsMeta

	data, err := c.DoMultipartCallback(ctx, "/mail", "new", sess, url.Values{}, meta, files)
	if err != nil {
		writeErr(stderr, llm, toCLIError(err, "api_error"))
		return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
	}

	id, responseErr := decodeMailSendID(data)
	if responseErr != nil {
		writeErr(stderr, llm, responseErr)
		return cliErrors.ExitCodeFor(responseErr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Sent    bool     `json:"sent"`
			ID      string   `json:"id,omitempty"`
			From    string   `json:"from,omitempty"`
			To      []string `json:"to"`
			Subject string   `json:"subject"`
		}{Sent: true, ID: id, From: addressText(from), To: msg.To, Subject: msg.Subject})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "sent", Value: "true"},
		{Name: "from", Value: addressText(from)},
		{Name: "to", Value: addressListToString(msg.To)},
		{Name: "subject", Value: msg.Subject},
	})
	return 0
}

func addressListToString(addrs []string) string {
	return strings.Join(addrs, ", ")
}

// mailPreview is what a mail send or mail draft dry run stands in for: every recipient the
// message would reach and every file it would carry. Folder is set only by mail draft, which
// stores the message instead of sending it.
type mailPreview struct {
	From        string   `json:"from,omitempty"`
	To          []string `json:"to,omitempty"`
	Cc          []string `json:"cc,omitempty"`
	Bcc         []string `json:"bcc,omitempty"`
	Subject     string   `json:"subject,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	Folder      string   `json:"folder,omitempty"`
}

// renderMailPreview writes what the message would be. A dry run is worth running only if it
// shows the parts of the request a caller cannot otherwise check, which for mail is the
// blind-copy list: the delivered message does not reveal it, so a wrong --bcc is discovered
// by its recipient rather than by the sender.
func renderMailPreview(stdout io.Writer, llm bool, preview mailPreview) {
	if llm {
		render.CompactJSON(stdout, struct {
			DryRun bool `json:"dry_run"`
			mailPreview
		}{DryRun: true, mailPreview: preview})
		return
	}

	fields := []render.Field{{Name: "dry_run", Value: "true"}}
	if preview.From != "" {
		fields = append(fields, render.Field{Name: "from", Value: preview.From})
	}
	for _, pair := range []struct {
		name  string
		addrs []string
	}{{"to", preview.To}, {"cc", preview.Cc}, {"bcc", preview.Bcc}} {
		if len(pair.addrs) > 0 {
			fields = append(fields, render.Field{Name: pair.name, Value: addressListToString(pair.addrs)})
		}
	}
	if preview.Subject != "" {
		fields = append(fields, render.Field{Name: "subject", Value: preview.Subject})
	}
	if len(preview.Attachments) > 0 {
		fields = append(fields, render.Field{Name: "attachments", Value: addressListToString(preview.Attachments)})
	}
	if preview.Folder != "" {
		fields = append(fields, render.Field{Name: "folder", Value: preview.Folder})
	}
	render.KeyValueTable(stdout, fields)
}

// attachmentNames reduces attachment paths to the file names the message would carry, which
// is what the recipient sees and what the multipart request sends.
func attachmentNames(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	names := make([]string, len(paths))
	for i, path := range paths {
		names[i] = filepath.Base(path)
	}
	return names
}

// buildDraftMIME renders a mailSendInput as a minimal RFC 822 message for PUT
// /mail?action=new&folder=<drafts folder>, which stores the request body as raw MIME
// bytes rather than accepting the structured JSON object mail.send uses. It carries a
// From header only for a --from that resolveFrom accepted; without one, the client that
// opens the draft uses the account's default sender. It carries no Date header: the OX
// server assigns the message's IMAP INTERNALDATE on storage regardless.
// validateDraftAddresses rejects addresses carrying a carriage return or line feed.
// mail.draft renders the RFC 822 message itself, so such a character would end the header
// it appears in and let the caller append further headers, or an entire forged body, to
// the message stored in Drafts. mail.send needs no equivalent check: it hands addresses to
// the server as JSON values and never assembles the message locally.
func validateDraftAddresses(msg mailSendInput) error {
	for _, group := range []struct {
		flag  string
		addrs []string
	}{{"--to", msg.To}, {"--cc", msg.Cc}, {"--bcc", msg.Bcc}} {
		for _, addr := range group.addrs {
			if strings.ContainsAny(addr, "\r\n") {
				return fmt.Errorf("%s contains a line break, which is not a valid address: %q", group.flag, addr)
			}
		}
	}
	return nil
}

func validateRecipientAddresses(msg mailSendInput) error {
	for _, group := range []struct {
		flag  string
		addrs []string
	}{{"--to", msg.To}, {"--cc", msg.Cc}, {"--bcc", msg.Bcc}} {
		for _, addr := range group.addrs {
			if strings.TrimSpace(addr) == "" {
				return fmt.Errorf("%s contains a blank address", group.flag)
			}
		}
	}
	return nil
}

func buildDraftMIME(msg mailSendInput, from *mail.Address) []byte {
	var b strings.Builder
	if from != nil {
		// mail.Address.String encodes a non-ASCII display name (RFC 2047).
		b.WriteString("From: " + from.String() + "\r\n")
	}
	if len(msg.To) > 0 {
		b.WriteString("To: " + addressListToString(msg.To) + "\r\n")
	}
	if len(msg.Cc) > 0 {
		b.WriteString("Cc: " + addressListToString(msg.Cc) + "\r\n")
	}
	if len(msg.Bcc) > 0 {
		b.WriteString("Bcc: " + addressListToString(msg.Bcc) + "\r\n")
	}
	if msg.Subject != "" {
		b.WriteString("Subject: " + mime.QEncoding.Encode("UTF-8", msg.Subject) + "\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	normalized := strings.ReplaceAll(msg.Body, "\r\n", "\n")
	b.WriteString(strings.ReplaceAll(normalized, "\n", "\r\n"))
	return []byte(b.String())
}

func runMailDraft(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, msg, cerr := parseMailMessageFlags("mail.draft", args)
	if cerr != nil {
		return exitWithError(stderr, f.LLM(), cerr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	folder := mailDraftDefaultFolder
	if value, ok := f.Lookup("folder"); ok {
		folder = value
	}

	if err := validateRecipientAddresses(msg); err != nil {
		return exitWithError(stderr, llm, invalidArgument(err.Error()))
	}
	if len(msg.Attachments) > 0 {
		return exitWithError(stderr, llm, invalidArgument("attachments are not supported by mail.draft"))
	}
	if err := validateDraftAddresses(msg); err != nil {
		return exitWithError(stderr, llm, invalidArgument(err.Error()))
	}
	if strings.TrimSpace(folder) == "" {
		return exitWithError(stderr, llm, invalidArgument("--folder is required"))
	}

	if dryRun {
		renderMailPreview(stdout, llm, mailPreview{
			From: fromDisplay(msg.From), To: msg.To, Cc: msg.Cc, Bcc: msg.Bcc, Subject: msg.Subject, Folder: folder,
		})
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, cerr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	from, cerr := resolveMessageFrom(ctx, c, sess, msg.From)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}
	params := url.Values{"folder": {folder}}
	data, err := c.DoRawBody(ctx, http.MethodPut, "/mail", "new", sess, params, "application/octet-stream", buildDraftMIME(msg, from))
	if err != nil {
		writeErr(stderr, llm, toCLIError(err, "api_error"))
		return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
	}

	var result struct {
		FolderID string `json:"folder_id"`
		ID       string `json:"id"`
	}
	if err := json.Unmarshal(data, &result); err != nil || strings.TrimSpace(result.ID) == "" {
		cerr := &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail draft response"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	newFolder := result.FolderID
	if newFolder == "" {
		newFolder = folder
	} else if newFolder != folder {
		cerr := &cliErrors.Error{Code: "invalid_response", Message: "mail draft response did not match the requested folder"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID     string `json:"id"`
			Folder string `json:"folder"`
			From   string `json:"from,omitempty"`
		}{ID: result.ID, Folder: newFolder, From: addressText(from)})
		return 0
	}

	fields := []render.Field{{Name: "id", Value: result.ID}, {Name: "folder", Value: newFolder}}
	if from != nil {
		fields = append(fields, render.Field{Name: "from", Value: addressText(from)})
	}
	render.KeyValueTable(stdout, fields)
	return 0
}

func runMailMove(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.move", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	preflight := f.Bool("preflight")
	verify := f.Bool("verify")
	id := f.String("id")
	folder := f.String("folder")
	to := f.String("to")
	expectedFingerprint, expectedFingerprintSet := f.Lookup("expect-fingerprint")

	if strings.TrimSpace(id) == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if strings.TrimSpace(folder) == "" {
		return exitWithError(stderr, llm, invalidArgument("--folder is required"))
	}
	if strings.TrimSpace(to) == "" {
		return exitWithError(stderr, llm, invalidArgument("--to is required"))
	}
	if dryRun && preflight {
		return exitWithError(stderr, llm, invalidArgument("--dry-run and --preflight are mutually exclusive"))
	}
	if expectedFingerprintSet && !validMailFingerprint(expectedFingerprint) {
		return exitWithError(stderr, llm, invalidArgument("--expect-fingerprint must be sha256 followed by 64 lowercase hexadecimal characters"))
	}

	plan, planErr := newMailMovePlan(id, folder, to)
	if planErr != nil {
		return exitWithError(stderr, llm, invalidArgument(planErr.Error()))
	}

	if dryRun {
		result := newMailMoveOutput(plan)
		result.DryRun = true
		result.Request = &plan.Request
		result.Fingerprint = expectedFingerprint
		renderMailMoveOutput(stdout, llm, result)
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, cerr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	if preflight {
		result, preflightErr := preflightMailMove(ctx, c, sess, plan, expectedFingerprint)
		if preflightErr != nil {
			writeErr(stderr, llm, preflightErr)
			return cliErrors.ExitCodeFor(preflightErr)
		}
		result.Preflight = true
		result.Request = &plan.Request
		renderMailMoveOutput(stdout, llm, result)
		return 0
	}

	result, moveErr := executeMailMove(ctx, c, sess, plan, verify, expectedFingerprint)
	if moveErr != nil {
		writeErr(stderr, llm, moveErr)
		return cliErrors.ExitCodeFor(moveErr)
	}
	renderMailMoveOutput(stdout, llm, result)
	return 0
}

func runMailCopy(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.copy", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	folder := f.String("folder")
	to := f.String("to")

	if strings.TrimSpace(id) == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if strings.TrimSpace(folder) == "" {
		return exitWithError(stderr, llm, invalidArgument("--folder is required"))
	}
	if strings.TrimSpace(to) == "" {
		return exitWithError(stderr, llm, invalidArgument("--to is required"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool   `json:"dry_run"`
				ID     string `json:"id"`
				Folder string `json:"folder"`
			}{DryRun: true, ID: id, Folder: to})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "id", Value: id},
			{Name: "folder", Value: to},
		})
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, cerr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	params := url.Values{"folder": {folder}, "id": {id}}
	body := map[string]string{"folder_id": to}

	data, err := c.DoJSON(ctx, http.MethodPut, "/mail", "copy", sess, params, body)
	if err != nil {
		writeErr(stderr, llm, toCLIError(err, "api_error"))
		return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
	}

	result, responseErr := decodeMailCopyResult(data, to)
	if responseErr != nil {
		writeErr(stderr, llm, responseErr)
		return cliErrors.ExitCodeFor(responseErr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID     string `json:"id"`
			Folder string `json:"folder"`
		}{ID: result.ID, Folder: result.FolderID})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: result.ID}, {Name: "folder", Value: result.FolderID}})
	return 0
}

func runMailFlag(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.flag", args,
		checkedFlag("seen", mailFlagValueCheck("--seen")),
		checkedFlag("flagged", mailFlagValueCheck("--flagged")))
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	folder := f.String("folder")
	seen := mailFlagValue(f, "seen")
	flagged := mailFlagValue(f, "flagged")

	if strings.TrimSpace(id) == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if strings.TrimSpace(folder) == "" {
		return exitWithError(stderr, llm, invalidArgument("--folder is required"))
	}
	if seen == nil && flagged == nil {
		return exitWithError(stderr, llm, invalidArgument("at least one of --seen or --flagged is required"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun  bool   `json:"dry_run"`
				ID      string `json:"id"`
				Seen    *bool  `json:"seen,omitempty"`
				Flagged *bool  `json:"flagged,omitempty"`
			}{DryRun: true, ID: id, Seen: seen, Flagged: flagged})
			return 0
		}
		fields := []render.Field{{Name: "dry_run", Value: "true"}, {Name: "id", Value: id}}
		if seen != nil {
			fields = append(fields, render.Field{Name: "seen", Value: strconv.FormatBool(*seen)})
		}
		if flagged != nil {
			fields = append(fields, render.Field{Name: "flagged", Value: strconv.FormatBool(*flagged)})
		}
		render.KeyValueTable(stdout, fields)
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, cerr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	params := url.Values{"folder": {folder}, "id": {id}}

	if seen != nil {
		body := map[string]any{"flags": mailFlagSeen, "value": *seen}
		data, err := c.DoJSON(ctx, http.MethodPut, "/mail", "update", sess, params, body)
		if err != nil {
			writeErr(stderr, llm, toCLIError(err, "api_error"))
			return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
		}
		if responseErr := validateMailFlagResult(data, id); responseErr != nil {
			writeErr(stderr, llm, responseErr)
			return cliErrors.ExitCodeFor(responseErr)
		}
	}
	if flagged != nil {
		body := map[string]any{"flags": mailFlagFlagged, "value": *flagged}
		data, err := c.DoJSON(ctx, http.MethodPut, "/mail", "update", sess, params, body)
		if err != nil {
			writeErr(stderr, llm, toCLIError(err, "api_error"))
			return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
		}
		if responseErr := validateMailFlagResult(data, id); responseErr != nil {
			writeErr(stderr, llm, responseErr)
			return cliErrors.ExitCodeFor(responseErr)
		}
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Updated bool `json:"updated"`
		}{Updated: true})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: id}, {Name: "updated", Value: "true"}})
	return 0
}

func runMailDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	folder := f.String("folder")
	confirm := f.String("confirm")

	if strings.TrimSpace(id) == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if strings.TrimSpace(folder) == "" {
		return exitWithError(stderr, llm, invalidArgument("--folder is required"))
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("--confirm is required and must repeat --id exactly to delete a message"))
	}
	if confirm != id {
		return exitWithError(stderr, llm, invalidArgument("--confirm must exactly match --id; refusing to delete"))
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
			{Name: "folder", Value: folder},
		})
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, cerr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	body := []map[string]string{{"folder": folder, "id": id}}

	data, err := c.DoJSON(ctx, http.MethodPut, "/mail", "delete", sess, nil, body)
	if err != nil {
		writeErr(stderr, llm, toCLIError(err, "api_error"))
		return cliErrors.ExitCodeFor(toCLIError(err, "api_error"))
	}

	var failed []json.RawMessage
	if err := json.Unmarshal(data, &failed); err != nil || failed == nil {
		cerr := &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail delete response"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	if len(failed) > 0 {
		cerr := &cliErrors.Error{Code: "api_error", Message: "message could not be deleted"}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Deleted bool   `json:"deleted"`
			ID      string `json:"id"`
		}{Deleted: true, ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: id}, {Name: "deleted", Value: "true"}})
	return 0
}

// parseMailMessageFlags reads the message flags that mail send and mail draft share and
// merges them with --input. Only mail send registers --attach.
func parseMailMessageFlags(operation string, args []string) (commandFlags, mailSendInput, *cliErrors.Error) {
	f, ferr := parseCommandFlags(operation, args)
	if ferr != nil {
		return f, mailSendInput{}, ferr
	}
	subject, subjectSet := f.Lookup("subject")
	body, bodySet := f.Lookup("body")
	from, fromSet := f.Lookup("from")
	var attach []string
	if f.Accepts("attach") {
		attach = f.Values("attach")
	}
	msg, err := buildMailSendInput(f.String("input"), from, fromSet, f.Values("to"), f.Values("cc"), f.Values("bcc"), subject, subjectSet, body, bodySet, attach)
	if err != nil {
		return f, msg, invalidArgument(err.Error())
	}
	if strings.TrimSpace(msg.From) != "" {
		parsed, err := parseFromValue(msg.From)
		if err != nil {
			return f, msg, invalidArgument(err.Error())
		}
		msg.From = parsed.String()
	} else {
		msg.From = ""
	}
	return f, msg, nil
}

// mailFlagValueCheck rejects a --seen or --flagged value that is not a boolean.
func mailFlagValueCheck(flag string) func(string) *cliErrors.Error {
	return func(value string) *cliErrors.Error {
		if _, err := strconv.ParseBool(value); err != nil {
			return invalidArgument(flag + " must be true or false")
		}
		return nil
	}
}

// mailFlagValue returns the parsed --seen or --flagged value, or nil when it is absent.
func mailFlagValue(f commandFlags, name string) *bool {
	value, ok := f.Lookup(name)
	if !ok {
		return nil
	}
	parsed, _ := strconv.ParseBool(value)
	return &parsed
}

// resolveMessageFrom resolves a --from value of mail send or mail draft. An empty value
// gives nil: the server or the client then uses the account's default sender.
func resolveMessageFrom(ctx context.Context, c *client.Client, sess client.Session, value string) (*mail.Address, *cliErrors.Error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseFromValue(value)
	if err != nil {
		return nil, invalidArgument(err.Error())
	}
	return resolveFrom(ctx, c, sess, parsed)
}

// addressText formats a resolved sender for output, or "" when none was given.
func addressText(address *mail.Address) string {
	if address == nil {
		return ""
	}
	if address.Name == "" {
		return address.Address
	}
	return address.Name + " <" + address.Address + ">"
}

// nullableString gives JSON null for an empty display name, as the OX mail API expects.
func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// fromDisplay shows a checked --from value (canonical form) as "Name <address>" or the bare
// address, for dry-run previews.
func fromDisplay(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := parseFromValue(value)
	if err != nil {
		return value
	}
	return addressText(parsed)
}
