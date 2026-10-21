// ABOUTME: The "mail read" and "mail attachment" commands: one message's body text and attachment
// ABOUTME: list via mail?action=get, and attachment downloads to owner-only files, never to stdout.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	mailReadDefaultMaxBodyBytes = 256 << 10
	mailReadMinimumMaxBodyBytes = 1 << 10
	mailReadMaximumMaxBodyBytes = 16 << 20
)

func init() {
	// mail.read is a read. Only its --mark-seen flag changes data (it sets the seen flag;
	// repeating it changes nothing more), so that flag carries the write effect.
	operations.Register(operations.Operation{
		Name:       "mail.read",
		Resource:   "mail",
		Action:     "read",
		Summary:    "Read one message's body text and attachment list; changes state only with --mark-seen, which marks it seen",
		Keywords:   []string{"open", "body", "content", "view", "message text"},
		Method:     http.MethodGet,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":             {Type: "string", Required: true, Description: "message id"},
			"folder":         {Type: "string", Required: true, Description: "folder id, e.g. \"default0/INBOX\""},
			"body-format":    {Type: "string", Required: false, Description: "text (default; HTML converted to plain text) or html (provider-sanitized HTML)"},
			"max-body-bytes": {Type: "int", Required: false, Description: "maximum body bytes returned, 1024-16777216 (default 262144); body_truncated reports a cut"},
			"mark-seen":      {Type: "bool", Required: false, SideEffect: operations.SideEffectWrite, Description: "mark the message as seen; by default reading leaves the seen flag unchanged"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder", "subject", "from", "to", "cc", "bcc", "sent_date", "received_date", "size", "seen", "flagged", "body_format", "body", "body_bytes", "body_truncated", "attachments", "nested_messages"},
			ScalarFields: []string{"id", "folder", "subject", "sent_date", "received_date", "size", "seen", "flagged", "body_format", "body", "body_bytes", "body_truncated"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "mail.attachment",
		Resource:   "mail",
		Action:     "attachment",
		Summary:    "Save one mail attachment to an owner-only file; content is never printed to stdout",
		Keywords:   []string{"download", "file"},
		Method:     http.MethodGet,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":         {Type: "string", Required: true, Description: "message id"},
			"folder":     {Type: "string", Required: true, Description: "folder id, e.g. \"default0/INBOX\""},
			"attachment": {Type: "string", Required: true, Description: "attachment id from mail read's attachments list"},
			"save-to":    {Type: "string", Required: true, Description: "local path for the attachment content; written owner-only"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder", "attachment", "filename", "content_type", "size", "saved_to", "bytes_written"},
			ScalarFields: []string{"id", "folder", "attachment", "filename", "content_type", "size", "saved_to", "bytes_written"},
		},
	})
}

type mailReadAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        *int64 `json:"size"`
	Disposition string `json:"disposition"`
}

type mailReadResult struct {
	ID            string               `json:"id"`
	Folder        string               `json:"folder"`
	Subject       string               `json:"subject"`
	From          []mailAddress        `json:"from"`
	To            []mailAddress        `json:"to"`
	Cc            []mailAddress        `json:"cc"`
	Bcc           []mailAddress        `json:"bcc"`
	SentDate      *string              `json:"sent_date"`
	ReceivedDate  *string              `json:"received_date"`
	Size          *int64               `json:"size"`
	Seen          bool                 `json:"seen"`
	Flagged       bool                 `json:"flagged"`
	BodyFormat    string               `json:"body_format"`
	Body          string               `json:"body"`
	BodyBytes     int                  `json:"body_bytes"`
	BodyTruncated bool                 `json:"body_truncated"`
	Attachments   []mailReadAttachment `json:"attachments"`
	// NestedMessages are messages attached to this one as message/rfc822, such as a
	// forwarded message. Their body text is not included.
	NestedMessages []mailReadNestedMessage `json:"nested_messages"`
}

// mailReadNestedMessage is one attached message from nested_msgs. OX sends it as a mail
// object without id and folder_id; its attachment ids are unique only inside the message
// that contains it.
type mailReadNestedMessage struct {
	Subject        string                  `json:"subject"`
	From           []mailAddress           `json:"from"`
	To             []mailAddress           `json:"to"`
	Cc             []mailAddress           `json:"cc"`
	SentDate       *string                 `json:"sent_date"`
	ReceivedDate   *string                 `json:"received_date"`
	Attachments    []mailReadAttachment    `json:"attachments"`
	NestedMessages []mailReadNestedMessage `json:"nested_messages,omitempty"`
}

// mailReadMaxNestingDepth bounds how deep attached messages inside attached messages are
// decoded; deeper levels are left out.
const mailReadMaxNestingDepth = 8

type mailReadOptions struct {
	llm          bool
	id           string
	folder       string
	bodyFormat   string
	maxBodyBytes int
	markSeen     bool
}

func parseMailReadOptions(args []string) (mailReadOptions, *cliErrors.Error) {
	opts := mailReadOptions{bodyFormat: "text", maxBodyBytes: mailReadDefaultMaxBodyBytes}
	f, ferr := parseCommandFlags("mail.read", args,
		checkedFlag("body-format", func(value string) *cliErrors.Error {
			if format := strings.TrimSpace(value); format != "text" && format != "html" {
				return invalidArgument("--body-format must be text or html")
			}
			return nil
		}),
		intFlag("max-body-bytes", mailReadMinimumMaxBodyBytes, mailReadMaximumMaxBodyBytes,
			fmt.Sprintf("--max-body-bytes must be an integer between %d and %d", mailReadMinimumMaxBodyBytes, mailReadMaximumMaxBodyBytes)))
	opts.llm = f.LLM()
	if ferr != nil {
		return opts, ferr
	}
	opts.id = strings.TrimSpace(f.String("id"))
	opts.folder = strings.TrimSpace(f.String("folder"))
	if bodyFormat, ok := f.Lookup("body-format"); ok {
		opts.bodyFormat = strings.TrimSpace(bodyFormat)
	}
	if maxBodyBytes, ok := f.Int("max-body-bytes"); ok {
		opts.maxBodyBytes = maxBodyBytes
	}
	opts.markSeen = f.Bool("mark-seen")
	if opts.id == "" {
		return opts, invalidArgument("--id is required")
	}
	if opts.folder == "" {
		return opts, invalidArgument("--folder is required")
	}
	return opts, nil
}

func runMailRead(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	opts, optionErr := parseMailReadOptions(args)
	if optionErr != nil {
		return exitWithError(stderr, opts.llm, optionErr)
	}
	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, opts.llm, authErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	result, err := fetchMailRead(ctx, c, sess, opts)
	if err != nil {
		cerr := toCLIError(err, "api_error")
		writeErr(stderr, opts.llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	if opts.llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	writeMailReadHuman(stdout, result)
	return 0
}

func writeMailReadHuman(w io.Writer, result mailReadResult) {
	sentDate, receivedDate, size := "", "", ""
	if result.SentDate != nil {
		sentDate = *result.SentDate
	}
	if result.ReceivedDate != nil {
		receivedDate = *result.ReceivedDate
	}
	if result.Size != nil {
		size = strconv.FormatInt(*result.Size, 10)
	}
	fields := []render.Field{
		{Name: "id", Value: result.ID},
		{Name: "folder", Value: result.Folder},
		{Name: "subject", Value: result.Subject},
		{Name: "from", Value: addressesToString(result.From)},
		{Name: "to", Value: addressesToString(result.To)},
	}
	if len(result.Cc) > 0 {
		fields = append(fields, render.Field{Name: "cc", Value: addressesToString(result.Cc)})
	}
	if len(result.Bcc) > 0 {
		fields = append(fields, render.Field{Name: "bcc", Value: addressesToString(result.Bcc)})
	}
	fields = append(fields,
		render.Field{Name: "sent_date", Value: sentDate},
		render.Field{Name: "received_date", Value: receivedDate},
		render.Field{Name: "size", Value: size},
		render.Field{Name: "seen", Value: strconv.FormatBool(result.Seen)},
		render.Field{Name: "flagged", Value: strconv.FormatBool(result.Flagged)},
		render.Field{Name: "body_format", Value: result.BodyFormat},
	)
	render.KeyValueTable(w, fields)
	writeMailReadAttachmentsHuman(w, result.Attachments)
	writeMailReadNestedHuman(w, result.NestedMessages, "")
	fmt.Fprintln(w)
	body := render.SafeMultilineText(result.Body)
	fmt.Fprint(w, body)
	if !strings.HasSuffix(body, "\n") {
		fmt.Fprintln(w)
	}
	if result.BodyTruncated {
		fmt.Fprintf(w, "\n(body cut short at %d of %d bytes; raise --max-body-bytes to see more)\n", len(result.Body), result.BodyBytes)
	}
}

func writeMailReadAttachmentsHuman(w io.Writer, attachments []mailReadAttachment) {
	if len(attachments) == 0 {
		return
	}
	fmt.Fprintln(w)
	rows := make([][]string, len(attachments))
	for i, attachment := range attachments {
		attachmentSize := ""
		if attachment.Size != nil {
			attachmentSize = strconv.FormatInt(*attachment.Size, 10)
		}
		rows[i] = []string{attachment.ID, attachment.Filename, attachment.ContentType, attachmentSize, attachment.Disposition}
	}
	render.Table(w, []string{"attachment", "filename", "content_type", "size", "disposition"}, rows)
}

// writeMailReadNestedHuman lists each attached message with its headers and attachments.
// Numbers show the nesting: 1, 2 for messages attached to the read message, 1.1 for a
// message attached to message 1.
func writeMailReadNestedHuman(w io.Writer, messages []mailReadNestedMessage, prefix string) {
	for i, message := range messages {
		number := prefix + strconv.Itoa(i+1)
		sentDate, receivedDate := "", ""
		if message.SentDate != nil {
			sentDate = *message.SentDate
		}
		if message.ReceivedDate != nil {
			receivedDate = *message.ReceivedDate
		}
		fmt.Fprintln(w)
		fields := []render.Field{
			{Name: "nested_message", Value: number},
			{Name: "subject", Value: message.Subject},
			{Name: "from", Value: addressesToString(message.From)},
			{Name: "to", Value: addressesToString(message.To)},
		}
		if len(message.Cc) > 0 {
			fields = append(fields, render.Field{Name: "cc", Value: addressesToString(message.Cc)})
		}
		fields = append(fields, render.Field{Name: "sent_date", Value: sentDate}, render.Field{Name: "received_date", Value: receivedDate})
		render.KeyValueTable(w, fields)
		writeMailReadAttachmentsHuman(w, message.Attachments)
		writeMailReadNestedHuman(w, message.NestedMessages, number+".")
	}
}

// mailReadRawPart is one entry of an OX mail object's attachments array: a body part, a
// file attachment, or a multipart container.
type mailReadRawPart struct {
	ID          json.RawMessage `json:"id"`
	ContentType string          `json:"content_type"`
	Disposition string          `json:"disp"`
	Filename    string          `json:"filename"`
	Size        *int64          `json:"size"`
	Content     *string         `json:"content"`
}

// mailReadRawMessage is the OX mail object from mail?action=get, or one of its nested_msgs.
type mailReadRawMessage struct {
	ID           json.RawMessage   `json:"id"`
	FolderID     string            `json:"folder_id"`
	Subject      *string           `json:"subject"`
	From         json.RawMessage   `json:"from"`
	To           json.RawMessage   `json:"to"`
	Cc           json.RawMessage   `json:"cc"`
	Bcc          json.RawMessage   `json:"bcc"`
	SentDate     json.RawMessage   `json:"sent_date"`
	ReceivedDate json.RawMessage   `json:"received_date"`
	Size         *int64            `json:"size"`
	Flags        *int64            `json:"flags"`
	Attachments  []mailReadRawPart `json:"attachments"`
	NestedMsgs   []json.RawMessage `json:"nested_msgs"`
}

type mailReadBodyPart struct {
	html    bool
	content string
}

// splitMailReadParts separates body text parts from file attachments. Multipart
// containers are skipped. A file attachment without an id is an error unless
// allowMissingID is set, as for nested messages, whose id rules are not documented.
func splitMailReadParts(parts []mailReadRawPart, allowMissingID bool) ([]mailReadBodyPart, []mailReadAttachment, error) {
	bodyParts := []mailReadBodyPart{}
	attachments := []mailReadAttachment{}
	for _, part := range parts {
		contentType := normalizedContentType(part.ContentType)
		disposition := strings.ToLower(strings.TrimSpace(part.Disposition))
		if part.Content != nil && strings.HasPrefix(contentType, "text/") && disposition != "attachment" && strings.TrimSpace(part.Filename) == "" {
			bodyParts = append(bodyParts, mailReadBodyPart{html: contentType == "text/html", content: *part.Content})
			continue
		}
		if strings.HasPrefix(contentType, "multipart/") {
			continue
		}
		partID, err := decodeFlexibleID(part.ID)
		if err != nil {
			if !allowMissingID {
				return nil, nil, &cliErrors.Error{Code: "invalid_response", Message: "mail read attachment did not include an id"}
			}
			partID = ""
		}
		// In the text view, mailbox.org lists the HTML alternative of the body as a
		// contentless attachment with the generated name Part_<id>.html. It is the body
		// in another format, not a file the sender attached.
		if partID != "" && contentType == "text/html" && part.Content == nil && part.Filename == "Part_"+partID+".html" {
			continue
		}
		if part.Size != nil && *part.Size < 0 {
			return nil, nil, &cliErrors.Error{Code: "invalid_response", Message: "mail read attachment contained a negative size"}
		}
		attachments = append(attachments, mailReadAttachment{
			ID: partID, Filename: part.Filename, ContentType: contentType, Size: part.Size, Disposition: disposition,
		})
	}
	return bodyParts, attachments, nil
}

// decodeMailReadHeaders fills the subject, address lists, and dates shared by a message and
// its nested messages. A nil target is skipped.
func decodeMailReadHeaders(message mailReadRawMessage, subject *string, from, to, cc, bcc *[]mailAddress, sentDate, receivedDate **string) error {
	if message.Subject != nil {
		*subject = *message.Subject
	}
	for _, field := range []struct {
		raw    json.RawMessage
		target *[]mailAddress
	}{{message.From, from}, {message.To, to}, {message.Cc, cc}, {message.Bcc, bcc}} {
		if field.target == nil {
			continue
		}
		addresses, err := decodeLenientMailAddresses(field.raw)
		if err != nil {
			return &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail read addresses: " + err.Error()}
		}
		*field.target = addresses
	}
	for _, field := range []struct {
		raw    json.RawMessage
		target **string
	}{{message.SentDate, sentDate}, {message.ReceivedDate, receivedDate}} {
		if len(field.raw) == 0 {
			continue
		}
		date, err := decodeOptionalMailDate(field.raw)
		if err != nil {
			return &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail read date"}
		}
		*field.target = date
	}
	return nil
}

// decodeMailReadNested decodes nested_msgs. Each nested message keeps its headers and file
// attachments; its body text is not decoded.
func decodeMailReadNested(raws []json.RawMessage, depth int) ([]mailReadNestedMessage, error) {
	messages := []mailReadNestedMessage{}
	if depth > mailReadMaxNestingDepth {
		return messages, nil
	}
	for _, raw := range raws {
		var message mailReadRawMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode a nested message in the mail read response"}
		}
		nested := mailReadNestedMessage{}
		if err := decodeMailReadHeaders(message, &nested.Subject, &nested.From, &nested.To, &nested.Cc, nil, &nested.SentDate, &nested.ReceivedDate); err != nil {
			return nil, err
		}
		_, attachments, err := splitMailReadParts(message.Attachments, true)
		if err != nil {
			return nil, err
		}
		nested.Attachments = attachments
		if len(message.NestedMsgs) > 0 {
			children, err := decodeMailReadNested(message.NestedMsgs, depth+1)
			if err != nil {
				return nil, err
			}
			nested.NestedMessages = children
		}
		messages = append(messages, nested)
	}
	return messages, nil
}

// fetchMailRead requests one message through mail?action=get. Without --mark-seen it sends
// unseen=true, so reading does not change the message's seen flag. Only the decoded body
// parts and attachment metadata are returned; raw headers in the response are discarded.
func fetchMailRead(ctx context.Context, c *client.Client, sess client.Session, opts mailReadOptions) (mailReadResult, error) {
	params := url.Values{"folder": {opts.folder}, "id": {opts.id}, "view": {opts.bodyFormat}, "timezone": {mailDateTimezone}}
	if !opts.markSeen {
		params.Set("unseen", "true")
	}
	data, err := c.Do(ctx, http.MethodGet, "/mail", "get", sess, params)
	if err != nil {
		return mailReadResult{}, err
	}
	var message mailReadRawMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return mailReadResult{}, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail read response"}
	}
	id, idErr := decodeFlexibleID(message.ID)
	if idErr != nil || id != opts.id || message.FolderID != opts.folder {
		return mailReadResult{}, &cliErrors.Error{Code: "invalid_response", Message: "mail read response did not match the requested message"}
	}
	if message.Flags == nil || *message.Flags < 0 {
		return mailReadResult{}, errMissingMailFlags
	}
	if message.Size != nil && *message.Size < 0 {
		return mailReadResult{}, errNegativeMailSize
	}
	result := mailReadResult{ID: id, Folder: message.FolderID, Size: message.Size}
	if err := decodeMailReadHeaders(message, &result.Subject, &result.From, &result.To, &result.Cc, &result.Bcc, &result.SentDate, &result.ReceivedDate); err != nil {
		return mailReadResult{}, err
	}
	result.Seen = opts.markSeen || *message.Flags&mailFlagSeen != 0
	result.Flagged = *message.Flags&mailFlagFlagged != 0

	bodyParts, attachments, err := splitMailReadParts(message.Attachments, false)
	if err != nil {
		return mailReadResult{}, err
	}
	result.Attachments = attachments
	nested, err := decodeMailReadNested(message.NestedMsgs, 1)
	if err != nil {
		return mailReadResult{}, err
	}
	result.NestedMessages = nested

	allHTML := len(bodyParts) > 0
	for _, part := range bodyParts {
		allHTML = allHTML && part.html
	}
	keepHTML := opts.bodyFormat == "html" && allHTML
	texts := make([]string, 0, len(bodyParts))
	for _, part := range bodyParts {
		content := strings.ReplaceAll(part.content, "\r\n", "\n")
		if part.html && !keepHTML {
			content = htmlToText(content)
		}
		texts = append(texts, content)
	}
	result.BodyFormat = "text"
	if keepHTML {
		result.BodyFormat = "html"
	}
	body := strings.Join(texts, "\n\n")
	result.BodyBytes = len(body)
	if len(body) > opts.maxBodyBytes {
		body = truncateUTF8(body, opts.maxBodyBytes)
		result.BodyTruncated = true
	}
	result.Body = body
	return result, nil
}

func runMailAttachment(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.attachment", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := strings.TrimSpace(f.String("id"))
	folder := strings.TrimSpace(f.String("folder"))
	attachmentID := strings.TrimSpace(f.String("attachment"))
	saveTo := f.String("save-to")
	for _, required := range []struct{ flag, value string }{{"--id", id}, {"--folder", folder}, {"--attachment", attachmentID}, {"--save-to", strings.TrimSpace(saveTo)}} {
		if required.value == "" {
			return exitWithError(stderr, llm, invalidArgument(required.flag+" is required"))
		}
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	// Read the message first so the attachment id is checked against this message and the
	// output can name the file without trusting the download response.
	message, err := fetchMailRead(ctx, c, sess, mailReadOptions{id: id, folder: folder, bodyFormat: "text", maxBodyBytes: mailReadMinimumMaxBodyBytes})
	if err != nil {
		cerr := toCLIError(err, "api_error")
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	attachment, matches := findMailReadAttachment(message.Attachments, message.NestedMessages, attachmentID)
	if attachment == nil {
		cerr := &cliErrors.Error{Code: "not_found", Message: "attachment not found in message: attachment=" + attachmentID + " id=" + id + " folder=" + folder}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	if matches > 1 {
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: fmt.Sprintf("attachment id is ambiguous: %d attachments in the message and its nested messages have id %s, so the download could save the wrong part; nothing was saved: id=%s folder=%s", matches, attachmentID, id, folder)}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	params := url.Values{"folder": {folder}, "id": {id}, "attachment": {attachmentID}, "save": {"1"}}
	content, err := c.DoRaw(ctx, http.MethodGet, "/mail", "attachment", sess, params)
	if err != nil {
		cerr := toCLIError(err, "api_error")
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	if err := writePrivateFile(saveTo, content); err != nil {
		cerr := &cliErrors.Error{Code: "invalid_argument", Message: "could not write --save-to file: " + err.Error()}
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	result := struct {
		ID           string `json:"id"`
		Folder       string `json:"folder"`
		Attachment   string `json:"attachment"`
		Filename     string `json:"filename"`
		ContentType  string `json:"content_type"`
		Size         *int64 `json:"size"`
		SavedTo      string `json:"saved_to"`
		BytesWritten int    `json:"bytes_written"`
	}{id, folder, attachmentID, attachment.Filename, attachment.ContentType, attachment.Size, saveTo, len(content)}
	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: result.ID},
		{Name: "folder", Value: result.Folder},
		{Name: "attachment", Value: result.Attachment},
		{Name: "filename", Value: result.Filename},
		{Name: "content_type", Value: result.ContentType},
		{Name: "saved_to", Value: result.SavedTo},
		{Name: "bytes_written", Value: strconv.Itoa(result.BytesWritten)},
	})
	return 0
}

// findMailReadAttachment finds an attachment id that mail read lists: among the message's
// own attachments or inside its nested messages. It returns the attachment and how many
// listed attachments have this id. The download request is the same for both; whether OX
// serves a nested message's attachment by this id is not documented and has not been
// checked against a live server. Because the download names only the outer message, an
// id that is listed more than once (in the outer message and a nested one, or in two
// nested messages) could save a different part under the wrong file name, so the caller
// must refuse it.
func findMailReadAttachment(attachments []mailReadAttachment, nested []mailReadNestedMessage, id string) (*mailReadAttachment, int) {
	var found *mailReadAttachment
	matches := 0
	for i := range attachments {
		if attachments[i].ID == id {
			if found == nil {
				found = &attachments[i]
			}
			matches++
		}
	}
	for i := range nested {
		nestedFound, nestedMatches := findMailReadAttachment(nested[i].Attachments, nested[i].NestedMessages, id)
		if found == nil {
			found = nestedFound
		}
		matches += nestedMatches
	}
	return found, matches
}

// decodeFlexibleID accepts a provider id sent as a JSON string or number.
func decodeFlexibleID(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("blank id")
		}
		return text, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return "", err
	}
	return number.String(), nil
}

// decodeLenientMailAddresses decodes OX [personal, address] pairs for display. Unlike the
// metadata decoder it keeps entries without an address (for example a bare group name).
func decodeLenientMailAddresses(raw json.RawMessage) ([]mailAddress, error) {
	addresses := []mailAddress{}
	if len(raw) == 0 || string(raw) == "null" {
		return addresses, nil
	}
	var pairs [][]*string
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return nil, err
	}
	for _, pair := range pairs {
		if len(pair) != 2 {
			return nil, errUnexpectedMailAddressColumns
		}
		address := mailAddress{}
		if pair[0] != nil {
			address.Name = *pair[0]
		}
		if pair[1] != nil {
			address.Address = *pair[1]
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}

func normalizedContentType(value string) string {
	if semicolon := strings.IndexByte(value, ';'); semicolon >= 0 {
		value = value[:semicolon]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

// truncateUTF8 cuts s to at most limit bytes without splitting a UTF-8 sequence.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

var blankLinesPattern = regexp.MustCompile(`\n{3,}`)

// htmlRawTextElements are elements whose content is raw text that is never shown.
var htmlRawTextElements = map[string]bool{"script": true, "style": true, "title": true}

// htmlHeadElements are the start tags that can be inside <head>. Any other start tag,
// such as <p> or <div>, ends a <head> that has no end tag, as in an HTML5 parser.
var htmlHeadElements = map[string]bool{
	"": true, "html": true, "head": true, "base": true, "basefont": true, "bgsound": true, "link": true,
	"meta": true, "noscript": true, "script": true, "style": true, "template": true, "title": true,
}

// htmlBreakElements end a line: a <br> tag, or the end tag of a block element.
var htmlBreakElements = map[string]bool{
	"p": true, "div": true, "tr": true, "li": true, "table": true, "blockquote": true, "pre": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// htmlToText is a fallback for an HTML body part the provider did not convert. It drops
// comments, tags, the head, and script, style and title content, keeps block boundaries
// as line breaks, and decodes entities once, after the tags are gone. A "<" starts a tag
// only when a letter, "/", "!", or "?" follows it, so text such as "3<5 but 7>2" is kept.
// Quoted attribute values may contain ">".
//
// Mail HTML is often not well formed, so a missing end tag must not hide the rest of the
// body: a <head> without </head> ends at <body>, at the first start tag that cannot be in
// a head, or at text that is not white space; a <title>, <style> or <script> without its
// end tag is described at skipHTMLElementContent; and a "<!--" without "-->" is dropped
// alone.
func htmlToText(value string) string {
	var text strings.Builder
	inHead, headDone := false, false
	writeText := func(s string) {
		if inHead {
			if strings.TrimLeft(s, " \t\r\n\f") == "" {
				return
			}
			inHead, headDone = false, true
		}
		text.WriteString(s)
	}
	for i := 0; i < len(value); {
		if value[i] != '<' || i+1 >= len(value) || !isHTMLTagStart(value[i+1]) {
			next := strings.IndexByte(value[i+1:], '<')
			if next < 0 {
				writeText(value[i:])
				break
			}
			writeText(value[i : i+1+next])
			i += 1 + next
			continue
		}
		if strings.HasPrefix(value[i:], "<!--") {
			end := strings.Index(value[i+4:], "-->")
			if end < 0 {
				// No end of comment: drop only "<!--" and keep the text after it.
				i += 4
				continue
			}
			i += 4 + end + 3
			continue
		}
		name, closing, selfClosing, end := scanHTMLTag(value, i)
		if end < 0 {
			// No end of tag: keep the rest as text rather than lose it.
			writeText(value[i:])
			break
		}
		i = end
		if inHead {
			switch {
			case closing && name == "head", !closing && name == "body":
				inHead, headDone = false, true
			case !closing && !htmlHeadElements[name]:
				inHead, headDone = false, true
			case closing:
				// Other end tags inside the head do not show anything.
				continue
			}
		}
		switch {
		case name == "br" || (closing && htmlBreakElements[name]):
			text.WriteByte('\n')
		case !closing && name == "head" && !inHead && !headDone && !selfClosing:
			inHead = true
		case !closing && !selfClosing && htmlRawTextElements[name]:
			i = skipHTMLElementContent(value, i, name)
		}
	}
	decoded := html.UnescapeString(text.String())
	lines := strings.Split(decoded, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t\u00a0")
	}
	decoded = blankLinesPattern.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(decoded)
}

func isHTMLTagStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '/' || c == '!' || c == '?'
}

// scanHTMLTag reads the tag that starts at value[start] == '<'. It returns the lower-case
// element name, whether it is an end tag or self-closing, and the index just after its
// closing '>', or -1 when the tag does not end. A '>' inside a quoted attribute value does
// not end the tag.
func scanHTMLTag(value string, start int) (name string, closing, selfClosing bool, end int) {
	i := start + 1
	if value[i] == '/' {
		closing = true
		i++
	}
	nameStart := i
	for i < len(value) && (isASCIILetter(value[i]) || (i > nameStart && value[i] >= '0' && value[i] <= '9')) {
		i++
	}
	name = strings.ToLower(value[nameStart:i])
	var quote, previous byte
	for ; i < len(value); i++ {
		c := value[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case (c == '"' || c == '\'') && previous == '=':
			// A quote starts a quoted value only right after "=" (spaces between are skipped).
			quote = c
		case c == '>':
			selfClosing = i > start+1 && value[i-1] == '/'
			return name, closing, selfClosing, i + 1
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != '\f' {
			previous = c
		}
	}
	return name, closing, false, -1
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// skipHTMLElementContent returns the index after the end tag of a script, style or title
// element whose content starts at from. When the end tag is missing, the content ends
// before the next "<body" or "</head" tag, because those cannot be part of the element in
// mail HTML. When neither is found, only the start tag is skipped and the content is
// shown, so that a missing end tag never hides the rest of the body.
func skipHTMLElementContent(value string, from int, name string) int {
	if end := findHTMLTag(value, from, name, true); end >= 0 {
		return end
	}
	stop := -1
	for _, candidate := range []struct {
		name    string
		closing bool
	}{{"body", false}, {"head", true}} {
		if at := indexHTMLTag(value, from, candidate.name, candidate.closing); at >= 0 && (stop < 0 || at < stop) {
			stop = at
		}
	}
	if stop >= 0 {
		return stop
	}
	return from
}

// findHTMLTag returns the index after the first tag with this name at or after from, or
// -1 when there is none or it does not end.
func findHTMLTag(value string, from int, name string, closing bool) int {
	at := indexHTMLTag(value, from, name, closing)
	if at < 0 {
		return -1
	}
	_, _, _, end := scanHTMLTag(value, at)
	return end
}

// indexHTMLTag returns the index of the first "<name" (or "</name" when closing) at or
// after from that is followed by a character that cannot continue the name, or -1. The
// name match ignores case, so a longer name such as </titles> does not match </title>.
func indexHTMLTag(value string, from int, name string, closing bool) int {
	prefix := "<"
	if closing {
		prefix = "</"
	}
	for i := from; i+len(prefix)+len(name) <= len(value); i++ {
		if value[i] != '<' || !strings.HasPrefix(value[i:], prefix) {
			continue
		}
		nameEnd := i + len(prefix) + len(name)
		if !strings.EqualFold(value[i+len(prefix):nameEnd], name) {
			continue
		}
		if nameEnd < len(value) && (isASCIILetter(value[nameEnd]) || (value[nameEnd] >= '0' && value[nameEnd] <= '9')) {
			continue
		}
		return i
	}
	return -1
}
