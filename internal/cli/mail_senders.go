// ABOUTME: The "mail senders" command: every address the account may send from, with the default.
// ABOUTME: Also resolves --from for mail send and mail draft against that list before a write.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	operations.Register(operations.Operation{
		Name:       "mail.senders",
		Resource:   "mail",
		Action:     "senders",
		Summary:    "List every address the account may send from (primary address, aliases, external accounts), with the default",
		Keywords:   []string{"sender", "from", "alias", "identity", "send as", "reply address", "addresses"},
		Method:     http.MethodGet,
		Endpoint:   "/account",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"query":    {Type: "string", Required: false, Description: "keep only senders whose address or name contains this text (case-insensitive), for example example.org"},
			"variants": {Type: "bool", Required: false, Description: "also list the secure.mailbox.org and tls.mailbox.org forms of mailbox.org addresses; by default they are left out when the plain address is listed, and hidden_variants counts them"},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"address", "name", "account_id", "account_name", "primary", "default", "mailbox_org_variant"},
			ScalarFields: []string{"address", "name", "account_id", "account_name", "primary", "default", "mailbox_org_variant"},
		},
	})
}

// Mail account columns of account?action=all: 1001=id, 1006=name, 1007=primary_address,
// 1037=personal, 1039=addresses (comma-separated, primary account only), 1005=transport_url,
// 1052=deactivated.
const mailSenderColumns = "1001,1006,1007,1037,1039,1005,1052"

// mailSender is one address the account may use as From.
type mailSender struct {
	Address     string `json:"address"`
	Name        string `json:"name"`
	AccountID   int    `json:"account_id"`
	AccountName string `json:"account_name"`
	Primary     bool   `json:"primary"`
	Default     bool   `json:"default"`
	// MailboxOrgVariant marks the "secure" and "tls" forms of a mailbox.org address
	// (user@secure.mailbox.org, user@tls.mailbox.org).
	MailboxOrgVariant string `json:"mailbox_org_variant,omitempty"`
}

// fetchMailSenders combines what the App Suite sender menu uses: the primary account's
// address list, external accounts that can send, the configured default and the user's
// display name for accounts without their own.
func fetchMailSenders(ctx context.Context, c *client.Client, sess client.Session) ([]mailSender, *cliErrors.Error) {
	data, err := c.Do(ctx, http.MethodGet, "/account", "all", sess, url.Values{"columns": {mailSenderColumns}})
	if err != nil {
		return nil, toCLIError(err, "api_error")
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode the account list for mail senders"}
	}

	displayName := ""
	if userData, err := c.Do(ctx, http.MethodGet, "/user", "get", sess, nil); err == nil {
		var user struct {
			DisplayName string `json:"display_name"`
		}
		if json.Unmarshal(userData, &user) == nil {
			displayName = strings.TrimSpace(user.DisplayName)
		}
	}
	defaultAddress := ""
	if tree, err := fetchJSLobTree(ctx, c, sess, "io.ox/mail"); err == nil {
		var value string
		if raw, ok := tree["defaultSendAddress"]; ok && json.Unmarshal(raw, &value) == nil {
			defaultAddress = strings.ToLower(strings.TrimSpace(value))
		}
	}

	var senders []mailSender
	seen := map[string]bool{}
	primaryOfAccountZero := ""
	for index, row := range rows {
		if len(row) != 7 {
			return nil, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("account row %d has %d columns, want seven", index, len(row))}
		}
		var id *int
		if json.Unmarshal(row[0], &id) != nil || id == nil {
			return nil, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("could not decode account row %d id", index)}
		}
		var accountName, primary, personal, addresses, transport string
		var deactivated bool
		_ = json.Unmarshal(row[1], &accountName)
		_ = json.Unmarshal(row[2], &primary)
		_ = json.Unmarshal(row[3], &personal)
		_ = json.Unmarshal(row[4], &addresses)
		_ = json.Unmarshal(row[5], &transport)
		_ = json.Unmarshal(row[6], &deactivated)
		// Only the primary account and external accounts with their own transport can send.
		if deactivated || (*id != 0 && strings.TrimSpace(transport) == "") {
			continue
		}
		primary = strings.ToLower(strings.TrimSpace(primary))
		if *id == 0 {
			primaryOfAccountZero = primary
		}
		name := strings.TrimSpace(personal)
		if name == "" {
			name = displayName
		}
		var list []string
		for _, address := range strings.Split(addresses, ",") {
			if address = strings.ToLower(strings.TrimSpace(address)); address != "" && address != primary {
				list = append(list, address)
			}
		}
		sort.Strings(list)
		if primary != "" {
			list = append([]string{primary}, list...)
		}
		for _, address := range list {
			if seen[address] {
				continue
			}
			seen[address] = true
			senders = append(senders, mailSender{
				Address:           address,
				Name:              name,
				AccountID:         *id,
				AccountName:       accountName,
				Primary:           address == primary,
				MailboxOrgVariant: mailboxOrgVariant(address),
			})
		}
	}
	if defaultAddress == "" {
		defaultAddress = primaryOfAccountZero
	}
	for i := range senders {
		senders[i].Default = senders[i].Address == defaultAddress
	}
	return senders, nil
}

func mailboxOrgVariant(address string) string {
	switch {
	case strings.HasSuffix(address, "@secure.mailbox.org"):
		return "secure"
	case strings.HasSuffix(address, "@tls.mailbox.org"):
		return "tls"
	}
	return ""
}

// parseFromValue checks --from before any request: one address, optionally with a
// display name ("Name <address>"), and no line breaks.
func parseFromValue(value string) (*mail.Address, error) {
	if strings.ContainsAny(value, "\r\n") {
		return nil, fmt.Errorf("--from contains a line break")
	}
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("--from must be one address such as user@example.org or \"Name <user@example.org>\"")
	}
	address.Address = strings.ToLower(address.Address)
	return address, nil
}

// resolveFrom accepts --from only when it is one of the account's sender addresses, and
// fills in that sender's display name when --from gives none.
func resolveFrom(ctx context.Context, c *client.Client, sess client.Session, from *mail.Address) (*mail.Address, *cliErrors.Error) {
	senders, cerr := fetchMailSenders(ctx, c, sess)
	if cerr != nil {
		return nil, cerr
	}
	return matchSender(senders, from)
}

// matchSender is resolveFrom with the sender list already fetched.
func matchSender(senders []mailSender, from *mail.Address) (*mail.Address, *cliErrors.Error) {
	for _, sender := range senders {
		if sender.Address == from.Address {
			resolved := *from
			if resolved.Name == "" {
				resolved.Name = sender.Name
			}
			return &resolved, nil
		}
	}
	return nil, invalidArgument(fmt.Sprintf("--from %s is not a sender address of this account; run \"mbx mail senders\" for the list", from.Address))
}

// mailboxOrgBaseAddress is the plain mailbox.org address of a secure or tls variant.
func mailboxOrgBaseAddress(address string) string {
	at := strings.LastIndex(address, "@")
	if at < 0 {
		return address
	}
	return address[:at] + "@mailbox.org"
}

// filterMailSenders keeps the senders that match query and, unless variants is set,
// leaves out each secure or tls variant whose plain address is also listed, except the
// default sender. It returns the kept senders and how many variants it left out.
func filterMailSenders(senders []mailSender, query string, variants bool) ([]mailSender, int) {
	listed := map[string]bool{}
	for _, sender := range senders {
		listed[sender.Address] = true
	}
	query = strings.ToLower(strings.TrimSpace(query))
	kept := []mailSender{}
	hidden := 0
	for _, sender := range senders {
		if query != "" && !strings.Contains(sender.Address, query) && !strings.Contains(strings.ToLower(sender.Name), query) {
			continue
		}
		if !variants && !sender.Default && sender.MailboxOrgVariant != "" && listed[mailboxOrgBaseAddress(sender.Address)] {
			hidden++
			continue
		}
		kept = append(kept, sender)
	}
	return kept, hidden
}

func runMailSenders(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mail.senders", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	query, querySet := f.Lookup("query")
	if querySet && strings.TrimSpace(query) == "" {
		return exitWithError(stderr, llm, invalidArgument("--query must not be blank"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}
	senders, cerr := fetchMailSenders(ctx, c, sess)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}
	senders, hidden := filterMailSenders(senders, query, f.Bool("variants"))

	if llm {
		render.CompactJSON(stdout, struct {
			Senders        []mailSender `json:"senders"`
			HiddenVariants int          `json:"hidden_variants,omitempty"`
		}{Senders: senders, HiddenVariants: hidden})
		return 0
	}
	rows := make([][]string, len(senders))
	for i, s := range senders {
		mark := ""
		if s.Default {
			mark = "default"
		}
		rows[i] = []string{s.Address, s.Name, strconv.Itoa(s.AccountID), mark, s.MailboxOrgVariant}
	}
	render.Table(stdout, []string{"address", "name", "account_id", "default", "variant"}, rows)
	if hidden > 0 {
		fmt.Fprintf(stdout, "\n(%d secure.mailbox.org and tls.mailbox.org forms left out; --variants lists them)\n", hidden)
	}
	return 0
}
