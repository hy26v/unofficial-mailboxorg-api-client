// ABOUTME: Mail metadata listing and server-side search with explicit fields, pagination, recursion, and filters.
// ABOUTME: Search may match body text on the provider, but neither command returns raw headers or bodies.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	mailListDefaultFolder   = "default0/INBOX"
	mailListDefaultLimit    = 20
	mailListMaxLimit        = 200
	mailListDefaultPageSize = 200
	mailListMaxAllItems     = 100000
	mailListDefaultMaxBytes = 1 << 20
	mailListMaximumMaxBytes = 16 << 20

	// Compatibility default: id, subject, from, and received_date. Additional safe
	// columns are requested only when --fields or a metadata filter needs them.
	mailListColumns = "600,607,603,610"

	// mailDateTimezone is sent as the timezone URL parameter on every mail request whose
	// response carries sent_date or received_date. Without it, OX adds the user's timezone
	// offset to these Time values; with it, they are plain UTC epoch milliseconds.
	mailDateTimezone = "UTC"
)

// mailListOnRequestFields are the output fields of mail list and mail search that --fields
// adds to the default id, subject, from and date.
var mailListDefaultFields = map[string]bool{"id": true, "subject": true, "from": true, "date": true}

var mailListOnRequestFields = []string{"folder", "to", "cc", "size", "sent_date", "received_date", "attachment", "seen", "flagged"}

// mailSearchOnRequestFields are mailListOnRequestFields without folder: mail search always
// returns the folder, which mail read needs with the id.
var mailSearchOnRequestFields = []string{"to", "cc", "size", "sent_date", "received_date", "attachment", "seen", "flagged"}

func init() {
	registerResource("mail", runMail)
	operations.Register(operations.Operation{
		Name:       "mail.list",
		Resource:   "mail",
		Action:     "list",
		Summary:    "List and filter safe message metadata with pagination",
		Keywords:   []string{"inbox", "latest", "recent", "newest", "unread", "messages", "emails"},
		Method:     http.MethodGet,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"folder":           {Type: "string", Required: false, Description: "folder id (default default0/INBOX); use 1 with --recursive for every mail folder"},
			"limit":            {Type: "int", Required: false, Description: "matching messages in one result page, 1-200 (default 20)"},
			"offset":           {Type: "int", Required: false, Description: "provider row offset for the first page"},
			"cursor":           {Type: "string", Required: false, Description: "opaque next_cursor from a prior result"},
			"all":              {Type: "bool", Required: false, Description: "fetch every page (use --format jsonl or --output for large results; --fit trims what does not fit)"},
			"page-size":        {Type: "int", Required: false, Description: "provider page size for --all or filtered scans, 1-200 (default 200)"},
			"fields":           {Type: "string", Required: false, Description: "comma-separated extra safe fields: folder,to,cc,size,sent_date,received_date,attachment,seen,flagged (id, subject, from and date are always returned)"},
			"recursive":        {Type: "bool", Required: false, Description: "scan the folder and all mail subfolders; implies --all, and with --limit returns the newest matches"},
			"skip-trash":       {Type: "bool", Required: false, Description: "with --recursive, leave out the Trash and Spam folders and their subfolders"},
			"from":             {Type: "string", Required: false, Description: "case-insensitive exact sender-address filter"},
			"from-domain":      {Type: "string", Required: false, Description: "sender domain filter, with or without leading @, for example example.org"},
			"to-domain":        {Type: "string", Required: false, Description: "recipient domain filter, with or without leading @"},
			"fit":              {Type: "bool", Required: false, Description: "when the result exceeds --max-output-bytes, return the newest messages that fit, with has_more, available and next_cursor, instead of failing"},
			"since":            {Type: "string", Required: false, Description: "inclusive received_date lower bound in RFC3339"},
			"until":            {Type: "string", Required: false, Description: "exclusive received_date upper bound in RFC3339"},
			"output":           {Type: "string", Required: false, Description: "write full JSON to an owner-only file"},
			"max-output-bytes": {Type: "int", Required: false, Description: "maximum JSON bytes written to stdout (default 1048576)"},
		},
		Output: operations.OutputSpec{
			Type:            "array",
			Fields:          []string{"id", "folder", "subject", "from", "to", "cc", "date", "sent_date", "received_date", "size", "attachment", "seen", "flagged"},
			ScalarFields:    []string{"id", "folder", "subject", "from", "date", "sent_date", "received_date", "size", "attachment", "seen", "flagged"},
			OnRequestFields: mailListOnRequestFields,
			OnRequestArg:    "fields",
		},
	})
	operations.Register(operations.Operation{
		Name:       "mail.search",
		Resource:   "mail",
		Action:     "search",
		Summary:    "Search messages by subject, address, or body text on the server; returns safe metadata",
		Keywords:   []string{"find", "look for", "query", "text", "subject"},
		Method:     http.MethodPut,
		Endpoint:   "/mail",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"query":            {Type: "string", Required: true, Description: "words that must all match (case-insensitive substrings); wrap a phrase in double quotes"},
			"in":               {Type: "string", Required: false, Description: "comma-separated fields to search: subject,from,to,cc,bcc,body, or address for from,to,cc,bcc (default subject,from,to,cc,body)"},
			"folder":           {Type: "string", Required: false, Description: "folder id (default default0/INBOX); use 1 with --recursive for every mail folder"},
			"limit":            {Type: "int", Required: false, Description: "matching messages in one result page, 1-200 (default 20)"},
			"offset":           {Type: "int", Required: false, Description: "offset into the folder's search matches for the first page"},
			"cursor":           {Type: "string", Required: false, Description: "opaque next_cursor from a prior result"},
			"all":              {Type: "bool", Required: false, Description: "return every match (use --format jsonl or --output for large results; --fit trims what does not fit)"},
			"page-size":        {Type: "int", Required: false, Description: "local page size for --all or filtered scans, 1-200 (default 200)"},
			"fields":           {Type: "string", Required: false, Description: "comma-separated extra safe fields: to,cc,size,sent_date,received_date,attachment,seen,flagged (id, folder, subject, from and date are always returned)"},
			"recursive":        {Type: "bool", Required: false, Description: "search the folder and all mail subfolders; implies --all, and with --limit returns the newest matches"},
			"skip-trash":       {Type: "bool", Required: false, Description: "with --recursive, leave out the Trash and Spam folders and their subfolders"},
			"from":             {Type: "string", Required: false, Description: "case-insensitive exact sender-address filter applied to matches"},
			"from-domain":      {Type: "string", Required: false, Description: "sender domain filter applied to matches, with or without leading @, for example example.org; --query can then be any word of the wanted mail"},
			"to-domain":        {Type: "string", Required: false, Description: "recipient domain filter applied to matches, with or without leading @"},
			"fit":              {Type: "bool", Required: false, Description: "when the result exceeds --max-output-bytes, return the newest matches that fit, with has_more, available and next_cursor, instead of failing"},
			"since":            {Type: "string", Required: false, Description: "inclusive received_date lower bound in RFC3339"},
			"until":            {Type: "string", Required: false, Description: "exclusive received_date upper bound in RFC3339"},
			"output":           {Type: "string", Required: false, Description: "write full JSON to an owner-only file"},
			"max-output-bytes": {Type: "int", Required: false, Description: "maximum JSON bytes written to stdout (default 1048576)"},
		},
		Output: operations.OutputSpec{
			Type:            "array",
			Fields:          []string{"id", "folder", "subject", "from", "to", "cc", "date", "sent_date", "received_date", "size", "attachment", "seen", "flagged"},
			ScalarFields:    []string{"id", "folder", "subject", "from", "date", "sent_date", "received_date", "size", "attachment", "seen", "flagged"},
			OnRequestFields: mailSearchOnRequestFields,
			OnRequestArg:    "fields",
		},
	})
}

func runMail(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		err := &cliErrors.Error{Code: "invalid_argument", Message: "mail requires an action, e.g. \"mail list\""}
		writeErr(stderr, false, err)
		return cliErrors.ExitCodeFor(err)
	}
	switch args[0] {
	case "list":
		return runMailList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "search":
		return runMailSearch(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runMailGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "read":
		return runMailRead(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "attachment":
		return runMailAttachment(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "move":
		return runMailMove(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "move-many":
		return runMailMoveMany(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "copy":
		return runMailCopy(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "flag":
		return runMailFlag(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runMailDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "send":
		return runMailSend(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "draft":
		return runMailDraft(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "senders":
		return runMailSenders(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "correspondents":
		return runMailCorrespondents(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		llm := hasFlag(args, "--llm")
		err := &cliErrors.Error{Code: "invalid_argument", Message: "unknown mail action: " + args[0]}
		writeErr(stderr, llm, err)
		return cliErrors.ExitCodeFor(err)
	}
}

type mailListItem struct {
	ID            string        `json:"id"`
	Folder        string        `json:"folder,omitempty"`
	Subject       string        `json:"subject"`
	From          string        `json:"from"`
	To            []mailAddress `json:"to,omitempty"`
	Cc            []mailAddress `json:"cc,omitempty"`
	Date          string        `json:"date"`
	SentDate      *string       `json:"sent_date,omitempty"`
	ReceivedDate  *string       `json:"received_date,omitempty"`
	Size          *int64        `json:"size,omitempty"`
	HasAttachment *bool         `json:"attachment,omitempty"`
	Seen          *bool         `json:"seen,omitempty"`
	Flagged       *bool         `json:"flagged,omitempty"`
	includeTo     bool
	includeCc     bool
	includeSent   bool
	// fromName is the sender's display name, which mail correspondents reports.
	fromName string
	// position is the folder offset of the item, the cursor that resumes a list at it.
	position int
}

// MarshalJSON distinguishes an optional field that was not requested from a requested
// field whose legitimate value is empty. A slice tagged omitempty otherwise drops [] and
// a *string tagged omitempty cannot represent both "not requested" and a requested null.
func (item mailListItem) MarshalJSON() ([]byte, error) {
	type mailListJSON struct {
		ID            string         `json:"id"`
		Folder        string         `json:"folder,omitempty"`
		Subject       string         `json:"subject"`
		From          string         `json:"from"`
		To            *[]mailAddress `json:"to,omitempty"`
		Cc            *[]mailAddress `json:"cc,omitempty"`
		Date          string         `json:"date"`
		SentDate      **string       `json:"sent_date,omitempty"`
		ReceivedDate  *string        `json:"received_date,omitempty"`
		Size          *int64         `json:"size,omitempty"`
		HasAttachment *bool          `json:"attachment,omitempty"`
		Seen          *bool          `json:"seen,omitempty"`
		Flagged       *bool          `json:"flagged,omitempty"`
	}
	var to, cc *[]mailAddress
	var sentDate **string
	if item.includeTo {
		to = &item.To
	}
	if item.includeCc {
		cc = &item.Cc
	}
	if item.includeSent {
		sentDate = &item.SentDate
	}
	return json.Marshal(mailListJSON{
		ID: item.ID, Folder: item.Folder, Subject: item.Subject, From: item.From,
		To: to, Cc: cc, Date: item.Date, SentDate: sentDate,
		ReceivedDate: item.ReceivedDate, Size: item.Size, HasAttachment: item.HasAttachment,
		Seen: item.Seen, Flagged: item.Flagged,
	})
}

type mailListColumn struct {
	Name string
	ID   string
}

var mailListExtraColumns = map[string]mailListColumn{
	"folder":        {Name: "folder", ID: "601"},
	"to":            {Name: "to", ID: "604"},
	"cc":            {Name: "cc", ID: "605"},
	"size":          {Name: "size", ID: "608"},
	"sent_date":     {Name: "sent_date", ID: "609"},
	"received_date": {Name: "received_date", ID: "610"},
	"attachment":    {Name: "attachment", ID: "602"},
	"seen":          {Name: "flags", ID: "611"},
	"flagged":       {Name: "flags", ID: "611"},
}

type mailListOptions struct {
	llm            bool
	folder         string
	limit          int
	limitSet       bool
	offset         int
	offsetSet      bool
	cursor         string
	all            bool
	pageSize       int
	fields         []string
	recursive      bool
	skipTrash      bool
	from           string
	fromSet        bool
	fromDomain     string
	toDomain       string
	since          *time.Time
	until          *time.Time
	output         string
	maxOutputBytes int
	fit            bool

	// Search mode only: the provider filter built from --query and --in.
	search       bool
	searchFilter any
}

func parseMailListOptionsFor(args []string, search bool) (mailListOptions, *cliErrors.Error) {
	opts := mailListOptions{folder: mailListDefaultFolder, limit: mailListDefaultLimit, pageSize: mailListDefaultPageSize, maxOutputBytes: mailListDefaultMaxBytes, search: search}
	operation := "mail.list"
	rules := []flagRule{
		intFlag("limit", 1, mailListMaxLimit, fmt.Sprintf("--limit must be an integer between 1 and %d", mailListMaxLimit)),
		intFlag("offset", 0, math.MaxInt, "--offset must be a non-negative integer"),
		intFlag("page-size", 1, mailListMaxLimit, fmt.Sprintf("--page-size must be an integer between 1 and %d", mailListMaxLimit)),
		checkedFlag("fields", func(value string) *cliErrors.Error {
			if _, err := parseMailListFields(value); err != nil {
				return invalidArgument(err.Error())
			}
			return nil
		}),
		checkedFlag("since", mailListDateCheck("--since")),
		checkedFlag("until", mailListDateCheck("--until")),
		intFlag("max-output-bytes", minimumStructuredOutputBytes, mailListMaximumMaxBytes, fmt.Sprintf("--max-output-bytes must be an integer between %d and %d", minimumStructuredOutputBytes, mailListMaximumMaxBytes)),
	}
	if search {
		operation = "mail.search"
		rules = append(rules, checkedFlag("in", func(value string) *cliErrors.Error {
			if _, err := parseMailSearchFields(value); err != nil {
				return invalidArgument(err.Error())
			}
			return nil
		}))
	}
	f, ferr := parseCommandFlags(operation, args, rules...)
	opts.llm = f.LLM()
	if ferr != nil {
		return opts, ferr
	}

	if folder, ok := f.Lookup("folder"); ok {
		opts.folder = strings.TrimSpace(folder)
	}
	if limit, ok := f.Int("limit"); ok {
		opts.limit, opts.limitSet = limit, true
	}
	if offset, ok := f.Int("offset"); ok {
		opts.offset, opts.offsetSet = offset, true
	}
	opts.cursor = strings.TrimSpace(f.String("cursor"))
	opts.all = f.Bool("all")
	if pageSize, ok := f.Int("page-size"); ok {
		opts.pageSize = pageSize
	}
	if fields, ok := f.Lookup("fields"); ok {
		opts.fields, _ = parseMailListFields(fields)
	}
	opts.recursive = f.Bool("recursive")
	opts.skipTrash = f.Bool("skip-trash")
	from, fromSet := f.Lookup("from")
	opts.from, opts.fromSet = strings.TrimSpace(from), fromSet
	opts.fromDomain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(f.String("from-domain")), "@"))
	opts.toDomain = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(f.String("to-domain")), "@"))
	if since, ok := f.Lookup("since"); ok {
		parsed, _ := time.Parse(time.RFC3339, since)
		opts.since = &parsed
	}
	if until, ok := f.Lookup("until"); ok {
		parsed, _ := time.Parse(time.RFC3339, until)
		opts.until = &parsed
	}
	opts.output = f.String("output")
	opts.fit = f.Bool("fit")
	if maxOutputBytes, ok := f.Int("max-output-bytes"); ok {
		opts.maxOutputBytes = maxOutputBytes
	}

	if opts.folder == "" {
		return opts, invalidArgument("--folder must not be blank")
	}
	// A recursive scan reads every page of every folder, so it implies --all; with --limit
	// it returns the newest matches. Without --recursive, --limit pages the folder itself,
	// so --all adds nothing.
	if opts.recursive {
		opts.all = true
	} else if opts.all && opts.limitSet {
		opts.all = false
	}
	if opts.recursive && (opts.offsetSet || opts.cursor != "") {
		return opts, invalidArgument("--recursive cannot be combined with --offset or --cursor")
	}
	if opts.offsetSet && opts.cursor != "" {
		return opts, invalidArgument("--offset and --cursor are mutually exclusive")
	}
	if opts.cursor != "" {
		offset, err := strconv.Atoi(strings.TrimPrefix(opts.cursor, "offset:"))
		if !strings.HasPrefix(opts.cursor, "offset:") || err != nil || offset < 0 {
			return opts, invalidArgument("--cursor must be an opaque value returned as next_cursor")
		}
		opts.offset = offset
	}
	if _, given := f.Lookup("to-domain"); given && opts.toDomain == "" {
		return opts, invalidArgument("--to-domain must not be blank")
	}
	if _, given := f.Lookup("from-domain"); given && opts.fromDomain == "" {
		return opts, invalidArgument("--from-domain must not be blank")
	}
	if opts.skipTrash && !opts.recursive {
		return opts, invalidArgument("--skip-trash needs --recursive")
	}
	if opts.fromSet && opts.from == "" {
		return opts, invalidArgument("--from must not be blank")
	}
	if opts.since != nil && opts.until != nil && !opts.since.Before(*opts.until) {
		return opts, invalidArgument("--since must be earlier than --until")
	}
	if opts.output != "" && strings.TrimSpace(opts.output) == "" {
		return opts, invalidArgument("--output must not be blank")
	}
	if search {
		query, querySet := f.Lookup("query")
		if !querySet {
			return opts, invalidArgument("--query is required")
		}
		terms, err := parseMailSearchQuery(query)
		if err != nil {
			return opts, invalidArgument(err.Error())
		}
		searchIn := mailSearchDefaultFields
		if in, ok := f.Lookup("in"); ok {
			searchIn, _ = parseMailSearchFields(in)
		}
		opts.searchFilter = mailSearchFilter(terms, searchIn)
		if opts.fromDomain != "" {
			// The provider matches the domain as a substring of the sender; the local
			// filter then keeps only exact sender domains.
			opts.searchFilter = []any{"and", opts.searchFilter, []any{"=", map[string]string{"field": "from"}, opts.fromDomain}}
		}
	}
	return opts, nil
}

// mailListDateCheck rejects a --since or --until value that is not an RFC 3339 time.
func mailListDateCheck(flag string) func(string) *cliErrors.Error {
	return func(value string) *cliErrors.Error {
		if _, err := time.Parse(time.RFC3339, value); err != nil {
			return invalidArgument(flag + " must be RFC3339: " + err.Error())
		}
		return nil
	}
}

func parseMailListFields(value string) ([]string, error) {
	seen := map[string]bool{}
	fields := []string{}
	allowed := make([]string, 0, len(mailListExtraColumns))
	for field := range mailListExtraColumns {
		allowed = append(allowed, field)
	}
	sort.Strings(allowed)
	for _, raw := range strings.Split(value, ",") {
		field := strings.TrimSpace(raw)
		if mailListDefaultFields[field] {
			continue // always returned
		}
		if _, ok := mailListExtraColumns[field]; !ok {
			return nil, fmt.Errorf("--fields contains unsupported field %q; allowed fields: %s", field, strings.Join(allowed, ", "))
		}
		if !seen[field] {
			seen[field] = true
			fields = append(fields, field)
		}
	}
	return fields, nil
}

func mailListColumnsFor(opts mailListOptions) []mailListColumn {
	columns := []mailListColumn{{Name: "id", ID: "600"}, {Name: "subject", ID: "607"}, {Name: "from", ID: "603"}, {Name: "date", ID: "610"}}
	wanted := append([]string(nil), opts.fields...)
	if opts.toDomain != "" {
		wanted = append(wanted, "to")
	}
	seenColumnIDs := map[string]bool{"600": true, "607": true, "603": true, "610": true}
	for _, field := range wanted {
		column := mailListExtraColumns[field]
		if !seenColumnIDs[column.ID] {
			columns = append(columns, column)
			seenColumnIDs[column.ID] = true
		}
	}
	return columns
}

func requestedMailListField(opts mailListOptions, field string) bool {
	for _, value := range opts.fields {
		if value == field {
			return true
		}
	}
	return false
}

type mailListPage struct {
	Items   []mailListItem
	HasMore bool
}

func fetchMailListPage(c *client.Client, sess client.Session, folder string, offset, pageSize int, columns []mailListColumn, opts mailListOptions) (mailListPage, error) {
	columnIDs := make([]string, len(columns))
	for i := range columns {
		columnIDs[i] = columns[i].ID
	}
	params := url.Values{
		"folder":           {folder},
		"columns":          {strings.Join(columnIDs, ",")},
		"sort":             {"610"},
		"order":            {"desc"},
		"left_hand_limit":  {strconv.Itoa(offset)},
		"right_hand_limit": {strconv.Itoa(offset + pageSize + 1)},
		"timezone":         {mailDateTimezone},
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	data, err := c.Do(ctx, http.MethodGet, "/mail", "all", sess, params)
	if err != nil {
		return mailListPage{}, err
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return mailListPage{}, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail list response"}
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	items, err := decodeMailListRows(rows, folder, columns, opts)
	if err != nil {
		return mailListPage{}, err
	}
	return mailListPage{Items: items, HasMore: hasMore}, nil
}

func decodeMailListRows(rows [][]json.RawMessage, folder string, columns []mailListColumn, opts mailListOptions) ([]mailListItem, error) {
	items := make([]mailListItem, 0, len(rows))
	for _, row := range rows {
		item, err := decodeMailListRowWithColumns(row, columns, opts)
		if err != nil {
			return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mail list row: " + err.Error()}
		}
		item.Folder = ""
		if opts.recursive || opts.search || requestedMailListField(opts, "folder") {
			item.Folder = folder
		}
		items = append(items, item)
	}
	return items, nil
}

func decodeMailListRowWithColumns(row []json.RawMessage, columns []mailListColumn, opts mailListOptions) (mailListItem, error) {
	if len(row) != len(columns) {
		return mailListItem{}, errUnexpectedMailColumns
	}
	item := mailListItem{
		includeTo:   requestedMailListField(opts, "to"),
		includeCc:   requestedMailListField(opts, "cc"),
		includeSent: requestedMailListField(opts, "sent_date"),
	}
	for i, column := range columns {
		raw := row[i]
		switch column.Name {
		case "id":
			if err := json.Unmarshal(raw, &item.ID); err != nil || strings.TrimSpace(item.ID) == "" {
				return mailListItem{}, errMissingMailID
			}
		case "subject":
			var subject *string
			if err := json.Unmarshal(raw, &subject); err != nil || subject == nil {
				return mailListItem{}, errMissingMailSubject
			}
			item.Subject = *subject
		case "from":
			addresses, err := decodeMailAddresses(raw)
			if err != nil {
				return mailListItem{}, err
			}
			if len(addresses) > 0 {
				item.From, item.fromName = addresses[0].Address, addresses[0].Name
			}
		case "to":
			addresses, err := decodeMailAddresses(raw)
			if err != nil {
				return mailListItem{}, err
			}
			if requestedMailListField(opts, "to") || opts.toDomain != "" {
				item.To = addresses
			}
		case "cc":
			addresses, err := decodeMailAddresses(raw)
			if err != nil {
				return mailListItem{}, err
			}
			item.Cc = addresses
		case "size":
			var size *int64
			if err := json.Unmarshal(raw, &size); err != nil || size == nil || *size < 0 {
				return mailListItem{}, errMissingMailSize
			}
			item.Size = size
		case "sent_date":
			date, err := decodeOptionalMailDate(raw)
			if err != nil {
				return mailListItem{}, err
			}
			item.SentDate = date
		case "date":
			date, err := decodeRequiredMailDate(raw)
			if err != nil {
				return mailListItem{}, err
			}
			item.Date = date
			if requestedMailListField(opts, "received_date") {
				item.ReceivedDate = stringPointer(date)
			}
		case "attachment":
			var value *bool
			if err := json.Unmarshal(raw, &value); err != nil || value == nil {
				return mailListItem{}, errMissingMailAttachmentFlag
			}
			item.HasAttachment = value
		case "flags":
			var flags *int64
			if err := json.Unmarshal(raw, &flags); err != nil || flags == nil || *flags < 0 {
				return mailListItem{}, errMissingMailFlags
			}
			if requestedMailListField(opts, "seen") {
				value := *flags&mailFlagSeen != 0
				item.Seen = &value
			}
			if requestedMailListField(opts, "flagged") {
				value := *flags&mailFlagFlagged != 0
				item.Flagged = &value
			}
		}
	}
	return item, nil
}

func decodeRequiredMailDate(raw json.RawMessage) (string, error) {
	var milliseconds *int64
	if err := json.Unmarshal(raw, &milliseconds); err != nil || milliseconds == nil {
		return "", errMissingMailDate
	}
	if *milliseconds <= 0 {
		return "", errInvalidMailDate
	}
	return time.UnixMilli(*milliseconds).UTC().Format(time.RFC3339), nil
}

func decodeOptionalMailDate(raw json.RawMessage) (*string, error) {
	var milliseconds *int64
	if err := json.Unmarshal(raw, &milliseconds); err != nil {
		return nil, err
	}
	if milliseconds == nil {
		return nil, nil
	}
	if *milliseconds <= 0 {
		return nil, errInvalidMailDate
	}
	value := time.UnixMilli(*milliseconds).UTC().Format(time.RFC3339)
	return &value, nil
}

func mailListMatches(item mailListItem, opts mailListOptions) bool {
	if opts.from != "" && !strings.EqualFold(item.From, opts.from) {
		return false
	}
	if opts.fromDomain != "" {
		at := strings.LastIndex(item.From, "@")
		if at < 0 || !strings.EqualFold(item.From[at+1:], opts.fromDomain) {
			return false
		}
	}
	if opts.toDomain != "" {
		matched := false
		for _, address := range item.To {
			at := strings.LastIndex(address.Address, "@")
			if at >= 0 && strings.EqualFold(address.Address[at+1:], opts.toDomain) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	received, err := time.Parse(time.RFC3339, item.Date)
	if err != nil {
		return false
	}
	if opts.since != nil && received.Before(*opts.since) {
		return false
	}
	if opts.until != nil && !received.Before(*opts.until) {
		return false
	}
	return true
}

func hasMailListFilters(opts mailListOptions) bool {
	return opts.from != "" || opts.fromDomain != "" || opts.toDomain != "" || opts.since != nil || opts.until != nil
}

func collectFolderMail(c *client.Client, sess client.Session, folder string, opts mailListOptions, columns []mailListColumn) ([]mailListItem, bool, int, error) {
	cursor := opts.offset
	items := []mailListItem{}
	hasFilters := hasMailListFilters(opts)
	pageSize := opts.pageSize
	if !opts.all && !hasFilters {
		pageSize = opts.limit
	}
	fetchPage := func(offset, size int) (mailListPage, error) {
		return fetchMailListPage(c, sess, folder, offset, size, columns, opts)
	}
	if opts.search {
		fetchPage = mailSearchPager(c, sess, folder, columns, opts)
	}
	for {
		page, err := fetchPage(cursor, pageSize)
		if err != nil {
			return nil, false, cursor, err
		}
		for index, item := range page.Items {
			if !mailListMatches(item, opts) {
				continue
			}
			if !requestedMailListField(opts, "to") {
				item.To = nil
			}
			if !opts.all && len(items) == opts.limit {
				return items, true, cursor + index, nil
			}
			item.position = cursor + index
			items = append(items, item)
			if len(items) > mailListMaxAllItems {
				return nil, false, cursor, &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("mail list exceeded %d matching messages; narrow the folder or date filters", mailListMaxAllItems)}
			}
		}
		cursor += len(page.Items)
		if !page.HasMore {
			return items, false, cursor, nil
		}
		if !opts.all && !hasFilters {
			return items, true, cursor, nil
		}
	}
}

func recursiveMailFolders(c *client.Client, sess client.Session, root string) ([]string, error) {
	folders := []string{root}
	seen := map[string]bool{root: true}
	for index := 0; index < len(folders); index++ {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		children, err := listFolders(ctx, c, sess, folders[index])
		cancel()
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if child.Module != "mail" || seen[child.ID] {
				continue
			}
			seen[child.ID] = true
			folders = append(folders, child.ID)
			if len(folders) > 10000 {
				return nil, &cliErrors.Error{Code: "invalid_response", Message: "recursive mail folder traversal exceeded 10000 folders"}
			}
		}
	}
	return folders, nil
}

// recursiveMailScanFolders returns the folders whose messages a recursive list or search
// reads. The private root holds folders, not messages, so when it is the start folder only
// its mail descendants are read.
func recursiveMailScanFolders(c *client.Client, sess client.Session, root string) ([]string, error) {
	folders, err := recursiveMailFolders(c, sess, root)
	if err != nil {
		return nil, err
	}
	if root == privateFolderRoot {
		folders = folders[1:]
	}
	return folders, nil
}

// withoutTrashAndSpam leaves the primary account's Trash and Spam folders, and their
// subfolders, out of a recursive scan. The account names them in trash_fullname and
// spam_fullname, or, as on mailbox.org, where those are empty, in trash and spam;
// without either, default0/Trash is left out.
func withoutTrashAndSpam(c *client.Client, sess client.Session, folders []string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	data, err := c.Do(ctx, http.MethodGet, "/account", "get", sess, url.Values{"id": {"0"}})
	if err != nil {
		return nil, err
	}
	var account struct {
		TrashFullname string `json:"trash_fullname"`
		SpamFullname  string `json:"spam_fullname"`
		Trash         string `json:"trash"`
		Spam          string `json:"spam"`
	}
	if err := json.Unmarshal(data, &account); err != nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode the mail account for --skip-trash"}
	}
	var skipped []string
	for _, names := range [][2]string{{account.TrashFullname, account.Trash}, {account.SpamFullname, account.Spam}} {
		name := strings.Trim(strings.TrimSpace(names[0]), "/")
		if name == "" {
			name = strings.Trim(strings.TrimSpace(names[1]), "/")
		}
		if name != "" {
			skipped = append(skipped, "default0/"+name)
		}
	}
	if len(skipped) == 0 || strings.TrimSpace(account.TrashFullname+account.Trash) == "" {
		skipped = append(skipped, mailTrashDefaultFolder)
	}
	kept := folders[:0:0]
	for _, folder := range folders {
		skip := false
		for _, root := range skipped {
			if strings.EqualFold(folder, root) || strings.HasPrefix(strings.ToLower(folder), strings.ToLower(root)+"/") {
				skip = true
				break
			}
		}
		if !skip {
			kept = append(kept, folder)
		}
	}
	return kept, nil
}

type mailListDocument struct {
	Items      []mailListItem `json:"items"`
	Truncated  bool           `json:"truncated"`
	Count      int            `json:"count"`
	Offset     int            `json:"offset"`
	Limit      int            `json:"limit,omitempty"`
	HasMore    bool           `json:"has_more"`
	NextOffset *int           `json:"next_offset,omitempty"`
	NextCursor string         `json:"next_cursor,omitempty"`
	// Available is set when --fit or the --limit of a recursive scan dropped items: the
	// number of items before it did.
	Available int `json:"available,omitempty"`
}

func runMailList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	return runMailListMode(stdout, stderr, args, httpClient, baseURL, sessionPath, getenv, false)
}

func runMailSearch(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	return runMailListMode(stdout, stderr, args, httpClient, baseURL, sessionPath, getenv, true)
}

func runMailListMode(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string, search bool) int {
	opts, optionErr := parseMailListOptionsFor(args, search)
	if optionErr != nil {
		writeErr(stderr, opts.llm, optionErr)
		return cliErrors.ExitCodeFor(optionErr)
	}
	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, opts.llm, authErr)
	}
	folders := []string{opts.folder}
	if opts.recursive {
		var err error
		folders, err = recursiveMailScanFolders(c, sess, opts.folder)
		if err == nil && opts.skipTrash {
			folders, err = withoutTrashAndSpam(c, sess, folders)
		}
		if err != nil {
			cerr := toCLIError(err, "api_error")
			writeErr(stderr, opts.llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
	}
	columns := mailListColumnsFor(opts)
	allItems := []mailListItem{}
	hasMore, nextOffset := false, opts.offset
	for _, folder := range folders {
		items, folderHasMore, folderNextOffset, err := collectFolderMail(c, sess, folder, opts, columns)
		if err != nil {
			cerr := toCLIError(err, "api_error")
			writeErr(stderr, opts.llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
		allItems = append(allItems, items...)
		if len(allItems) > mailListMaxAllItems {
			cerr := &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("recursive mail list exceeded %d matching messages; narrow the folder or date filters", mailListMaxAllItems)}
			writeErr(stderr, opts.llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
		if folderHasMore {
			hasMore, nextOffset = true, folderNextOffset
		}
	}
	available := 0
	if opts.recursive {
		sort.SliceStable(allItems, func(i, j int) bool { return allItems[i].Date > allItems[j].Date })
		if opts.limitSet && len(allItems) > opts.limit {
			available, allItems, hasMore = len(allItems), allItems[:opts.limit], true
		}
	}
	document := mailListDocument{Items: allItems, Truncated: hasMore, Count: len(allItems), Offset: opts.offset, HasMore: hasMore, Available: available}
	if !opts.all || opts.limitSet {
		document.Limit = opts.limit
	}
	if hasMore && !opts.recursive {
		document.NextOffset = &nextOffset
		document.NextCursor = "offset:" + strconv.Itoa(nextOffset)
	}
	return emitMailListDocument(stdout, stderr, opts, document)
}

// fitMailListDocument keeps the leading items of document that fit in maxBytes, with
// has_more, available and, unless the list is recursive, the cursor of the first dropped
// item. A recursive list merges folders, so no single cursor resumes it.
func fitMailListDocument(document mailListDocument, maxBytes int, recursive bool) (mailListDocument, []byte, error) {
	items := document.Items
	// prefix[k] is the encoded size of the first k items with the commas between them.
	prefix := make([]int, len(items)+1)
	for i, item := range items {
		data, err := json.Marshal(item)
		if err != nil {
			return document, nil, err
		}
		prefix[i+1] = prefix[i] + len(data) + 1
	}
	fitted := document
	fitted.Truncated, fitted.HasMore, fitted.Available = true, true, len(items)
	for keep := sort.Search(len(items)+1, func(k int) bool { return prefix[k] > maxBytes }) - 1; keep >= 0; keep-- {
		fitted.Items, fitted.Count = items[:keep], keep
		fitted.NextOffset, fitted.NextCursor = nil, ""
		if keep < len(items) && !recursive {
			next := items[keep].position
			fitted.NextOffset, fitted.NextCursor = &next, "offset:"+strconv.Itoa(next)
		}
		payload, err := json.Marshal(fitted)
		if err != nil {
			return document, nil, err
		}
		payload = append(payload, '\n')
		if len(payload) <= maxBytes || keep == 0 {
			return fitted, payload, nil
		}
	}
	return fitted, nil, nil
}

func emitMailListDocument(stdout, stderr io.Writer, opts mailListOptions, document mailListDocument) int {
	payload, err := json.Marshal(document)
	if err == nil && opts.fit && opts.output == "" && len(payload)+1 > opts.maxOutputBytes {
		document, payload, err = fitMailListDocument(document, opts.maxOutputBytes, opts.recursive)
		payload = bytes.TrimSuffix(payload, []byte("\n"))
	}
	if err != nil {
		cerr := &cliErrors.Error{Code: "invalid_response", Message: "could not encode mail list output"}
		writeErr(stderr, opts.llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	payload = append(payload, '\n')
	if opts.output != "" {
		if err := writePrivateFile(opts.output, payload); err != nil {
			cerr := &cliErrors.Error{Code: "invalid_argument", Message: "could not write --output file: " + err.Error()}
			writeErr(stderr, opts.llm, cerr)
			return cliErrors.ExitCodeFor(cerr)
		}
		if opts.llm {
			render.CompactJSON(stdout, struct {
				SavedTo      string `json:"saved_to"`
				BytesWritten int    `json:"bytes_written"`
				Count        int    `json:"count"`
			}{SavedTo: opts.output, BytesWritten: len(payload), Count: document.Count})
		} else {
			render.KeyValueTable(stdout, []render.Field{{Name: "saved_to", Value: opts.output}, {Name: "bytes_written", Value: strconv.Itoa(len(payload))}, {Name: "count", Value: strconv.Itoa(document.Count)}})
		}
		return 0
	}
	if len(payload) > opts.maxOutputBytes {
		cerr := &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("mail list output is %d bytes, exceeding --max-output-bytes=%d; use --output <file>", len(payload), opts.maxOutputBytes)}
		writeErr(stderr, opts.llm, cerr)
		return cliErrors.ExitCodeFor(cerr)
	}
	if opts.llm {
		_, _ = stdout.Write(payload)
		return 0
	}
	extraFields := opts.fields
	if opts.search && !requestedMailListField(opts, "folder") {
		extraFields = append([]string{"folder"}, extraFields...)
	}
	header := []string{"id", "subject", "from", "date"}
	header = append(header, extraFields...)
	rows := make([][]string, len(document.Items))
	for i, item := range document.Items {
		row := []string{item.ID, item.Subject, item.From, item.Date}
		for _, field := range extraFields {
			row = append(row, mailListHumanField(item, field))
		}
		rows[i] = row
	}
	render.Table(stdout, header, rows)
	writeTruncationNotice(stdout, document.Truncated, opts.limit)
	return 0
}

func mailListHumanField(item mailListItem, field string) string {
	switch field {
	case "folder":
		return item.Folder
	case "to":
		return addressesToString(item.To)
	case "cc":
		return addressesToString(item.Cc)
	case "size":
		if item.Size != nil {
			return strconv.FormatInt(*item.Size, 10)
		}
	case "sent_date":
		if item.SentDate != nil {
			return *item.SentDate
		}
	case "received_date":
		if item.ReceivedDate != nil {
			return *item.ReceivedDate
		}
	case "attachment":
		if item.HasAttachment != nil {
			return strconv.FormatBool(*item.HasAttachment)
		}
	case "seen":
		if item.Seen != nil {
			return strconv.FormatBool(*item.Seen)
		}
	case "flagged":
		if item.Flagged != nil {
			return strconv.FormatBool(*item.Flagged)
		}
	}
	return ""
}

var errUnexpectedMailColumns = &cliErrors.Error{Code: "invalid_response", Message: "unexpected number of mail columns in response"}
var errMissingMailID = &cliErrors.Error{Code: "invalid_response", Message: "mail row did not include an id"}
var errMissingMailDate = &cliErrors.Error{Code: "invalid_response", Message: "mail row did not include a received date"}
var errInvalidMailDate = &cliErrors.Error{Code: "invalid_response", Message: "mail row contained a non-positive date"}
