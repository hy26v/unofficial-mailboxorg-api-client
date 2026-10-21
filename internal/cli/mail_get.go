// ABOUTME: The "mail get" command: fetches one message's safe metadata (subject, from,
// ABOUTME: to, cc, size, dates, and safe flag state) by id + folder via GET /mail?action=list, never the raw
// ABOUTME: headers or body/attachments.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

// OX mail column ids: id, folder_id, subject, from, to, cc, size, sent_date,
// received_date, attachment, flags.
const mailGetColumns = "600,601,607,603,604,605,608,609,610,602,611"

func init() {
	operations.Register(operations.Operation{
		Name:       "mail.get",
		Resource:   "mail",
		Action:     "get",
		Summary:    "Get one message's metadata (not body/attachments) by id and folder",
		Method:     http.MethodGet,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":     {Type: "string", Required: true, Description: "message id"},
			"folder": {Type: "string", Required: true, Description: "folder id, e.g. \"default0/INBOX\""},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder", "subject", "from", "to", "cc", "size", "sent_date", "received_date", "attachment", "seen", "flagged", "fingerprint"},
			ScalarFields: []string{"id", "folder", "subject", "size", "sent_date", "received_date", "attachment", "seen", "flagged", "fingerprint"},
		},
	})
}

type mailAddress struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

type mailGetResult struct {
	ID            string        `json:"id"`
	Folder        string        `json:"folder"`
	Subject       string        `json:"subject"`
	From          []mailAddress `json:"from"`
	To            []mailAddress `json:"to"`
	Cc            []mailAddress `json:"cc"`
	Size          int64         `json:"size"`
	SentDate      *string       `json:"sent_date"`
	ReceivedDate  *string       `json:"received_date"`
	HasAttachment bool          `json:"attachment"`
	Seen          bool          `json:"seen"`
	Flagged       bool          `json:"flagged"`
	Fingerprint   string        `json:"fingerprint"`
}

func runMailGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")
	folder := f.String("folder")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if folder == "" {
		return exitWithError(stderr, llm, invalidArgument("--folder is required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	result, err := fetchMailMetadata(ctx, c, sess, id, folder)
	if err != nil {
		cerr := toCLIError(err, "api_error")
		writeErr(stderr, llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}

	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}

	sentDate := ""
	if result.SentDate != nil {
		sentDate = *result.SentDate
	}
	receivedDate := ""
	if result.ReceivedDate != nil {
		receivedDate = *result.ReceivedDate
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: result.ID},
		{Name: "folder", Value: result.Folder},
		{Name: "subject", Value: result.Subject},
		{Name: "from", Value: addressesToString(result.From)},
		{Name: "to", Value: addressesToString(result.To)},
		{Name: "sent_date", Value: sentDate},
		{Name: "received_date", Value: receivedDate},
		{Name: "seen", Value: strconv.FormatBool(result.Seen)},
		{Name: "flagged", Value: strconv.FormatBool(result.Flagged)},
		{Name: "fingerprint", Value: result.Fingerprint},
	})
	return 0
}

// fetchMailMetadata performs the safe metadata-only read shared by mail.get, verified
// moves, and move-many. It deliberately uses mail?action=list with an explicit safe column
// set; mail?action=get may include raw headers and message bodies.
func fetchMailMetadata(ctx context.Context, c *client.Client, sess client.Session, id, folder string) (mailGetResult, error) {
	reqBody, err := json.Marshal([]map[string]string{{"id": id, "folder": folder}})
	if err != nil {
		return mailGetResult{}, &cliErrors.Error{Code: "invalid_argument", Message: "could not encode mail metadata request: " + err.Error()}
	}
	params := url.Values{"columns": {mailGetColumns}, "data": {string(reqBody)}, "timezone": {mailDateTimezone}}
	data, err := c.Do(ctx, http.MethodGet, "/mail", "list", sess, params)
	if err != nil {
		return mailGetResult{}, err
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return mailGetResult{}, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail metadata response"}
	}
	if len(rows) == 0 {
		return mailGetResult{}, &cliErrors.Error{Code: "not_found", Message: "message not found: id=" + id + " folder=" + folder}
	}
	if len(rows) != 1 {
		return mailGetResult{}, &cliErrors.Error{Code: "invalid_response", Message: "mail metadata response contained multiple messages"}
	}
	result, decodeErr := decodeMailGetRow(rows[0])
	if decodeErr != nil {
		return mailGetResult{}, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail metadata row: " + decodeErr.Error()}
	}
	if result.ID != id || result.Folder != folder {
		return mailGetResult{}, &cliErrors.Error{Code: "invalid_response", Message: "mail metadata response did not match the requested message"}
	}
	return result, nil
}

func addressesToString(addrs []mailAddress) string {
	out := ""
	for i, a := range addrs {
		if i > 0 {
			out += ", "
		}
		out += a.Address
	}
	return out
}

// decodeMailGetRow decodes one OX mail row for columns "600,601,607,603,604,605,608,609,610,602,611":
// id, folder_id, subject, from, to, cc (all address lists as [personal, address] pairs),
// size, sent_date, received_date (epoch milliseconds), attachment (boolean), and flags
// (bitmask).
func decodeMailGetRow(row []json.RawMessage) (mailGetResult, error) {
	if len(row) != 11 {
		return mailGetResult{}, errUnexpectedMailGetColumns
	}

	var id, folder string
	var subject *string
	if err := json.Unmarshal(row[0], &id); err != nil {
		return mailGetResult{}, err
	}
	if err := json.Unmarshal(row[1], &folder); err != nil {
		return mailGetResult{}, err
	}
	if err := json.Unmarshal(row[2], &subject); err != nil {
		return mailGetResult{}, err
	}
	if subject == nil {
		return mailGetResult{}, errMissingMailSubject
	}

	from, err := decodeMailAddresses(row[3])
	if err != nil {
		return mailGetResult{}, err
	}
	to, err := decodeMailAddresses(row[4])
	if err != nil {
		return mailGetResult{}, err
	}
	cc, err := decodeMailAddresses(row[5])
	if err != nil {
		return mailGetResult{}, err
	}

	var size *int64
	if err := json.Unmarshal(row[6], &size); err != nil {
		return mailGetResult{}, err
	}
	if size == nil {
		return mailGetResult{}, errMissingMailSize
	}
	if *size < 0 {
		return mailGetResult{}, errNegativeMailSize
	}

	var sentMs, receivedMs *int64
	if err := json.Unmarshal(row[7], &sentMs); err != nil {
		return mailGetResult{}, err
	}
	if err := json.Unmarshal(row[8], &receivedMs); err != nil {
		return mailGetResult{}, err
	}
	if (sentMs != nil && *sentMs <= 0) || (receivedMs != nil && *receivedMs <= 0) {
		return mailGetResult{}, errInvalidMailGetDate
	}
	var sentDate, receivedDate *string
	if sentMs != nil {
		value := time.UnixMilli(*sentMs).UTC().Format(time.RFC3339)
		sentDate = &value
	}
	if receivedMs != nil {
		value := time.UnixMilli(*receivedMs).UTC().Format(time.RFC3339)
		receivedDate = &value
	}

	var attachment *bool
	if err := json.Unmarshal(row[9], &attachment); err != nil {
		return mailGetResult{}, err
	}
	if attachment == nil {
		return mailGetResult{}, errMissingMailAttachmentFlag
	}

	var flags *int64
	if err := json.Unmarshal(row[10], &flags); err != nil {
		return mailGetResult{}, err
	}
	if flags == nil {
		return mailGetResult{}, errMissingMailFlags
	}
	if *flags < 0 {
		return mailGetResult{}, errNegativeMailFlags
	}

	result := mailGetResult{
		ID:            id,
		Folder:        folder,
		Subject:       *subject,
		From:          from,
		To:            to,
		Cc:            cc,
		Size:          *size,
		SentDate:      sentDate,
		ReceivedDate:  receivedDate,
		HasAttachment: *attachment,
		Seen:          *flags&mailFlagSeen != 0,
		Flagged:       *flags&mailFlagFlagged != 0,
	}
	result.Fingerprint = mailFingerprint(result)
	return result, nil
}

// mailFingerprint identifies message content using only safe stable metadata. Folder,
// provider-local id, and mutable flags are excluded so the value remains invariant across
// a move while still detecting a wrong destination message.
func mailFingerprint(message mailGetResult) string {
	stable := struct {
		Subject       string        `json:"subject"`
		From          []mailAddress `json:"from"`
		To            []mailAddress `json:"to"`
		Cc            []mailAddress `json:"cc"`
		Size          int64         `json:"size"`
		SentDate      *string       `json:"sent_date"`
		ReceivedDate  *string       `json:"received_date"`
		HasAttachment bool          `json:"attachment"`
	}{
		Subject: message.Subject, From: message.From, To: message.To, Cc: message.Cc,
		Size: message.Size, SentDate: message.SentDate, ReceivedDate: message.ReceivedDate,
		HasAttachment: message.HasAttachment,
	}
	encoded, _ := json.Marshal(stable)
	sum := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", sum)
}

func decodeMailAddresses(raw json.RawMessage) ([]mailAddress, error) {
	var pairs [][]json.RawMessage
	if err := json.Unmarshal(raw, &pairs); err != nil {
		return nil, err
	}
	addrs := make([]mailAddress, 0, len(pairs))
	for _, p := range pairs {
		if len(p) != 2 {
			return nil, errUnexpectedMailAddressColumns
		}
		var name, address *string
		if err := json.Unmarshal(p[0], &name); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(p[1], &address); err != nil {
			return nil, err
		}
		if address == nil || strings.TrimSpace(*address) == "" {
			return nil, errMissingMailAddress
		}
		addr := mailAddress{}
		if name != nil {
			addr.Name = *name
		}
		if address != nil {
			addr.Address = *address
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

var errUnexpectedMailGetColumns = &cliErrors.Error{Code: "invalid_response", Message: "unexpected number of mail columns in response"}
var errMissingMailSubject = &cliErrors.Error{Code: "invalid_response", Message: "mail response did not include a subject"}
var errMissingMailSize = &cliErrors.Error{Code: "invalid_response", Message: "mail response did not include a size"}
var errNegativeMailSize = &cliErrors.Error{Code: "invalid_response", Message: "mail response contained a negative size"}
var errInvalidMailGetDate = &cliErrors.Error{Code: "invalid_response", Message: "mail response contained a non-positive date"}
var errMissingMailAttachmentFlag = &cliErrors.Error{Code: "invalid_response", Message: "mail response did not include its attachment flag"}
var errMissingMailFlags = &cliErrors.Error{Code: "invalid_response", Message: "mail response did not include its flags"}
var errNegativeMailFlags = &cliErrors.Error{Code: "invalid_response", Message: "mail response contained negative flags"}
var errUnexpectedMailAddressColumns = &cliErrors.Error{Code: "invalid_response", Message: "mail address did not contain exactly two columns"}
var errMissingMailAddress = &cliErrors.Error{Code: "invalid_response", Message: "mail address entry did not include an address"}
