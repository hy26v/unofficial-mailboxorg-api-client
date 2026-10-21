// ABOUTME: The "mail correspondents" command: distinct addresses in past mail that match a query.
// ABOUTME: Searches the address fields of every mail folder and counts messages per address; no bodies.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	mailCorrespondentsDefaultLimit = 20
	mailCorrespondentsMaxLimit     = 200
)

var mailCorrespondentsFieldOrder = []string{"from", "to", "cc"}

func init() {
	operations.Register(operations.Operation{
		Name:       "mail.correspondents",
		Resource:   "mail",
		Action:     "correspondents",
		Summary:    "Find the email addresses of a person or company in past mail: distinct matching addresses with name, message count and last date",
		Keywords:   []string{"address", "email address", "who", "person", "company", "correspondent", "contact", "recipient"},
		Method:     http.MethodPut,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"query":  {Type: "string", Required: true, Description: "words that must all appear in the address or display name (case-insensitive substrings), for example a name or domain"},
			"in":     {Type: "string", Required: false, Description: "comma-separated address fields to search: from,to,cc (default from,to,cc)"},
			"folder": {Type: "string", Required: false, Description: "folder id whose messages and mail subfolders are searched (default 1: every mail folder)"},
			"since":  {Type: "string", Required: false, Description: "inclusive received_date lower bound in RFC3339"},
			"until":  {Type: "string", Required: false, Description: "exclusive received_date upper bound in RFC3339"},
			"limit":  {Type: "int", Required: false, Description: "addresses to return, most messages first, 1-200 (default 20)"},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"address", "name", "count", "last_date", "roles"},
			ScalarFields: []string{"address", "name", "count", "last_date"},
		},
	})
}

// mailCorrespondent is one distinct address. Count is the number of matching messages
// that carry it; roles names the fields it appeared in.
type mailCorrespondent struct {
	Address  string   `json:"address"`
	Name     string   `json:"name,omitempty"`
	Count    int      `json:"count"`
	LastDate string   `json:"last_date"`
	Roles    []string `json:"roles"`
	nameDate string
	roles    map[string]bool
}

type mailCorrespondentsDocument struct {
	Items     []mailCorrespondent `json:"items"`
	Count     int                 `json:"count"`
	Total     int                 `json:"total"`
	Truncated bool                `json:"truncated"`
	HasMore   bool                `json:"has_more"`
}

func runMailCorrespondents(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.correspondents", args,
		intFlag("limit", 1, mailCorrespondentsMaxLimit, fmt.Sprintf("--limit must be an integer between 1 and %d", mailCorrespondentsMaxLimit)),
		checkedFlag("in", func(value string) *cliErrors.Error {
			_, err := parseMailCorrespondentFields(value)
			return err
		}),
		checkedFlag("since", mailListDateCheck("--since")),
		checkedFlag("until", mailListDateCheck("--until")),
	)
	llm := f.LLM()
	if ferr != nil {
		return exitWithError(stderr, llm, ferr)
	}
	opts := mailListOptions{folder: privateFolderRoot, all: true, recursive: true, pageSize: mailListDefaultPageSize,
		fields: []string{"to", "cc"}, search: true, llm: llm}
	if folder, ok := f.Lookup("folder"); ok {
		if opts.folder = strings.TrimSpace(folder); opts.folder == "" {
			return exitWithError(stderr, llm, invalidArgument("--folder must not be blank"))
		}
	}
	limit := mailCorrespondentsDefaultLimit
	if value, ok := f.Int("limit"); ok {
		limit = value
	}
	if since, ok := f.Lookup("since"); ok {
		parsed, _ := time.Parse(time.RFC3339, since)
		opts.since = &parsed
	}
	if until, ok := f.Lookup("until"); ok {
		parsed, _ := time.Parse(time.RFC3339, until)
		opts.until = &parsed
	}
	if opts.since != nil && opts.until != nil && !opts.since.Before(*opts.until) {
		return exitWithError(stderr, llm, invalidArgument("--since must be earlier than --until"))
	}
	query, querySet := f.Lookup("query")
	if !querySet {
		return exitWithError(stderr, llm, invalidArgument("--query is required, e.g. --query example.org"))
	}
	terms, err := parseMailSearchQuery(query)
	if err != nil {
		return exitWithError(stderr, llm, invalidArgument(err.Error()))
	}
	searchIn := mailCorrespondentsFieldOrder
	if in, ok := f.Lookup("in"); ok {
		searchIn, _ = parseMailCorrespondentFields(in)
	}
	opts.searchFilter = mailSearchFilter(terms, searchIn)

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}
	folders, err := recursiveMailScanFolders(c, sess, opts.folder)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	columns := mailListColumnsFor(opts)
	var messages []mailListItem
	for _, folder := range folders {
		items, _, _, err := collectFolderMail(c, sess, folder, opts, columns)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		messages = append(messages, items...)
		if len(messages) > mailListMaxAllItems {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("mail correspondents matched more than %d messages; narrow the query, folder or dates", mailListMaxAllItems)})
		}
	}

	all := collectMailCorrespondents(messages, terms, searchIn)
	document := mailCorrespondentsDocument{Items: all, Total: len(all)}
	if len(all) > limit {
		document.Items, document.Truncated, document.HasMore = all[:limit], true, true
	}
	document.Count = len(document.Items)
	if llm {
		render.CompactJSON(stdout, document)
		return 0
	}
	rows := make([][]string, len(document.Items))
	for i, item := range document.Items {
		rows[i] = []string{item.Address, item.Name, strconv.Itoa(item.Count), item.LastDate, strings.Join(item.Roles, ",")}
	}
	render.Table(stdout, []string{"address", "name", "count", "last_date", "roles"}, rows)
	writeTruncationNotice(stdout, document.Truncated, limit)
	return 0
}

func parseMailCorrespondentFields(value string) ([]string, *cliErrors.Error) {
	seen := map[string]bool{}
	var fields []string
	for _, raw := range strings.Split(value, ",") {
		field := strings.TrimSpace(raw)
		if field != "from" && field != "to" && field != "cc" {
			return nil, invalidArgument(fmt.Sprintf("--in contains unsupported field %q; allowed fields: %s", field, strings.Join(mailCorrespondentsFieldOrder, ", ")))
		}
		if !seen[field] {
			seen[field] = true
			fields = append(fields, field)
		}
	}
	return fields, nil
}

// collectMailCorrespondents counts, per lowercased address, the messages whose searched
// fields carry an address or display name that contains every term. The name is the most
// recent non-empty one. Most messages come first, then the most recent, then the address.
func collectMailCorrespondents(messages []mailListItem, terms, fields []string) []mailCorrespondent {
	lowered := make([]string, len(terms))
	for i, term := range terms {
		lowered[i] = strings.ToLower(term)
	}
	matches := func(address mailAddress) bool {
		text := strings.ToLower(address.Name + " " + address.Address)
		for _, term := range lowered {
			if !strings.Contains(text, term) {
				return false
			}
		}
		return true
	}
	byAddress := map[string]*mailCorrespondent{}
	for _, message := range messages {
		counted := map[string]bool{}
		for _, field := range fields {
			var addresses []mailAddress
			switch field {
			case "from":
				if message.From != "" {
					addresses = []mailAddress{{Name: message.fromName, Address: message.From}}
				}
			case "to":
				addresses = message.To
			case "cc":
				addresses = message.Cc
			}
			for _, address := range addresses {
				key := strings.ToLower(strings.TrimSpace(address.Address))
				if key == "" || !matches(address) {
					continue
				}
				entry := byAddress[key]
				if entry == nil {
					entry = &mailCorrespondent{Address: key, roles: map[string]bool{}}
					byAddress[key] = entry
				}
				entry.roles[field] = true
				if name := strings.TrimSpace(address.Name); name != "" && name != address.Address && message.Date >= entry.nameDate {
					entry.Name, entry.nameDate = name, message.Date
				}
				if !counted[key] {
					counted[key] = true
					entry.Count++
					if message.Date > entry.LastDate {
						entry.LastDate = message.Date
					}
				}
			}
		}
	}
	out := make([]mailCorrespondent, 0, len(byAddress))
	for _, entry := range byAddress {
		entry.Roles = []string{}
		for _, field := range mailCorrespondentsFieldOrder {
			if entry.roles[field] {
				entry.Roles = append(entry.Roles, field)
			}
		}
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].LastDate != out[j].LastDate {
			return out[i].LastDate > out[j].LastDate
		}
		return out[i].Address < out[j].Address
	})
	return out
}
