// ABOUTME: The "account" commands: list/get the primary and external mail accounts, and
// ABOUTME: create/update/delete external mail accounts via the OX middleware's /account module.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	registerResource("account", runAccount)
	operations.Register(operations.Operation{
		Name:       "account.list",
		Resource:   "account",
		Action:     "list",
		Summary:    "List mail accounts: the primary account (id 0) and external accounts; for sender addresses use mail.senders",
		Keywords:   []string{"external accounts", "imap"},
		Method:     http.MethodGet,
		Endpoint:   "/account",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args:       map[string]operations.ArgSpec{},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"id", "name", "primary_address", "login"},
			ScalarFields: []string{"id", "name", "primary_address", "login"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "account.get",
		Resource:   "account",
		Action:     "get",
		Summary:    "Get one mail account by id (0 is the primary account)",
		Method:     http.MethodGet,
		Endpoint:   "/account",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id": {Type: "int", Required: true, Description: "the account's numeric id"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "name", "primary_address", "login"},
			ScalarFields: []string{"id", "name", "primary_address", "login"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "account.create",
		Resource:   "account",
		Action:     "create",
		Summary:    "Add a new external mail account",
		Method:     http.MethodPut,
		Endpoint:   "/account",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"login":           {Type: "string", Required: true, Description: "login name for the external account"},
			"password":        {Type: "string", Required: false, Description: "password for the external account; visible in the process list and shell history, so prefer --password-stdin or --password-env (exactly one password source is required)"},
			"password-stdin":  {Type: "bool", Required: false, Description: "read the external account password from the first line of stdin"},
			"password-env":    {Type: "bool", Required: false, Description: "read the external account password from the MBX_ACCOUNT_PASSWORD environment variable"},
			"mail-url":        {Type: "string", Required: true, Description: "IMAP/POP3 URL, e.g. \"imap://host:993\""},
			"primary-address": {Type: "string", Required: true, Description: "the external account's own email address"},
			"name":            {Type: "string", Required: false, Description: "display name for the account"},
			"dry-run":         {Type: "bool", Required: false, Description: "validate and show what would be created without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "login", "mail_url", "primary_address", "name"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "account.update",
		Resource:   "account",
		Action:     "update",
		Summary:    "Update an external mail account",
		Method:     http.MethodPut,
		Endpoint:   "/account",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"id":              {Type: "int", Required: true, Description: "the account's numeric id"},
			"login":           {Type: "string", Required: false, Description: "new login name for the external account"},
			"password":        {Type: "string", Required: false, Description: "new password for the external account; visible in the process list and shell history, so prefer --password-stdin or --password-env"},
			"password-stdin":  {Type: "bool", Required: false, Description: "read the new external account password from the first line of stdin"},
			"password-env":    {Type: "bool", Required: false, Description: "read the new external account password from the MBX_ACCOUNT_PASSWORD environment variable"},
			"mail-url":        {Type: "string", Required: false, Description: "new IMAP/POP3 URL"},
			"primary-address": {Type: "string", Required: false, Description: "new email address for the external account"},
			"name":            {Type: "string", Required: false, Description: "new display name for the account"},
			"dry-run":         {Type: "bool", Required: false, Description: "validate and show what would change without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "account.delete",
		Resource:    "account",
		Action:      "delete",
		Summary:     "Delete an external mail account",
		Method:      http.MethodPut,
		Endpoint:    "/account",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "int", Required: true, Description: "the account's numeric id"},
			"confirm": {Type: "string", Required: true, Description: "must exactly match --id to confirm deletion"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be deleted without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"deleted", "id"},
			ScalarFields: []string{"deleted", "id"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
}

func runAccount(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("account requires an action, e.g. \"account list\""))
	}

	switch args[0] {
	case "list":
		return runAccountList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runAccountGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runAccountCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runAccountUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runAccountDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown account action: "+args[0]))
	}
}

type accountItem struct {
	ID             int    `json:"id"`
	Name           string `json:"name"`
	PrimaryAddress string `json:"primary_address"`
	Login          string `json:"login"`
}

// accountPasswordEnv names the environment variable read by --password-env. It is separate
// from MBX_PASSWORD, which is the mailbox.org sign-in password.
const accountPasswordEnv = "MBX_ACCOUNT_PASSWORD"

// maxAccountPasswordBytes bounds the line read by --password-stdin.
const maxAccountPasswordBytes = 4096

// accountPasswordStdin is the reader used by --password-stdin. The Handler signature has
// no injected stdin, so this is the process stdin; tests replace it.
var accountPasswordStdin io.Reader = os.Stdin

// accountPasswordSources records which password flags an account command received.
type accountPasswordSources struct {
	flagValue string
	flagSet   bool
	stdin     bool
	env       bool
}

// accountPasswordSourcesFrom reads the password flags of account create or update.
func accountPasswordSourcesFrom(f commandFlags) accountPasswordSources {
	value, set := f.Lookup("password")
	return accountPasswordSources{
		flagValue: value,
		flagSet:   set,
		stdin:     f.Bool("password-stdin"),
		env:       f.Bool("password-env"),
	}
}

// resolve returns the password from the one requested source. set is false when no
// source was requested. Two sources at once are rejected, because one would silently win.
func (p accountPasswordSources) resolve(getenv func(string) string) (password string, set bool, cerr *cliErrors.Error) {
	count := 0
	for _, requested := range []bool{p.flagSet, p.stdin, p.env} {
		if requested {
			count++
		}
	}
	switch {
	case count == 0:
		return "", false, nil
	case count > 1:
		return "", false, &cliErrors.Error{Code: "invalid_argument", Message: "use only one of --password, --password-stdin, --password-env"}
	case p.flagSet:
		return p.flagValue, true, nil
	case p.env:
		value := getenv(accountPasswordEnv)
		if value == "" {
			return "", false, &cliErrors.Error{Code: "invalid_argument", Message: "--password-env requires a non-empty " + accountPasswordEnv + " environment variable"}
		}
		return value, true, nil
	default:
		value, err := readPasswordLine(accountPasswordStdin)
		if err != nil {
			return "", false, &cliErrors.Error{Code: "invalid_argument", Message: "--password-stdin: " + err.Error()}
		}
		return value, true, nil
	}
}

// readPasswordLine reads the first line of r without its line ending.
func readPasswordLine(r io.Reader) (string, error) {
	if r == nil {
		return "", fmt.Errorf("stdin is not available")
	}
	reader := bufio.NewReader(io.LimitReader(r, maxAccountPasswordBytes+2))
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("could not read stdin")
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if len(line) > maxAccountPasswordBytes {
		return "", fmt.Errorf("the password line is longer than %d bytes", maxAccountPasswordBytes)
	}
	if line == "" {
		return "", fmt.Errorf("stdin did not contain a password")
	}
	return line, nil
}

// warnPasswordOnCommandLine tells the caller that --password exposed the secret.
func warnPasswordOnCommandLine(stderr io.Writer, llm bool) {
	writeWarning(stderr, llm, "password_on_command_line",
		"--password makes the external account password visible in the process list and shell history; use --password-stdin or --password-env ("+accountPasswordEnv+")")
}

func runAccountList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("account.list", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	// account?action=all requires an explicit "columns" param and returns column-tuples
	// (an array of arrays, positioned per the requested columns), not an array of objects:
	// 1001=id, 1006=name, 1007=primary_address, 1002=login.
	params := url.Values{"columns": {"1001,1006,1007,1002"}}
	data, err := c.Do(ctx, http.MethodGet, "/account", "all", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var accountRows [][]json.RawMessage
	if err := json.Unmarshal(data, &accountRows); err != nil || accountRows == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode account list response"})
	}

	accounts := make([]accountItem, 0, len(accountRows))
	for rowIndex, row := range accountRows {
		if len(row) != 4 {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("account list row %d has %d columns, want four", rowIndex, len(row))})
		}
		var item accountItem
		var id *int
		if err := json.Unmarshal(row[0], &id); err != nil || id == nil || *id < 0 {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("could not decode account list row %d id", rowIndex)})
		}
		item.ID = *id
		var name *string
		if err := json.Unmarshal(row[1], &name); err != nil || name == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("could not decode account list row %d name", rowIndex)})
		}
		item.Name = *name
		var primaryAddress *string
		if err := json.Unmarshal(row[2], &primaryAddress); err != nil || primaryAddress == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("could not decode account list row %d primary address", rowIndex)})
		}
		item.PrimaryAddress = *primaryAddress
		if err := json.Unmarshal(row[3], &item.Login); err != nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("could not decode account list row %d login", rowIndex)})
		}
		accounts = append(accounts, item)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Accounts []accountItem `json:"accounts"`
		}{Accounts: accounts})
		return 0
	}

	rows := make([][]string, len(accounts))
	for i, a := range accounts {
		rows[i] = []string{strconv.Itoa(a.ID), a.Name, a.PrimaryAddress}
	}
	render.Table(stdout, []string{"id", "name", "primary_address"}, rows)
	return 0
}

func runAccountGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("account.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	idStr := f.String("id")

	if idStr == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 0"))
	}
	id, convErr := strconv.Atoi(idStr)
	if convErr != nil || id < 0 {
		return exitWithError(stderr, llm, invalidArgument("--id must be a non-negative integer, got: "+idStr))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	params := url.Values{}
	params.Set("id", strconv.Itoa(id))
	data, err := c.Do(ctx, http.MethodGet, "/account", "get", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var raw struct {
		ID             *int    `json:"id"`
		Name           *string `json:"name"`
		PrimaryAddress *string `json:"primary_address"`
		Login          string  `json:"login"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw.ID == nil || *raw.ID != id || raw.Name == nil || raw.PrimaryAddress == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode account response"})
	}
	account := accountItem{ID: *raw.ID, Name: *raw.Name, PrimaryAddress: *raw.PrimaryAddress, Login: raw.Login}

	if llm {
		render.CompactJSON(stdout, account)
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: strconv.Itoa(account.ID)},
		{Name: "name", Value: account.Name},
		{Name: "primary_address", Value: account.PrimaryAddress},
		{Name: "login", Value: account.Login},
	})
	return 0
}

func runAccountCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("account.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	login, mailURL, primaryAddress, name := f.String("login"), f.String("mail-url"), f.String("primary-address"), f.String("name")
	passwordSources := accountPasswordSourcesFrom(f)

	if login == "" {
		return exitWithError(stderr, llm, invalidArgument("--login is required"))
	}
	if !passwordSources.flagSet && !passwordSources.stdin && !passwordSources.env {
		return exitWithError(stderr, llm, invalidArgument("a password is required: use --password-stdin, --password-env (reads "+accountPasswordEnv+"), or --password"))
	}
	if mailURL == "" {
		return exitWithError(stderr, llm, invalidArgument("--mail-url is required"))
	}
	if primaryAddress == "" {
		return exitWithError(stderr, llm, invalidArgument("--primary-address is required"))
	}
	password, _, passwordErr := passwordSources.resolve(getenv)
	if passwordErr != nil {
		return exitWithError(stderr, llm, passwordErr)
	}
	if password == "" {
		return exitWithError(stderr, llm, invalidArgument("the external account password must not be empty"))
	}
	if passwordSources.flagSet {
		warnPasswordOnCommandLine(stderr, llm)
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun         bool   `json:"dry_run"`
				Login          string `json:"login"`
				MailURL        string `json:"mail_url"`
				PrimaryAddress string `json:"primary_address"`
				Name           string `json:"name,omitempty"`
			}{DryRun: true, Login: login, MailURL: mailURL, PrimaryAddress: primaryAddress, Name: name})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "login", Value: login},
			{Name: "mail_url", Value: mailURL},
			{Name: "primary_address", Value: primaryAddress},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	body := map[string]any{
		"login":           login,
		"password":        password,
		"mail_url":        mailURL,
		"primary_address": primaryAddress,
	}
	if name != "" {
		body["name"] = name
	}

	data, err := c.DoJSON(ctx, http.MethodPut, "/account", "new", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var created struct {
		ID *int `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == nil || *created.ID <= 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode account create response"})
	}
	id := *created.ID

	if llm {
		render.CompactJSON(stdout, struct {
			ID int `json:"id"`
		}{ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: strconv.Itoa(id)}})
	return 0
}

func runAccountUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("account.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	idStr := f.String("id")
	login, loginSet := f.Lookup("login")
	mailURL, mailURLSet := f.Lookup("mail-url")
	primaryAddress, primaryAddressSet := f.Lookup("primary-address")
	name, nameSet := f.Lookup("name")
	passwordSources := accountPasswordSourcesFrom(f)

	if idStr == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	id, convErr := strconv.Atoi(idStr)
	if convErr != nil || id < 0 {
		return exitWithError(stderr, llm, invalidArgument("--id must be a non-negative integer, got: "+idStr))
	}
	password, passwordSet, passwordErr := passwordSources.resolve(getenv)
	if passwordErr != nil {
		return exitWithError(stderr, llm, passwordErr)
	}
	if passwordSet && password == "" {
		return exitWithError(stderr, llm, invalidArgument("the external account password must not be empty"))
	}
	if !loginSet && !passwordSet && !mailURLSet && !primaryAddressSet && !nameSet {
		return exitWithError(stderr, llm, invalidArgument("at least one of --login, --password (or --password-stdin, --password-env), --mail-url, --primary-address, --name is required"))
	}
	if passwordSources.flagSet {
		warnPasswordOnCommandLine(stderr, llm)
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool `json:"dry_run"`
				ID     int  `json:"id"`
			}{DryRun: true, ID: id})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "id", Value: strconv.Itoa(id)},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	body := map[string]any{"id": id}
	if loginSet {
		body["login"] = login
	}
	if passwordSet {
		body["password"] = password
	}
	if mailURLSet {
		body["mail_url"] = mailURL
	}
	if primaryAddressSet {
		body["primary_address"] = primaryAddress
	}
	if nameSet {
		body["name"] = name
	}

	updateParams := url.Values{}
	updateParams.Set("id", strconv.Itoa(id))
	if _, err := c.DoJSON(ctx, http.MethodPut, "/account", "update", sess, updateParams, body); err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID int `json:"id"`
		}{ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: strconv.Itoa(id)}})
	return 0
}

func runAccountDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("account.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	idStr, confirm := f.String("id"), f.String("confirm")

	if idStr == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	id, convErr := strconv.Atoi(idStr)
	if convErr != nil || id < 0 {
		return exitWithError(stderr, llm, invalidArgument("--id must be a non-negative integer, got: "+idStr))
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("--confirm is required and must exactly match --id ("+idStr+") to delete an account"))
	}
	if confirm != idStr {
		return exitWithError(stderr, llm, invalidArgument("--confirm ("+confirm+") does not match --id ("+idStr+"); refusing to delete"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool `json:"dry_run"`
				ID     int  `json:"id"`
			}{DryRun: true, ID: id})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "id", Value: strconv.Itoa(id)},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	// The account module's delete contract takes an array of numeric account IDs,
	// even when deleting a single account.
	data, err := c.DoJSON(ctx, http.MethodPut, "/account", "delete", sess, nil, []int{id})
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	if resultErr := accountDeleteResultError(data, id); resultErr != nil {
		return exitWithError(stderr, llm, resultErr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Deleted bool `json:"deleted"`
			ID      int  `json:"id"`
		}{Deleted: true, ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "deleted", Value: "true"},
		{Name: "id", Value: strconv.Itoa(id)},
	})
	return 0
}

// accountDeleteResultError validates account?action=delete's account-specific result.
// Unlike timestamped groupware deletes, its array names the account IDs that were
// deleted; applying the shared "declined IDs" decoder inverts success and failure.
func accountDeleteResultError(data []byte, requestedID int) *cliErrors.Error {
	var deleted []json.Number
	if err := json.Unmarshal(data, &deleted); err == nil && len(deleted) == 1 {
		if id, convErr := deleted[0].Int64(); convErr == nil && id == int64(requestedID) {
			return nil
		}
	}
	return &cliErrors.Error{
		Code:    "invalid_response",
		Message: "account delete response did not confirm deletion of account " + strconv.Itoa(requestedID),
	}
}
