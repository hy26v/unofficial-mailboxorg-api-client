// ABOUTME: The "mail thread" command: the messages of one conversation across every mail folder,
// ABOUTME: Sent included, found by the subject without Re:/Fwd: prefixes and listed oldest first.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	mailThreadDefaultLimit = 50
	mailThreadMaxLimit     = 200
)

func init() {
	operations.Register(operations.Operation{
		Name:       "mail.thread",
		Resource:   "mail",
		Action:     "thread",
		Summary:    "List the messages of one conversation in every mail folder except Trash, Spam and Drafts, Sent included, oldest first: the messages whose subject without Re:, Fwd: and similar prefixes is the same",
		Keywords:   []string{"conversation", "thread", "history", "replies", "exchange", "correspondence"},
		Method:     http.MethodPut,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":       {Type: "string", Required: true, Description: "id of one message of the conversation"},
			"folder":   {Type: "string", Required: true, Description: "folder id of that message"},
			"limit":    {Type: "int", Required: false, Description: fmt.Sprintf("messages to return, the newest of the conversation, 1-%d (default %d)", mailThreadMaxLimit, mailThreadDefaultLimit)},
			"snippets": {Type: "bool", Required: false, Description: fmt.Sprintf("add the first %d characters of each body as snippet; reads each message, so it needs --limit %d or less", mailSnippetChars, mailSnippetMaxItems)},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"id", "folder", "subject", "from", "from_name", "to", "date", "snippet"},
			ScalarFields: []string{"id", "folder", "subject", "from", "from_name", "date", "snippet"},
		},
	})
}

// mailThreadDocument is the result of mail thread. Subject is the subject without
// prefixes that the messages share; Basis names how they were found.
type mailThreadDocument struct {
	Subject   string         `json:"subject"`
	Basis     string         `json:"basis"`
	Items     []mailListItem `json:"items"`
	Count     int            `json:"count"`
	Truncated bool           `json:"truncated"`
	HasMore   bool           `json:"has_more"`
	Available int            `json:"available,omitempty"`
}

// mailReplyPrefix matches the reply and forward prefixes that mail clients put before a
// subject, in several languages, with an optional count such as "Re[2]:", and a leading
// list tag such as "[team]".
var mailReplyPrefix = regexp.MustCompile(`(?i)^\s*(?:\[[^\]]{1,40}\]\s*)?(?:re|aw|fwd?|wg|sv|vs|antw|rv|tr|enc|res|ref|réf|odp|doorst)\s*(?:\[\d+\]|\(\d+\))?\s*:\s*`)

// threadSubject is subject without reply and forward prefixes, with white space collapsed.
func threadSubject(subject string) string {
	subject = strings.Join(strings.Fields(subject), " ")
	for {
		stripped := mailReplyPrefix.ReplaceAllString(subject, "")
		if stripped == subject {
			return strings.TrimSpace(subject)
		}
		subject = stripped
	}
}

func runMailThread(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.thread", args,
		intFlag("limit", 1, mailThreadMaxLimit, fmt.Sprintf("--limit must be an integer between 1 and %d", mailThreadMaxLimit)))
	llm := f.LLM()
	if ferr != nil {
		return exitWithError(stderr, llm, ferr)
	}
	id, folder := strings.TrimSpace(f.String("id")), strings.TrimSpace(f.String("folder"))
	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if folder == "" {
		return exitWithError(stderr, llm, invalidArgument("--folder is required"))
	}
	limit := mailThreadDefaultLimit
	if value, ok := f.Int("limit"); ok {
		limit = value
	}
	snippets := f.Bool("snippets")
	if snippets && limit > mailSnippetMaxItems {
		return exitWithError(stderr, llm, invalidArgument(fmt.Sprintf("--snippets reads each message, so it needs --limit %d or less", mailSnippetMaxItems)))
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	start, err := fetchMailRead(ctx, c, sess, mailReadOptions{id: id, folder: folder, bodyFormat: "text", maxBodyBytes: mailReadMinimumMaxBodyBytes})
	cancel()
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	subject := threadSubject(start.Subject)
	if subject == "" {
		return exitWithError(stderr, llm, invalidArgument(fmt.Sprintf("message %s in %s has no subject to find its conversation by; search for words of its body with mail search", id, folder)))
	}

	opts := mailListOptions{folder: privateFolderRoot, all: true, recursive: true, skipTrash: true, skipDrafts: true, pageSize: mailListDefaultPageSize,
		fields: []string{"to"}, search: true, llm: llm, searchFilter: mailSearchFilter([]string{subject}, []string{"subject"})}
	folders, err := mailScanFolders(c, sess, &opts)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	var items []mailListItem
	seen := map[string]bool{}
	for _, scanned := range folders {
		matches, _, _, err := collectFolderMail(c, sess, scanned, opts, mailListColumnsForFolder(opts, scanned))
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		for _, item := range matches {
			if strings.EqualFold(threadSubject(item.Subject), subject) {
				seen[item.sourceFolder+"\x00"+item.ID] = true
				items = append(items, item)
			}
		}
		if len(items) > mailListMaxAllItems {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("the conversation has more than %d messages", mailListMaxAllItems)})
		}
	}
	// The start message belongs to the conversation even in a folder that the search
	// leaves out, such as Trash.
	if !seen[folder+"\x00"+id] {
		items = append(items, threadItemFromRead(start))
	}
	sort.SliceStable(items, func(i, j int) bool { return mailListKeyOf(items[j]).before(mailListKeyOf(items[i])) })
	document := mailThreadDocument{Subject: subject, Basis: "subject", Items: items}
	if len(items) > limit {
		document.Available, document.Items = len(items), items[len(items)-limit:]
		document.HasMore, document.Truncated = true, true
	}
	document.Count = len(document.Items)
	if snippets {
		if err := addMailSnippets(c, sess, document.Items); err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
	}
	if llm {
		render.CompactJSON(stdout, document)
		return 0
	}
	rows := make([][]string, len(document.Items))
	for i, item := range document.Items {
		rows[i] = []string{item.Date, item.Folder, item.ID, item.From, addressesToString(item.To), item.Subject}
	}
	render.Table(stdout, []string{"date", "folder", "id", "from", "to", "subject"}, rows)
	if document.HasMore {
		fmt.Fprintf(stdout, "\n(the newest %d of %s messages; raise --limit to see older ones)\n", document.Count, strconv.Itoa(document.Available))
	}
	return 0
}

// threadItemFromRead is the list item of a message read with mail read.
func threadItemFromRead(message mailReadResult) mailListItem {
	item := mailListItem{ID: message.ID, Folder: message.Folder, sourceFolder: message.Folder, Subject: message.Subject, To: message.To, includeTo: true}
	if len(message.From) > 0 {
		item.From = message.From[0].Address
		if name := displayName(message.From[0].Name); name != item.From {
			item.FromName = name
		}
	}
	if message.ReceivedDate != nil {
		item.Date = *message.ReceivedDate
	} else if message.SentDate != nil {
		item.Date = *message.SentDate
	}
	return item
}
