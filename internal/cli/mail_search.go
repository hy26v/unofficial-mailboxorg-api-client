// ABOUTME: Server-side mail search for "mail search": query parsing, the OX search filter, and bounded paging.
// ABOUTME: The provider may match body text, but results carry only the same safe metadata as mail list.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

const mailSearchMaxTerms = 10

// mailSearchFields maps the CLI's --in names to OX mail search-term field names. OX
// matches each as a case-insensitive substring; "content" is the message body text.
var mailSearchFields = map[string]string{
	"subject": "subject",
	"from":    "from",
	"to":      "to",
	"cc":      "cc",
	"bcc":     "bcc",
	"body":    "content",
}

var mailSearchFieldOrder = []string{"subject", "from", "to", "cc", "bcc", "body"}

var mailSearchDefaultFields = []string{"subject", "from", "to", "cc", "body"}

// mailSearchAddressFields is the --in shorthand for every address field.
const mailSearchAddressFields = "address"

func parseMailSearchFields(value string) ([]string, error) {
	seen := map[string]bool{}
	fields := []string{}
	for _, raw := range strings.Split(value, ",") {
		field := strings.TrimSpace(raw)
		expanded := []string{field}
		if field == mailSearchAddressFields {
			expanded = []string{"from", "to", "cc", "bcc"}
		} else if _, ok := mailSearchFields[field]; !ok {
			return nil, fmt.Errorf("--in contains unsupported field %q; allowed fields: %s, or %s for from,to,cc,bcc", field, strings.Join(mailSearchFieldOrder, ", "), mailSearchAddressFields)
		}
		for _, name := range expanded {
			if !seen[name] {
				seen[name] = true
				fields = append(fields, name)
			}
		}
	}
	return fields, nil
}

// parseMailSearchQuery splits a query into terms that must all match. Whitespace separates
// terms; a double-quoted span is one phrase term.
func parseMailSearchQuery(query string) ([]string, error) {
	terms := []string{}
	var current strings.Builder
	inQuote := false
	flush := func() {
		if term := strings.TrimSpace(current.String()); term != "" {
			terms = append(terms, term)
		}
		current.Reset()
	}
	for _, r := range query {
		switch {
		case r == '"':
			flush()
			inQuote = !inQuote
		case unicode.IsSpace(r) && !inQuote:
			flush()
		case unicode.IsControl(r):
			return nil, fmt.Errorf("--query must not contain control characters")
		default:
			current.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("--query has an unclosed double quote")
	}
	flush()
	if len(terms) == 0 {
		return nil, fmt.Errorf("--query must not be blank")
	}
	if len(terms) > mailSearchMaxTerms {
		return nil, fmt.Errorf("--query has %d terms; the maximum is %d", len(terms), mailSearchMaxTerms)
	}
	return terms, nil
}

// mailSearchFilter builds the OX search-term filter: every term must match at least one of
// the selected fields.
func mailSearchFilter(terms, fields []string) any {
	termFilters := make([]any, 0, len(terms))
	for _, term := range terms {
		alternatives := make([]any, 0, len(fields))
		for _, field := range fields {
			alternatives = append(alternatives, []any{"=", map[string]string{"field": mailSearchFields[field]}, term})
		}
		if len(alternatives) == 1 {
			termFilters = append(termFilters, alternatives[0])
		} else {
			termFilters = append(termFilters, append([]any{"or"}, alternatives...))
		}
	}
	if len(termFilters) == 1 {
		return termFilters[0]
	}
	return append([]any{"and"}, termFilters...)
}

// mailSearchPager returns a page source for one folder. Each provider request sends
// limit=0,<end>: OX documents the pair as left_hand_limit/right_hand_limit, so the start
// is inclusive and the end exclusive, and the request asks for rows 0..end-1. The request
// always starts at 0 so the returned rows have a known start whether or not the server
// honors limit (OX 7.10.6 documents no limit for search and may return every match).
// Pages are then cut locally from that match list, so offsets and cursors index the
// folder's matches, and has_more is true only when a row exists after the page.
func mailSearchPager(c *client.Client, sess client.Session, folder string, columns []mailListColumn, opts mailListOptions) func(offset, size int) (mailListPage, error) {
	var matches []mailListItem
	complete := false
	requested := 0
	return func(offset, size int) (mailListPage, error) {
		// One row after the page shows whether another page exists.
		needed := offset + size + 1
		if !complete && len(matches) < needed {
			end := needed
			// Grow repeated requests geometrically, so a scan over many pages does not
			// download the leading matches again for every page.
			if end < 2*requested {
				end = 2 * requested
			}
			if end > mailListMaxAllItems+1 {
				end = mailListMaxAllItems + 1
			}
			items, err := fetchMailSearchItems(c, sess, folder, columns, opts, end)
			if err != nil {
				return mailListPage{}, err
			}
			matches, requested = items, end
			// Fewer rows than requested means the match list ended. More rows than
			// requested means the server ignored limit and sent every match.
			complete = len(items) != end
		}
		if offset >= len(matches) {
			return mailListPage{Items: []mailListItem{}}, nil
		}
		pageEnd := offset + size
		if pageEnd >= len(matches) {
			return mailListPage{Items: matches[offset:]}, nil
		}
		return mailListPage{Items: matches[offset:pageEnd], HasMore: true}, nil
	}
}

// fetchMailSearchItems requests the folder's matches with index 0 up to, but not
// including, end, in received-date order.
func fetchMailSearchItems(c *client.Client, sess client.Session, folder string, columns []mailListColumn, opts mailListOptions, end int) ([]mailListItem, error) {
	columnIDs := make([]string, len(columns))
	for i := range columns {
		columnIDs[i] = columns[i].ID
	}
	params := url.Values{
		"folder":   {folder},
		"columns":  {strings.Join(columnIDs, ",")},
		"sort":     {"610"},
		"order":    {"desc"},
		"limit":    {"0," + strconv.Itoa(end)},
		"timezone": {mailDateTimezone},
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	data, err := c.DoJSON(ctx, http.MethodPut, "/mail", "search", sess, params, map[string]any{"filter": opts.searchFilter})
	if err != nil {
		return nil, err
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail search response"}
	}
	if len(rows) > mailListMaxAllItems {
		return nil, &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("mail search matched more than %d messages in %s; narrow the query", mailListMaxAllItems, folder)}
	}
	return decodeMailListRows(rows, folder, columns, opts)
}
