// ABOUTME: The "login" and "logout" commands: force a fresh credential login or explicitly
// ABOUTME: invalidate and clear the persisted session, rather than only ever authenticating
// ABOUTME: implicitly as a side effect of another command.
package cli

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/config"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	registerResource("login", runLogin)
	operations.Register(operations.Operation{
		Name:       "login",
		Resource:   "login",
		Action:     "login",
		Summary:    "Force a fresh credential login and persist the resulting session",
		Method:     http.MethodPost,
		Endpoint:   "/login",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"dry-run": {Type: "bool", Required: false, Description: "report the account that would be logged in, without contacting the server"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"status", "username"},
			ScalarFields: []string{"status", "username"},
			DryRunFields: []string{"status", "username"},
		},
	})

	registerResource("logout", runLogout)
	operations.Register(operations.Operation{
		Name:       "logout",
		Resource:   "logout",
		Action:     "logout",
		Summary:    "Invalidate the current session and clear the local session store",
		Method:     http.MethodGet,
		Endpoint:   "/login",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"dry-run": {Type: "bool", Required: false, Description: "report what would be invalidated, without contacting the server or clearing the local session"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"status"},
			ScalarFields: []string{"status"},
			DryRunFields: []string{"status"},
		},
	})
}

func init() {
	registerResource("change-password", runChangePassword)
	operations.Register(operations.Operation{
		Name:        "change-password",
		Resource:    "change-password",
		Action:      "change-password",
		Summary:     "Change the account password (destructive; requires --confirm <account email>; reads passwords from a hidden terminal prompt or MBX_PASSWORD and MBX_NEW_PASSWORD)",
		Method:      http.MethodPut,
		Endpoint:    "/passwordchange",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"current-password": {Type: "string", Required: false, Description: "deprecated: the current password as an argument, which other local users and shell history can see; prefer the terminal prompt or MBX_PASSWORD"},
			"new-password":     {Type: "string", Required: false, Description: "deprecated: the new password as an argument, which other local users and shell history can see; prefer the terminal prompt or MBX_NEW_PASSWORD"},
			"confirm":          {Type: "string", Required: true, Description: "must exactly match the configured account email"},
			"dry-run":          {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"status"},
			ScalarFields: []string{"status"},
			DryRunFields: []string{"status"},
		},
	})
}

// withPromptedLoginCredentials performs terminal-only first-run setup for an explicit
// login. The username is persisted through the config package; the password is overlaid
// only on this in-process environment accessor and never added to os.Environ.
func withPromptedLoginCredentials(
	args []string,
	settings config.Settings,
	configPath string,
	getenv func(string) string,
	prompts LoginPrompts,
) (func(string) string, *cliErrors.Error) {
	effectiveGetenv := settings.Environment(getenv)
	if !promptableLogin(args) {
		return effectiveGetenv, nil
	}

	if settings.Username == "" && prompts.Username != nil {
		promptedUsername, err := prompts.Username()
		if err != nil {
			return effectiveGetenv, &cliErrors.Error{Code: "invalid_argument", Message: "could not read mailbox.org email: " + err.Error()}
		}
		username, err := config.NormalizeUsername(promptedUsername)
		if err != nil {
			return effectiveGetenv, &cliErrors.Error{Code: "invalid_argument", Message: err.Error()}
		}
		username, err = config.SaveUsername(configPath, username)
		if err != nil {
			return effectiveGetenv, &cliErrors.Error{Code: "invalid_configuration", Message: "could not save mailbox.org email: " + err.Error()}
		}
		settings.Username = username
		effectiveGetenv = settings.Environment(getenv)
	}

	if prompts.Password == nil || effectiveGetenv("MBX_PASSWORD") != "" || strings.TrimSpace(effectiveGetenv("MBX_USERNAME")) == "" {
		return effectiveGetenv, nil
	}
	password, err := prompts.Password()
	if err != nil {
		return effectiveGetenv, &cliErrors.Error{Code: "invalid_argument", Message: "could not read password securely: " + err.Error()}
	}
	if password == "" {
		return effectiveGetenv, &cliErrors.Error{Code: "invalid_argument", Message: "password cannot be empty"}
	}

	return func(key string) string {
		if key == "MBX_PASSWORD" {
			return password
		}
		return effectiveGetenv(key)
	}, nil
}

func promptableLogin(args []string) bool {
	return len(args) == 1 && args[0] == "login"
}

func runLogin(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("login", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()

	if f.Bool("dry-run") {
		username := ""
		if getenv != nil {
			username = strings.TrimSpace(getenv("MBX_USERNAME"))
		}
		if username == "" {
			return exitWithError(stderr, llm, invalidArgument("missing credentials: configure username in config.toml (see \"mbx help config\")"))
		}
		renderLoginResult(stdout, llm, true, username)
		return 0
	}

	creds, err := config.LoadCredentials(getenv)
	if err != nil {
		return exitWithError(stderr, llm, invalidArgument(err.Error()))
	}

	store := &client.SessionStore{Path: sessionPath}
	c := client.New(httpClient, baseURL, clientName, clientVersion, store)

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	sess, err := c.Login(ctx, creds.Username, creds.Password)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "auth_failed"))
	}
	if err := store.Save(sess); err != nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "internal_error", Message: "could not persist session: " + err.Error()})
	}

	renderLoginResult(stdout, llm, false, creds.Username)
	return 0
}

func renderLoginResult(stdout io.Writer, llm, dryRun bool, username string) {
	if llm {
		render.CompactJSON(stdout, struct {
			Status   string `json:"status"`
			Username string `json:"username"`
		}{Status: "ok", Username: username})
		return
	}

	if dryRun {
		io.WriteString(stdout, "Dry run: would log in as "+username+"\n")
		return
	}
	io.WriteString(stdout, "Logged in as "+username+"\n")
}

func runLogout(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("logout", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")

	store := &client.SessionStore{Path: sessionPath}
	sess, err := store.Load()
	if err != nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "internal_error", Message: "could not read session store: " + err.Error()})
	}

	if sess.ID == "" {
		if len(sess.Cookies) == 0 {
			renderLogoutResult(stdout, llm, "no active session")
			return 0
		}
		if dryRun {
			renderLogoutResult(stdout, llm, "dry run: would clear the local cookie-only session")
			return 0
		}
		if err := store.Clear(); err != nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "internal_error", Message: "could not clear session store: " + err.Error()})
		}
		renderLogoutResult(stdout, llm, "cleared local session")
		return 0
	}

	if dryRun {
		renderLogoutResult(stdout, llm, "dry run: would log out and clear the local session")
		return 0
	}

	c := client.New(httpClient, baseURL, clientName, clientVersion, store)
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	logoutErr := c.Logout(ctx, sess)
	if logoutErr != nil && !cliErrors.IsSessionRejection(logoutErr) {
		return exitWithError(stderr, llm, toCLIError(logoutErr, "api_error"))
	}
	if err := store.Clear(); err != nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "internal_error", Message: "could not clear session store: " + err.Error()})
	}

	message := "logged out"
	if logoutErr != nil {
		message = "cleared expired local session"
	}
	renderLogoutResult(stdout, llm, message)
	return 0
}

func renderLogoutResult(stdout io.Writer, llm bool, message string) {
	if llm {
		render.CompactJSON(stdout, struct {
			Status string `json:"status"`
		}{Status: "ok"})
		return
	}
	io.WriteString(stdout, message+"\n")
}

// SecretPrompt reads one hidden value from an interactive terminal after showing label.
// The process entry point supplies it only when stdin is a terminal.
type SecretPrompt func(label string) (string, error)

// changePasswordArgsWarning is the warning message (code password_on_command_line) for a
// password passed as a command argument.
const changePasswordArgsWarning = "--current-password and --new-password put passwords in the process list and shell history; " +
	"use the hidden terminal prompt, or MBX_PASSWORD and MBX_NEW_PASSWORD, instead"

type changePasswordArgs struct {
	llm             bool
	dryRun          bool
	currentPassword string
	newPassword     string
	confirm         string
	passwordArgUsed bool
}

// parseChangePasswordArgs reads the change-password flags. Both the pre-dispatch prompt
// step and the command use it, so they read the arguments in the same way. When the error
// is not nil, only llm is set.
func parseChangePasswordArgs(args []string) (changePasswordArgs, *cliErrors.Error) {
	f, ferr := parseCommandFlags("change-password", args)
	if ferr != nil {
		return changePasswordArgs{llm: f.LLM()}, ferr
	}
	currentPassword, currentSet := f.Lookup("current-password")
	newPassword, newSet := f.Lookup("new-password")
	return changePasswordArgs{
		llm:             f.LLM(),
		dryRun:          f.Bool("dry-run"),
		currentPassword: currentPassword,
		newPassword:     newPassword,
		confirm:         f.String("confirm"),
		passwordArgUsed: currentSet || newSet,
	}, nil
}

// withPromptedChangePasswordSecrets runs before dispatch, where the real stderr and the
// terminal prompt are available. It warns when a password is passed as an argument, before
// any prompt, with writeWarning: as JSON when structured is true, like the account
// commands' password warning. For
// a valid, confirmed, non-dry-run invocation it asks for each password that neither an
// argument nor the environment supplies, and overlays the answers on this in-process
// environment accessor only (MBX_PASSWORD for the current password, MBX_NEW_PASSWORD for
// the new one). Invalid invocations are left to the command, which reports them without
// asking for any secret.
func withPromptedChangePasswordSecrets(args []string, getenv func(string) string, prompt SecretPrompt, stderr io.Writer, structured bool) (func(string) string, *cliErrors.Error) {
	if len(args) == 0 || args[0] != "change-password" {
		return getenv, nil
	}
	parsed, parseErr := parseChangePasswordArgs(args[1:])
	if parseErr != nil {
		return getenv, nil
	}
	if parsed.passwordArgUsed {
		writeWarning(stderr, structured, "password_on_command_line", changePasswordArgsWarning)
	}
	if parsed.dryRun || prompt == nil || getenv == nil {
		return getenv, nil
	}
	username := strings.TrimSpace(getenv("MBX_USERNAME"))
	if username == "" || parsed.confirm != username {
		return getenv, nil
	}

	overlay := map[string]string{}
	if parsed.currentPassword == "" && getenv("MBX_PASSWORD") == "" {
		current, err := prompt("Current mailbox.org password: ")
		if err != nil {
			return getenv, &cliErrors.Error{Code: "invalid_argument", Message: "could not read the current password securely: " + err.Error()}
		}
		overlay["MBX_PASSWORD"] = current
	}
	if parsed.newPassword == "" && getenv("MBX_NEW_PASSWORD") == "" {
		newPassword, err := prompt("New mailbox.org password: ")
		if err != nil {
			return getenv, &cliErrors.Error{Code: "invalid_argument", Message: "could not read the new password securely: " + err.Error()}
		}
		repeated, err := prompt("Repeat the new mailbox.org password: ")
		if err != nil {
			return getenv, &cliErrors.Error{Code: "invalid_argument", Message: "could not read the new password securely: " + err.Error()}
		}
		if repeated != newPassword {
			return getenv, &cliErrors.Error{Code: "invalid_argument", Message: "the two new passwords do not match; the password was not changed"}
		}
		overlay["MBX_NEW_PASSWORD"] = newPassword
	}
	if len(overlay) == 0 {
		return getenv, nil
	}
	return func(key string) string {
		if value, ok := overlay[key]; ok {
			return value
		}
		return getenv(key)
	}, nil
}

// runChangePassword changes the account password. This is destructive (a wrong new
// password locks the account holder out), so it requires --confirm to exactly match the
// configured account email before anything is sent, and never auto-retries. The current
// password comes from --current-password, MBX_PASSWORD or the terminal prompt; the new
// password from --new-password, MBX_NEW_PASSWORD or the terminal prompt (asked twice).
// The prompt runs before dispatch, see withPromptedChangePasswordSecrets.
func runChangePassword(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	parsed, parseErr := parseChangePasswordArgs(args)
	llm := parsed.llm
	if parseErr != nil {
		return exitWithError(stderr, llm, parseErr)
	}
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	username := strings.TrimSpace(getenv("MBX_USERNAME"))
	if username == "" {
		return exitWithError(stderr, llm, invalidArgument("missing credentials: configure username in config.toml (see \"mbx help config\")"))
	}
	if parsed.confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("changing the password is destructive; repeat the account's email exactly with --confirm "+username))
	}
	if parsed.confirm != username {
		return exitWithError(stderr, llm, invalidArgument("--confirm does not match the account's email ("+username+"); refusing to change the password"))
	}

	// A dry run never sends either password, so it does not need them; the real run asks
	// for missing values on a terminal or reports which input to set.
	if parsed.dryRun {
		renderChangePasswordResult(stdout, llm, true)
		return 0
	}

	currentPassword := parsed.currentPassword
	if currentPassword == "" {
		currentPassword = getenv("MBX_PASSWORD")
	}
	newPassword := parsed.newPassword
	if newPassword == "" {
		newPassword = getenv("MBX_NEW_PASSWORD")
	}
	if currentPassword == "" {
		return exitWithError(stderr, llm, invalidArgument("the current password is required: run on a terminal to be asked for it, or set MBX_PASSWORD (--current-password also works but exposes it)"))
	}
	if newPassword == "" {
		return exitWithError(stderr, llm, invalidArgument("the new password is required: run on a terminal to be asked for it, or set MBX_NEW_PASSWORD (--new-password also works but exposes it)"))
	}

	// The current password is the login password, so it is also the credential fallback
	// when the stored session cannot be reused and MBX_PASSWORD is not set.
	loginPassword := getenv("MBX_PASSWORD")
	if loginPassword == "" {
		loginPassword = currentPassword
	}

	store := &client.SessionStore{Path: sessionPath}
	c := client.New(httpClient, baseURL, clientName, clientVersion, store)

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	sess, aerr := c.Authenticate(ctx, username, loginPassword)
	if aerr != nil {
		return exitWithError(stderr, llm, toCLIError(aerr, "auth_failed"))
	}

	body := map[string]string{"old_password": currentPassword, "new_password": newPassword}
	if _, err := c.DoJSON(ctx, http.MethodPut, "/passwordchange", "update", sess, nil, body); err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	renderChangePasswordResult(stdout, llm, false)
	return 0
}

func renderChangePasswordResult(stdout io.Writer, llm, dryRun bool) {
	if llm {
		render.CompactJSON(stdout, struct {
			Status string `json:"status"`
		}{Status: "ok"})
		return
	}

	if dryRun {
		io.WriteString(stdout, "Dry run: would change the account password\n")
		return
	}
	io.WriteString(stdout, "Password changed\n")
}
