// ABOUTME: Draft revision for "mail draft --replace-id": fields not given come from the earlier
// ABOUTME: draft, and after the new draft is saved the earlier one moves to Trash.
package cli

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

// mailDraftReplaced reports the earlier draft that a new draft replaced: its id and folder,
// and its id in Trash after the move.
type mailDraftReplaced struct {
	ID      string `json:"id"`
	Folder  string `json:"folder"`
	MovedTo string `json:"moved_to"`
	TrashID string `json:"trash_id,omitempty"`
}

// fillFromReplacedDraft reads the earlier draft and takes from it every field that msg
// does not set: to, cc, bcc, subject, from, the body and the threading headers. The body
// is the draft's text; an HTML draft loses its formatting.
func fillFromReplacedDraft(ctx context.Context, c *client.Client, sess client.Session, folder, id string, msg *mailSendInput, bodyGiven bool) *cliErrors.Error {
	old, err := fetchMailRead(ctx, c, sess, mailReadOptions{id: id, folder: folder, bodyFormat: "text", links: mailLinksFull, maxBodyBytes: mailReadMaximumMaxBodyBytes})
	if err != nil {
		cerr := toCLIError(err, "api_error")
		cerr.Message = fmt.Sprintf("could not read the draft to replace (%s in %s): %s", id, folder, cerr.Message)
		return cerr
	}
	// Only a draft is replaced, so --replace-id cannot move other mail to Trash.
	if !old.draft && !strings.EqualFold(folder, mailDraftDefaultFolder) {
		return invalidArgument(fmt.Sprintf("message %s in %s is not a draft; --replace-id replaces only drafts", id, folder))
	}
	if old.BodyTruncated {
		return invalidArgument(fmt.Sprintf("the draft to replace (%s in %s) has a body over %d bytes; give --body", id, folder, mailReadMaximumMaxBodyBytes))
	}
	headers, cerr := fetchMailThreadHeaders(ctx, c, sess, old)
	if cerr != nil {
		return cerr
	}
	for _, field := range []struct {
		target *[]string
		source []mailAddress
	}{{&msg.To, old.To}, {&msg.Cc, old.Cc}, {&msg.Bcc, old.Bcc}} {
		if len(*field.target) > 0 {
			continue
		}
		for _, address := range field.source {
			if address.Address != "" {
				*field.target = append(*field.target, headerAddress(address))
			}
		}
	}
	if msg.Subject == "" {
		msg.Subject = old.Subject
	}
	if msg.From == "" && len(old.From) > 0 && old.From[0].Address != "" {
		msg.From = (&mail.Address{Name: strings.Join(strings.Fields(old.From[0].Name), " "), Address: old.From[0].Address}).String()
	}
	if !bodyGiven && msg.Body == "" {
		msg.Body = old.Body
	}
	if ids := messageIDs(headers.inReplyTo); len(ids) > 0 {
		msg.inReplyTo = ids[0]
	}
	msg.references = messageIDs(headers.references)
	if len(msg.references) > mailReplyMaxReferences {
		msg.references = append(msg.references[:1], msg.references[len(msg.references)-mailReplyMaxReferences+1:]...)
	}
	return nil
}

// moveReplacedDraft moves the earlier draft to Trash, the soft delete of mail move.
func moveReplacedDraft(ctx context.Context, c *client.Client, sess client.Session, folder, id string) (mailDraftReplaced, *cliErrors.Error) {
	replaced := mailDraftReplaced{ID: id, Folder: folder, MovedTo: mailTrashDefaultFolder}
	plan, err := newMailMovePlan(id, folder, mailTrashDefaultFolder)
	if err != nil {
		return replaced, invalidArgument(err.Error())
	}
	result, cerr := executeMailMove(ctx, c, sess, plan, false, "")
	if cerr != nil {
		return replaced, cerr
	}
	if result.Destination.ID != nil {
		replaced.TrashID = *result.Destination.ID
	}
	return replaced, nil
}
