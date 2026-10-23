// ABOUTME: The "mail attachment" command: one mail attachment as extracted text, as base64, or
// ABOUTME: saved to an owner-only file; the attachment id is checked against the message first.
package cli

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	mailAttachmentDefaultTextBytes   = 40_000
	mailAttachmentDefaultBase64Bytes = 30_000
	mailAttachmentMinimumBytes       = 1 << 10
	mailAttachmentMaximumBytes       = 16 << 20
)

func init() {
	operations.Register(operations.Operation{
		Name:       "mail.attachment",
		Resource:   "mail",
		Action:     "attachment",
		Summary:    "Get one email attachment (PDF, Word, Excel, ODT, HTML or text file, image, ...) as extracted text or as base64, or save it to an owner-only file",
		Keywords:   []string{"download", "file", "pdf", "document", "docx", "contract", "invoice", "extract", "attached", "open attachment"},
		Method:     http.MethodGet,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":         {Type: "string", Required: true, Description: "message id"},
			"folder":     {Type: "string", Required: true, Description: "folder id of the message, e.g. \"default0/INBOX\""},
			"attachment": {Type: "string", Required: true, Description: "attachment id from mail read's attachments list"},
			"content":    {Type: "string", Required: false, Description: "text (default; the text of a PDF, DOCX, XLSX, ODT, HTML or text file, with page_count, pages_without_text, has_images and notes about what has no text) or base64 (the file itself)"},
			"max-bytes":  {Type: "int", Required: false, Description: fmt.Sprintf("maximum bytes returned, %d-%d: of text (default %d; truncated and next_offset report a cut) or of the file for base64 (default %d; a larger file fails)", mailAttachmentMinimumBytes, mailAttachmentMaximumBytes, mailAttachmentDefaultTextBytes, mailAttachmentDefaultBase64Bytes)},
			"offset":     {Type: "int", Required: false, Description: "with content text, the byte offset into the extracted text to start at: the next_offset of a cut result"},
			"save-to":    {Type: "string", Required: false, Description: "local path for the whole attachment, written owner-only; then content, max-bytes and offset do not apply"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder", "attachment", "filename", "content_type", "size", "format", "text", "content_base64", "bytes", "total_bytes", "offset", "truncated", "next_offset", "page_count", "pages_without_text", "has_images", "notes", "saved_to", "bytes_written"},
			ScalarFields: []string{"id", "folder", "attachment", "filename", "content_type", "size", "format", "text", "content_base64", "bytes", "total_bytes", "offset", "truncated", "next_offset", "page_count", "has_images", "saved_to", "bytes_written"},
		},
	})
}

// mailAttachmentSaved is the result of mail attachment --save-to.
type mailAttachmentSaved struct {
	ID           string `json:"id"`
	Folder       string `json:"folder"`
	Attachment   string `json:"attachment"`
	Filename     string `json:"filename"`
	ContentType  string `json:"content_type"`
	Size         *int64 `json:"size"`
	SavedTo      string `json:"saved_to"`
	BytesWritten int    `json:"bytes_written"`
}

// mailAttachmentContent is the result of mail attachment without --save-to: the text of
// the file, or the file as base64.
type mailAttachmentContent struct {
	ID            string  `json:"id"`
	Folder        string  `json:"folder"`
	Attachment    string  `json:"attachment"`
	Filename      string  `json:"filename"`
	ContentType   string  `json:"content_type"`
	Size          *int64  `json:"size"`
	Format        string  `json:"format"`
	Text          *string `json:"text,omitempty"`
	ContentBase64 *string `json:"content_base64,omitempty"`
	// Bytes is the length of the returned text, or of the file for base64; TotalBytes
	// is the length of all extracted text, or of the file.
	Bytes            int      `json:"bytes"`
	TotalBytes       int      `json:"total_bytes"`
	Offset           int      `json:"offset"`
	Truncated        bool     `json:"truncated"`
	NextOffset       *int     `json:"next_offset,omitempty"`
	PageCount        *int     `json:"page_count,omitempty"`
	PagesWithoutText []int    `json:"pages_without_text,omitempty"`
	HasImages        bool     `json:"has_images"`
	Notes            []string `json:"notes,omitempty"`
}

func runMailAttachment(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.attachment", args,
		checkedFlag("content", func(value string) *cliErrors.Error {
			if value := strings.TrimSpace(value); value != "text" && value != "base64" {
				return invalidArgument("--content must be text or base64")
			}
			return nil
		}),
		intFlag("max-bytes", mailAttachmentMinimumBytes, mailAttachmentMaximumBytes, fmt.Sprintf("--max-bytes must be an integer between %d and %d", mailAttachmentMinimumBytes, mailAttachmentMaximumBytes)),
		intFlag("offset", 0, math.MaxInt, "--offset must be a non-negative integer"),
	)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := strings.TrimSpace(f.String("id"))
	folder := strings.TrimSpace(f.String("folder"))
	attachmentID := strings.TrimSpace(f.String("attachment"))
	saveTo, saving := f.Lookup("save-to")
	for _, required := range []struct{ flag, value string }{{"--id", id}, {"--folder", folder}, {"--attachment", attachmentID}} {
		if required.value == "" {
			return exitWithError(stderr, llm, invalidArgument(required.flag+" is required"))
		}
	}
	format := "text"
	if value, ok := f.Lookup("content"); ok {
		format = strings.TrimSpace(value)
	}
	maxBytes, maxBytesSet := f.Int("max-bytes")
	offset, offsetSet := f.Int("offset")
	_, contentSet := f.Lookup("content")
	switch {
	case saving && strings.TrimSpace(saveTo) == "":
		return exitWithError(stderr, llm, invalidArgument("--save-to must not be blank"))
	case saving && (contentSet || maxBytesSet || offsetSet):
		return exitWithError(stderr, llm, invalidArgument("--save-to writes the whole attachment; --content, --max-bytes and --offset apply only without it"))
	case format == "base64" && offsetSet:
		return exitWithError(stderr, llm, invalidArgument("--offset applies only to --content text"))
	}
	if !maxBytesSet {
		maxBytes = mailAttachmentDefaultTextBytes
		if format == "base64" {
			maxBytes = mailAttachmentDefaultBase64Bytes
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
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	attachment, matches := findMailReadAttachment(message.Attachments, message.NestedMessages, attachmentID)
	if attachment == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "attachment not found in message: attachment=" + attachmentID + " id=" + id + " folder=" + folder + "; mail read lists the attachment ids"})
	}
	if matches > 1 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_argument", Message: fmt.Sprintf("attachment id is ambiguous: %d attachments in the message and its nested messages have id %s, so the download could return the wrong part; nothing was read: id=%s folder=%s", matches, attachmentID, id, folder)})
	}

	params := url.Values{"folder": {folder}, "id": {id}, "attachment": {attachmentID}, "save": {"1"}}
	content, err := c.DoRaw(ctx, http.MethodGet, "/mail", "attachment", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if saving {
		if err := writePrivateFile(saveTo, content); err != nil {
			return exitWithError(stderr, llm, invalidArgument("could not write --save-to file: "+err.Error()))
		}
		result := mailAttachmentSaved{id, folder, attachmentID, attachment.Filename, attachment.ContentType, attachment.Size, saveTo, len(content)}
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

	result := mailAttachmentContent{ID: id, Folder: folder, Attachment: attachmentID, Filename: attachment.Filename,
		ContentType: attachment.ContentType, Size: attachment.Size, Format: format, TotalBytes: len(content)}
	if format == "base64" {
		if len(content) > maxBytes {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("the attachment is %d bytes, over --max-bytes %d; raise --max-bytes, read it with --content text, or save it with the mbx CLI and --save-to", len(content), maxBytes)})
		}
		encoded := base64.StdEncoding.EncodeToString(content)
		result.ContentBase64, result.Bytes = &encoded, len(content)
		result.HasImages = strings.HasPrefix(attachment.ContentType, "image/")
	} else {
		extracted, cerr := extractAttachmentText(content, attachment.ContentType, attachment.Filename)
		if cerr != nil {
			return exitWithError(stderr, llm, cerr)
		}
		text := extracted.text
		result.TotalBytes = len(text)
		result.PageCount, result.PagesWithoutText, result.HasImages, result.Notes = extracted.pageCount, extracted.pagesWithoutText, extracted.hasImages, extracted.notes
		start := min(offset, len(text))
		for start > 0 && start < len(text) && !isUTF8Start(text[start]) {
			start--
		}
		part := truncateUTF8(text[start:], maxBytes)
		result.Offset, result.Bytes = start, len(part)
		if end := start + len(part); end < len(text) {
			result.Truncated, result.NextOffset = true, &end
		}
		result.Text = &part
	}
	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	fields := []render.Field{
		{Name: "id", Value: result.ID},
		{Name: "folder", Value: result.Folder},
		{Name: "attachment", Value: result.Attachment},
		{Name: "filename", Value: result.Filename},
		{Name: "content_type", Value: result.ContentType},
		{Name: "format", Value: result.Format},
		{Name: "bytes", Value: strconv.Itoa(result.Bytes)},
		{Name: "total_bytes", Value: strconv.Itoa(result.TotalBytes)},
	}
	if result.PageCount != nil {
		fields = append(fields, render.Field{Name: "page_count", Value: strconv.Itoa(*result.PageCount)})
	}
	if len(result.PagesWithoutText) > 0 {
		pages := make([]string, len(result.PagesWithoutText))
		for i, page := range result.PagesWithoutText {
			pages[i] = strconv.Itoa(page)
		}
		fields = append(fields, render.Field{Name: "pages_without_text", Value: strings.Join(pages, ",")})
	}
	fields = append(fields, render.Field{Name: "has_images", Value: strconv.FormatBool(result.HasImages)})
	for _, note := range result.Notes {
		fields = append(fields, render.Field{Name: "note", Value: note})
	}
	render.KeyValueTable(stdout, fields)
	fmt.Fprintln(stdout)
	switch {
	case result.ContentBase64 != nil:
		fmt.Fprintln(stdout, *result.ContentBase64)
	case result.Text != nil:
		body := render.SafeMultilineText(*result.Text)
		fmt.Fprint(stdout, body)
		if !strings.HasSuffix(body, "\n") {
			fmt.Fprintln(stdout)
		}
		if result.NextOffset != nil {
			fmt.Fprintf(stdout, "\n(text cut at %d of %d bytes; continue with --offset %d)\n", result.Offset+result.Bytes, result.TotalBytes, *result.NextOffset)
		}
	}
	return 0
}

// isUTF8Start reports whether b starts a UTF-8 sequence.
func isUTF8Start(b byte) bool { return b&0xC0 != 0x80 }

// findMailReadAttachment finds an attachment id that mail read lists: among the message's
// own attachments or inside its nested messages. It returns the attachment and how many
// listed attachments have this id. The download request is the same for both; whether OX
// serves a nested message's attachment by this id is not documented and has not been
// checked against a live server. Because the download names only the outer message, an
// id that is listed more than once (in the outer message and a nested one, or in two
// nested messages) could return a different part under the wrong file name, so the caller
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
