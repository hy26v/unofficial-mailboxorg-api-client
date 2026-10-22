// ABOUTME: Reply drafts and signatures for mail draft and mail send: recipients, subject and
// ABOUTME: threading headers from the original message, an optional quote, and snippet signatures.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

// mailReplyMaxReferences bounds the References header of a reply: the first message id
// of the thread and the newest ones.
const mailReplyMaxReferences = 20

// mailReplyQuoteBytes bounds the original text that --quote adds to a reply.
const mailReplyQuoteBytes = 64 << 10

// mailReplyOptions are the reply arguments of mail draft.
type mailReplyOptions struct {
	folder   string
	id       string
	replyAll bool
	quote    bool
}

func (o mailReplyOptions) active() bool { return o.id != "" }

// parseMailReplyOptions reads --reply-folder, --reply-id, --reply-all and --quote.
func parseMailReplyOptions(f commandFlags) (mailReplyOptions, *cliErrors.Error) {
	opts := mailReplyOptions{
		folder:   strings.TrimSpace(f.String("reply-folder")),
		id:       strings.TrimSpace(f.String("reply-id")),
		replyAll: f.Bool("reply-all"),
		quote:    f.Bool("quote"),
	}
	_, folderSet := f.Lookup("reply-folder")
	_, idSet := f.Lookup("reply-id")
	switch {
	case (folderSet || idSet) && (opts.folder == "" || opts.id == ""):
		return opts, invalidArgument("--reply-id and --reply-folder must be given together: the id and folder of the message to answer, from mail search or mail list")
	case !opts.active() && (opts.replyAll || opts.quote):
		return opts, invalidArgument("--reply-all and --quote need --reply-id and --reply-folder")
	}
	return opts, nil
}

// messageIDPattern accepts one message id in angle brackets, without spaces or line
// breaks, so it is safe to write into a header.
var messageIDPattern = regexp.MustCompile(`^<[^<>\s]+>$`)

// messageIDs returns the valid message ids of a References or In-Reply-To value.
func messageIDs(value string) []string {
	var ids []string
	for _, field := range strings.Fields(value) {
		if messageIDPattern.MatchString(field) {
			ids = append(ids, field)
		}
	}
	return ids
}

// firstHeader returns the first value of a header from the provider's headers object.
func firstHeader(headers map[string][]string, name string) string {
	if values := headers[strings.ToLower(name)]; len(values) > 0 {
		return strings.TrimSpace(values[0])
	}
	return ""
}

// mailThreadHeaders are the threading headers of the original message.
type mailThreadHeaders struct {
	messageID  string
	references string
	inReplyTo  string
	replyTo    string
}

// fetchMailThreadHeaders reads Message-ID, References, In-Reply-To and Reply-To. The
// provider's headers object usually has them; when it has no Message-ID, the message
// source is read for its header section.
func fetchMailThreadHeaders(ctx context.Context, c *client.Client, sess client.Session, original mailReadResult) (mailThreadHeaders, *cliErrors.Error) {
	headers := mailThreadHeaders{
		messageID:  firstHeader(original.headers, "Message-ID"),
		references: firstHeader(original.headers, "References"),
		inReplyTo:  firstHeader(original.headers, "In-Reply-To"),
		replyTo:    firstHeader(original.headers, "Reply-To"),
	}
	if headers.messageID != "" {
		return headers, nil
	}
	params := url.Values{"folder": {original.Folder}, "id": {original.ID}, "src": {"1"}, "unseen": {"true"}}
	data, err := c.Do(ctx, http.MethodGet, "/mail", "get", sess, params)
	if err != nil {
		return headers, toCLIError(err, "api_error")
	}
	var source string
	if json.Unmarshal(data, &source) != nil {
		return headers, &cliErrors.Error{Code: "invalid_response", Message: "could not decode the source of the message to answer"}
	}
	message, err := mail.ReadMessage(strings.NewReader(source))
	if err != nil {
		return headers, &cliErrors.Error{Code: "invalid_response", Message: "could not read the headers of the message to answer"}
	}
	headers.messageID = strings.TrimSpace(message.Header.Get("Message-Id"))
	headers.references = message.Header.Get("References")
	headers.inReplyTo = message.Header.Get("In-Reply-To")
	headers.replyTo = message.Header.Get("Reply-To")
	return headers, nil
}

// replyReferences is the References value of a reply: the original's references (or its
// In-Reply-To when it has none) followed by its Message-ID, at most
// mailReplyMaxReferences ids, keeping the first.
func replyReferences(headers mailThreadHeaders, messageID string) []string {
	ids := messageIDs(headers.references)
	if len(ids) == 0 {
		ids = messageIDs(headers.inReplyTo)
	}
	ids = append(ids, messageID)
	if len(ids) > mailReplyMaxReferences {
		ids = append(ids[:1], ids[len(ids)-mailReplyMaxReferences+1:]...)
	}
	return ids
}

// replySubject adds "Re: " unless the subject already starts with it.
func replySubject(subject string) string {
	trimmed := strings.TrimSpace(subject)
	if len(trimmed) >= 3 && strings.EqualFold(trimmed[:3], "re:") {
		return trimmed
	}
	return "Re: " + trimmed
}

// headerAddress formats a name and address for a header, without line breaks.
func headerAddress(address mailAddress) string {
	name := strings.Join(strings.Fields(address.Name), " ")
	return (&mail.Address{Name: name, Address: address.Address}).String()
}

// addressSet tracks lower-case addresses.
type addressSet map[string]bool

func (s addressSet) has(address string) bool { return s[strings.ToLower(strings.TrimSpace(address))] }
func (s addressSet) add(address string)      { s[strings.ToLower(strings.TrimSpace(address))] = true }

// replyRecipients chooses the reply's To and Cc. A reply goes to Reply-To, or else to the
// sender; a reply to the account's own message goes to that message's recipients. With
// replyAll, Cc has the other recipients, without the account's own addresses.
func replyRecipients(original mailReadResult, replyTo string, own addressSet, replyAll bool) (to, cc []string) {
	var toAddresses, ccAddresses []mailAddress
	ownMessage := len(original.From) > 0 && own.has(original.From[0].Address)
	switch {
	case ownMessage:
		toAddresses = original.To
		if replyAll {
			ccAddresses = original.Cc
		}
	default:
		if list, err := mail.ParseAddressList(replyTo); err == nil && len(list) > 0 {
			for _, address := range list {
				toAddresses = append(toAddresses, mailAddress{Name: address.Name, Address: address.Address})
			}
		} else {
			toAddresses = original.From
		}
		if replyAll {
			ccAddresses = append(append([]mailAddress{}, original.To...), original.Cc...)
		}
	}
	seen := addressSet{}
	for _, address := range toAddresses {
		if address.Address == "" || seen.has(address.Address) {
			continue
		}
		seen.add(address.Address)
		to = append(to, headerAddress(address))
	}
	for _, address := range ccAddresses {
		if address.Address == "" || seen.has(address.Address) || own.has(address.Address) {
			continue
		}
		seen.add(address.Address)
		cc = append(cc, headerAddress(address))
	}
	return to, cc
}

// replySender picks the sender address of a reply: the account's own address that the
// original was sent to, so a reply to mail for an alias comes from that alias. For the
// account's own message, it is the address that sent it.
func replySender(original mailReadResult, headers map[string][]string, senders []mailSender) *mail.Address {
	candidates := append(append([]mailAddress{}, original.From...), original.To...)
	candidates = append(candidates, original.Cc...)
	for _, name := range []string{"Delivered-To", "X-Original-To"} {
		for _, value := range headers[strings.ToLower(name)] {
			candidates = append(candidates, mailAddress{Address: strings.Trim(strings.TrimSpace(value), "<>")})
		}
	}
	for _, candidate := range candidates {
		for _, sender := range senders {
			if strings.EqualFold(sender.Address, strings.TrimSpace(candidate.Address)) {
				return &mail.Address{Name: sender.Name, Address: sender.Address}
			}
		}
	}
	return nil
}

// quoteReply is the original text with "> " before each line, after a line that names
// the sender and the date.
func quoteReply(original mailReadResult) string {
	date := ""
	if original.SentDate != nil {
		date = *original.SentDate
	} else if original.ReceivedDate != nil {
		date = *original.ReceivedDate
	}
	author := "the sender"
	if len(original.From) > 0 {
		author = original.From[0].Address
		if name := strings.Join(strings.Fields(original.From[0].Name), " "); name != "" {
			author = name + " <" + author + ">"
		}
	}
	intro := author + " wrote:"
	if date != "" {
		intro = "On " + date + ", " + intro
	}
	lines := strings.Split(strings.TrimRight(original.Body, "\n"), "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + line
		}
	}
	quoted := intro + "\n" + strings.Join(lines, "\n")
	if original.BodyTruncated {
		quoted += "\n> [quoted text cut]"
	}
	return quoted
}

// prepareMailReply fills a reply draft from the original message: the threading headers,
// the recipients and subject unless given, the sender when none was given, and the quote.
// It returns the sender to use, or nil for the account default.
func prepareMailReply(ctx context.Context, c *client.Client, sess client.Session, reply mailReplyOptions, msg *mailSendInput, senders []mailSender) (*mail.Address, *cliErrors.Error) {
	maxBody := mailReadMinimumMaxBodyBytes
	if reply.quote {
		maxBody = mailReplyQuoteBytes
	}
	original, err := fetchMailRead(ctx, c, sess, mailReadOptions{id: reply.id, folder: reply.folder, bodyFormat: "text", links: mailLinksClean, maxBodyBytes: maxBody})
	if err != nil {
		return nil, toCLIError(err, "api_error")
	}
	headers, cerr := fetchMailThreadHeaders(ctx, c, sess, original)
	if cerr != nil {
		return nil, cerr
	}
	if !messageIDPattern.MatchString(headers.messageID) {
		return nil, &cliErrors.Error{Code: "invalid_argument", Message: fmt.Sprintf("the message to answer (%s/%s) has no valid Message-ID header, so a reply cannot be threaded; save the draft without --reply-id", reply.folder, reply.id)}
	}
	msg.inReplyTo = headers.messageID
	msg.references = replyReferences(headers, headers.messageID)

	own := addressSet{}
	for _, sender := range senders {
		own.add(sender.Address)
	}
	to, cc := replyRecipients(original, headers.replyTo, own, reply.replyAll)
	if len(msg.To) == 0 {
		msg.To = to
	}
	if len(msg.Cc) == 0 {
		msg.Cc = cc
	}
	if msg.Subject == "" {
		msg.Subject = replySubject(original.Subject)
	}
	if reply.quote {
		msg.Body = appendBodySection(msg.Body, quoteReply(original))
	}
	if msg.From != "" {
		return nil, nil
	}
	return replySender(original, original.headers, senders), nil
}

// appendBodySection adds a section, such as a signature or a quote, after the body with
// one blank line between them.
func appendBodySection(body, section string) string {
	if strings.TrimSpace(body) == "" {
		return section
	}
	return strings.TrimRight(body, "\n") + "\n\n" + section
}

// mailSignatureDefault selects the default signature of the mailbox.org settings.
const mailSignatureDefault = "default"

// htmlTagPattern finds markup in snippet content, which App Suite stores as HTML.
var htmlTagPattern = regexp.MustCompile(`<[A-Za-z/!][^>]*>`)

// fetchMailSignature returns the plain text of a signature: the snippet with this id, or
// for "default" the one the settings name for new mail (defaultSignature) or for replies
// (defaultReplyForwardSignature). It also returns the snippet id.
func fetchMailSignature(ctx context.Context, c *client.Client, sess client.Session, value string, reply bool) (string, string, *cliErrors.Error) {
	id := strings.TrimSpace(value)
	if id == mailSignatureDefault {
		key := "defaultSignature"
		if reply {
			key = "defaultReplyForwardSignature"
		}
		tree, err := fetchJSLobTree(ctx, c, sess, "io.ox/mail")
		if err != nil {
			return "", "", toCLIError(err, "api_error")
		}
		id = ""
		if raw, ok := tree[key]; ok {
			var text string
			if json.Unmarshal(raw, &text) == nil {
				id = strings.TrimSpace(text)
			} else {
				var number json.Number
				if json.Unmarshal(raw, &number) == nil {
					id = number.String()
				}
			}
		}
		if id == "" {
			return "", "", invalidArgument("no default signature is set for this kind of message (" + key + "); give a snippet id from snippet.list as --signature")
		}
	}
	data, err := c.Do(ctx, http.MethodGet, "/snippet", "get", sess, url.Values{"id": {id}})
	if err != nil {
		return "", "", toCLIError(err, "api_error")
	}
	var snippet snippetOut
	if json.Unmarshal(data, &snippet) != nil || snippet.ID != id || snippet.Content == nil {
		return "", "", &cliErrors.Error{Code: "invalid_response", Message: "could not decode signature snippet " + id}
	}
	text := strings.ReplaceAll(*snippet.Content, "\r\n", "\n")
	if htmlTagPattern.MatchString(text) {
		text = htmlToTextWithLinks(text, mailLinksClean)
	}
	return strings.TrimSpace(text), id, nil
}
