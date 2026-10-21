// ABOUTME: The "share" commands: create and delete OX infostore share links via the
// ABOUTME: /share/management module; delete first checks for a link without creating one.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

// shareDefaultModule is the only OX module confirmed to work with the live deployment's
// share/management endpoint in live tests; other module values
// (e.g. "calendar") are not implemented because they were never verified.
const shareDefaultModule = "infostore"

func init() {
	registerResource("share", runShare)
	operations.Register(operations.Operation{
		Name:       "share.create",
		Resource:   "share",
		Action:     "create",
		Summary:    "Create (or fetch the existing) public share link for an infostore file",
		Method:     http.MethodPut,
		Endpoint:   "/share/management",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"folder":  {Type: "string", Required: true, Description: "the infostore folder id containing the file"},
			"id":      {Type: "string", Required: true, Description: "the file's id within that folder, either bare (\"2\") or composite as listed by files list (\"36/2\")"},
			"module":  {Type: "string", Required: false, Description: "OX module of the shared item (default \"infostore\"; only infostore is verified)"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be created without sending the request"},
			"confirm": {Type: "string", Required: false, Description: "ignored; accepted only so that older command lines keep working"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"url", "entity", "is_new"},
			ScalarFields: []string{"url", "entity", "is_new"},
			DryRunFields: []string{"dry_run", "folder", "item"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "share.delete",
		Resource:    "share",
		Action:      "delete",
		Summary:     "Delete an infostore file's public share link (reports not_found, and creates nothing, when the file has no link)",
		Method:      http.MethodPut,
		Endpoint:    "/share/management",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"folder":  {Type: "string", Required: true, Description: "the infostore folder id containing the file"},
			"id":      {Type: "string", Required: true, Description: "the file's id within that folder, either bare (\"2\") or composite as listed by files list (\"36/2\")"},
			"module":  {Type: "string", Required: false, Description: "OX module of the shared item (default \"infostore\"; only infostore is verified)"},
			"confirm": {Type: "string", Required: true, Description: "must exactly match --id to confirm deletion"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be deleted without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"deleted", "folder", "id"},
			ScalarFields: []string{"deleted", "folder", "id"},
			DryRunFields: []string{"dry_run", "folder", "item"},
		},
	})
}

func runShare(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("share requires an action, e.g. \"share create\""))
	}

	switch args[0] {
	case "create":
		return runShareCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runShareDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown share action: "+args[0]))
	}
}

type shareLink struct {
	URL    string `json:"url"`
	Entity int    `json:"entity"`
	IsNew  bool   `json:"is_new"`
}

func decodeShareLink(data []byte) (shareLink, bool) {
	var raw struct {
		URL    string `json:"url"`
		Entity *int   `json:"entity"`
		IsNew  *bool  `json:"is_new"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || !validShareLinkURL(raw.URL) ||
		raw.Entity == nil || *raw.Entity <= 0 || raw.IsNew == nil {
		return shareLink{}, false
	}
	return shareLink{URL: raw.URL, Entity: *raw.Entity, IsNew: *raw.IsNew}, true
}

func validShareLinkURL(value string) bool {
	if strings.TrimSpace(value) != value {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// shareTarget is the file that share create or share delete works on.
type shareTarget struct {
	folder string
	id     string // --id as given, bare or composite
	item   string // the composite infostore item id
	module string
}

// shareTargetFromFlags checks --folder, --id and --module in the order that share
// create and share delete have always used.
func shareTargetFromFlags(f commandFlags) (shareTarget, *cliErrors.Error) {
	target := shareTarget{folder: f.String("folder"), id: f.String("id"), module: shareDefaultModule}
	if value, given := f.Lookup("module"); given {
		target.module = value
	}
	if target.folder == "" {
		return target, invalidArgument("--folder is required")
	}
	if target.id == "" {
		return target, invalidArgument("--id is required")
	}
	item, itemErr := shareItemID(target.folder, target.id)
	if itemErr != nil {
		return target, itemErr
	}
	target.item = item
	if target.module != shareDefaultModule {
		return target, invalidArgument("unsupported --module: " + target.module + " (only \"" + shareDefaultModule + "\" is supported)")
	}
	return target, nil
}

// renderShareDryRun writes the dry-run result of share create and share delete.
func renderShareDryRun(stdout io.Writer, llm bool, target shareTarget) {
	if llm {
		render.CompactJSON(stdout, struct {
			DryRun bool   `json:"dry_run"`
			Folder string `json:"folder"`
			Item   string `json:"item"`
		}{DryRun: true, Folder: target.folder, Item: target.item})
		return
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "dry_run", Value: "true"},
		{Name: "folder", Value: target.folder},
		{Name: "item", Value: target.item},
	})
}

// shareItemID returns the composite infostore item id ("<folder>/<id>") for a --id that
// is either bare ("2") or already composite ("36/2", as files list prints it). A composite
// id must belong to --folder; joining it again would address "36/36/2".
func shareItemID(folder, id string) (string, *cliErrors.Error) {
	slash := strings.LastIndex(id, "/")
	if slash < 0 {
		return folder + "/" + id, nil
	}
	prefix, bare := id[:slash], id[slash+1:]
	if bare == "" || prefix != folder {
		return "", &cliErrors.Error{
			Code:    "invalid_argument",
			Message: "--id " + id + " is a composite file id that does not belong to --folder " + folder + "; pass the bare id or the composite id from that folder",
		}
	}
	return id, nil
}

func runShareCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("share.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	target, targetErr := shareTargetFromFlags(f)
	if targetErr != nil {
		return exitWithError(stderr, llm, targetErr)
	}

	if f.Bool("dry-run") {
		renderShareDryRun(stdout, llm, target)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	body := map[string]any{"module": target.module, "folder": target.folder, "item": target.item}
	data, err := c.DoJSON(ctx, http.MethodPut, "/share/management", "getLink", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	link, valid := decodeShareLink(data)
	if !valid {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode share link response"})
	}

	if llm {
		render.CompactJSON(stdout, link)
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "url", Value: link.URL},
		{Name: "entity", Value: strconv.Itoa(link.Entity)},
		{Name: "is_new", Value: strconv.FormatBool(link.IsNew)},
	})
	return 0
}

func runShareDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("share.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	target, targetErr := shareTargetFromFlags(f)
	if targetErr != nil {
		return exitWithError(stderr, llm, targetErr)
	}
	folder, id, item, module := target.folder, target.id, target.item, target.module
	confirm := f.String("confirm")
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("--confirm is required and must exactly match --id ("+id+") to delete a share link"))
	}
	if confirm != id {
		return exitWithError(stderr, llm, invalidArgument("--confirm ("+confirm+") does not match --id ("+id+"); refusing to delete"))
	}

	if f.Bool("dry-run") {
		renderShareDryRun(stdout, llm, target)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	// getLink creates a link when none exists, so first check, without creating anything,
	// whether the file has a public (anonymous) link at all.
	hasLink, err := infostoreItemHasPublicLink(ctx, c, sess, folder, item)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	if !hasLink {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "file " + item + " in folder " + folder + " has no public share link; nothing was deleted and no link was created"})
	}

	body := map[string]any{"module": module, "folder": folder, "item": item}

	// No read-only action returns the link's optimistic-locking timestamp that deleteLink
	// requires. getLink returns the existing link (is_new:false) with that timestamp in its
	// envelope. The check above makes it very unlikely that getLink creates a link here.
	linkData, timestamp, err := c.DoJSONWithTimestamp(ctx, http.MethodPut, "/share/management", "getLink", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	link, valid := decodeShareLink(linkData)
	if !valid {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode share link response; getLink can create a link, so check whether file " + item + " now has a public link and remove it with \"mbx share delete\" if needed"})
	}

	deleteParams := url.Values{}
	deleteParams.Set("timestamp", strconv.FormatInt(timestamp, 10))
	if _, err := c.DoJSON(ctx, http.MethodPut, "/share/management", "deleteLink", sess, deleteParams, body); err != nil {
		cerr := toCLIError(err, "api_error")
		if link.IsNew {
			cerr = shareLinkLeftBehindError(cerr, link.URL, folder, item, id)
		}
		return exitWithError(stderr, llm, cerr)
	}
	if link.IsNew {
		// The link seen by the check was removed before getLink ran (for example by another
		// client), so getLink created a new one. That new link is now deleted again, but the
		// link the caller asked to delete was already gone.
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "the public share link of file " + item + " was removed by another client before this delete; getLink created a new link and mbx deleted that new link again; no public link remains"})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Deleted bool   `json:"deleted"`
			Folder  string `json:"folder"`
			ID      string `json:"id"`
		}{Deleted: true, Folder: folder, ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "deleted", Value: "true"},
		{Name: "folder", Value: folder},
		{Name: "id", Value: id},
	})
	return 0
}

// shareLinkLeftBehindError reports a failed cleanup of a link that getLink created. The
// link is public until someone removes it, so the message names it and the command that
// removes it.
func shareLinkLeftBehindError(cause *cliErrors.Error, linkURL, folder, item, id string) *cliErrors.Error {
	return &cliErrors.Error{
		Code:       "share_link_left_behind",
		HTTPStatus: cause.HTTPStatus,
		Retryable:  cause.Retryable,
		RequestID:  cause.RequestID,
		Message: "a NEW public share link was created and could not be removed; it must be removed: url=" + linkURL +
			" folder=" + folder + " item=" + item + "; run \"mbx share delete --folder " + folder + " --id " + id + " --confirm " + id +
			"\"; cause: " + cause.Message + " (" + cause.Code + ")",
	}
}

// shareExtendedPermissionsColumn is infostore column 7010,
// com.openexchange.share.extendedObjectPermissions. Its entries describe each permission
// on an item; a public share link is an entry with type "anonymous".
const shareExtendedPermissionsColumn = "7010"

// infostoreItemHasPublicLink reads one item's extended object permissions with
// PUT /infostore?action=list, which never creates a share link.
func infostoreItemHasPublicLink(ctx context.Context, c *client.Client, sess client.Session, folder, item string) (bool, error) {
	params := url.Values{"columns": {"1,20," + shareExtendedPermissionsColumn}}
	data, err := c.DoJSON(ctx, http.MethodPut, "/infostore", "list", sess, params, []map[string]string{{"id": item, "folder": folder}})
	if err != nil {
		return false, err
	}
	invalid := &cliErrors.Error{Code: "invalid_response", Message: "could not decode the share permissions of file " + item}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return false, invalid
	}
	if len(rows) == 0 {
		return false, &cliErrors.Error{Code: "not_found", Message: "file " + item + " was not found in folder " + folder}
	}
	if len(rows) != 1 || len(rows[0]) != 3 {
		return false, invalid
	}
	gotID, idOK := jsonIDString(rows[0][0])
	gotFolder, folderOK := jsonIDString(rows[0][1])
	if !idOK || !folderOK || gotID != item || gotFolder != folder {
		return false, &cliErrors.Error{Code: "invalid_response", Message: "share permission response did not match file " + item}
	}
	var permissions []struct {
		Type     string `json:"type"`
		ShareURL string `json:"share_url"`
	}
	if err := json.Unmarshal(rows[0][2], &permissions); err != nil {
		return false, invalid
	}
	for _, permission := range permissions {
		if permission.Type == "anonymous" {
			return true, nil
		}
	}
	return false, nil
}

// jsonIDString accepts an OX id encoded as a JSON string or number.
func jsonIDString(raw json.RawMessage) (string, bool) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, text != ""
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), true
	}
	return "", false
}
