// ABOUTME: The one sign-in helper that command handlers call before their first API request.
// ABOUTME: Loads credentials, builds the client, authenticates and keeps the real error code.
package cli

// Sign-in guide for command handlers
//
// A handler that talks to the API signs in with signIn, after all local checks and
// after any --dry-run that needs no server data has returned:
//
//	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
//	defer cancel()
//	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
//	if authErr != nil {
//		return exitWithError(stderr, llm, authErr)
//	}
//	data, err := c.Do(ctx, http.MethodGet, "/quota", "filestore", sess, nil)
//
// The caller owns the context. Use the same ctx for sign-in and the requests that follow
// when the old code did so (most handlers). Some handlers (folders, mail) gave sign-in its
// own requestTimeout and their requests a new one; keep that split by passing a separate
// sign-in context and cancelling it right after signIn returns:
//
//	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
//	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
//	cancelAuth()
//
// signIn returns the error that the handler must write, with its real code:
//
//   - missing credentials: invalid_argument (exit 4); no request is sent
//   - errors that the client already structured (rate limit 429, server errors, LGI-/SES-
//     login refusals, invalid responses) keep their code, HTTP status and retry data
//   - a transport failure (for example a refused connection or a timeout) becomes
//     network_error (exit 1, retryable): the credentials were not rejected, so the caller
//     must not log in again because of it
//   - any other error becomes auth_failed (exit 2), as toCLIError did in every handler
//     before. This includes an unreadable session file, which "mbx login" repairs.
//
// Write the error once with exitWithError(stderr, llm, authErr). Do not map it again
// with toCLIError(err, "auth_failed"): it is already final.
//
// After a handler moves to signIn, add an argument list for it to signInCommands in
// session_auth_test.go. That test checks the three sign-in failures end to end.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/config"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

// signIn loads the sign-in credentials from getenv, opens the session store at
// sessionPath and authenticates within ctx. It returns the client and session for the
// requests that follow, or the error to report (see the guide above).
func signIn(ctx context.Context, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) (*client.Client, client.Session, *cliErrors.Error) {
	creds, err := config.LoadAuthenticationCredentials(getenv)
	if err != nil {
		return nil, client.Session{}, invalidArgument(err.Error())
	}

	store := &client.SessionStore{Path: sessionPath}
	c := client.New(httpClient, baseURL, clientName, clientVersion, store)

	sess, err := c.Authenticate(ctx, creds.Username, creds.Password)
	if err != nil {
		var cerr *cliErrors.Error
		if !errors.As(err, &cerr) && isTransportError(err) {
			return nil, client.Session{}, &cliErrors.Error{Code: "network_error", Message: "sign-in request failed: " + err.Error(), Retryable: true}
		}
		return nil, client.Session{}, toCLIError(err, "auth_failed")
	}
	return c, sess, nil
}

// isTransportError reports whether err comes from the HTTP transport (connection,
// DNS, TLS or timeout) rather than from the provider or the local session store.
func isTransportError(err error) bool {
	var urlErr *url.Error
	var netErr net.Error
	return errors.As(err, &urlErr) || errors.As(err, &netErr) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
