// ABOUTME: HTTP client for the mailbox.org OX middleware: full login, autologin,
// ABOUTME: session-aware authenticated requests, and error-envelope mapping.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/retryafter"
)

// Client talks to the mailbox.org OX middleware.
type Client struct {
	http          *http.Client
	baseURL       string
	clientName    string
	clientVersion string
	store         *SessionStore

	// Response size limits by kind; New sets the defaults and tests may lower them.
	maxJSONBytes     int64
	maxDocumentBytes int64
}

// New creates a Client. httpClient and store are injected so tests can replace transport
// and persistence.
func New(httpClient *http.Client, baseURL, clientName, clientVersion string, store *SessionStore) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	// A Client supplied by the caller may be shared with unrelated requests. Keep its
	// redirect policy intact and wrap it on a private shallow copy instead of mutating the
	// shared value. Transport and Jar remain shared, as they are designed to be.
	safeHTTP := *httpClient
	callerRedirectPolicy := safeHTTP.CheckRedirect
	// net/http calls CheckRedirect only for a redirect, so via always holds at least the
	// original request.
	safeHTTP.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return fmt.Errorf("refusing to follow redirect to untrusted host %q", req.URL.Host)
		}
		// Matching hosts are not enough. Go forwards the Cookie header on a same-host
		// redirect, and the session id travels in the query string, so an https response
		// redirecting to http on the same host would put both on the wire in cleartext. A host
		// reached on its default ports makes that reachable without any host change, since
		// https://host and http://host share a URL host. Upgrading to https stays allowed.
		if via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return fmt.Errorf("refusing to follow redirect from https to %q", req.URL.Scheme)
		}
		if callerRedirectPolicy != nil {
			return callerRedirectPolicy(req, via)
		}
		// Setting CheckRedirect replaces net/http's default policy, including its loop
		// protection, so reproduce the default ten-request limit after the security checks.
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &Client{
		http:          &safeHTTP,
		baseURL:       strings.TrimRight(baseURL, "/"),
		clientName:    clientName,
		clientVersion: clientVersion,
		store:         store,

		maxJSONBytes:     MaxJSONResponseBytes,
		maxDocumentBytes: MaxDocumentResponseBytes,
	}
}

// Login performs a full credential-based login with staySignedIn=true, so the returned
// session's cookies carry a server-set expiry and can later be used for autologin.
func (c *Client) Login(ctx context.Context, username, password string) (Session, error) {
	form := url.Values{
		"name":         {username},
		"password":     {password},
		"client":       {c.clientName},
		"version":      {c.clientVersion},
		"staySignedIn": {"true"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login?action=login", strings.NewReader(form.Encode()))
	if err != nil {
		return Session{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	sess, err := c.doLogin(req)
	if err != nil {
		return Session{}, err
	}
	// A credential login starts a new cookie set, so deletion markers and already-expired
	// cookies have nothing to invalidate. Do not return them to callers that immediately
	// use this session for an authenticated request.
	sess.Cookies = mergeCookies(nil, sess.Cookies)
	sess.Username = NormalizeAccount(username)
	return sess, nil
}

// Autologin reuses the stored session's cookies to obtain a fresh session id without
// resending credentials.
func (c *Client) Autologin(ctx context.Context) (Session, error) {
	stored, err := c.store.Load()
	if err != nil {
		return Session{}, err
	}
	return c.autologin(ctx, stored)
}

// autologin refreshes the given stored session. Authenticate passes the session it has
// already checked against the account, so a session file that another process replaces
// in the meantime cannot be used without that check.
func (c *Client) autologin(ctx context.Context, stored Session) (Session, error) {
	q := url.Values{"action": {"autologin"}, "client": {c.clientName}, "version": {c.clientVersion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/login?"+q.Encode(), nil)
	if err != nil {
		return Session{}, err
	}
	addCookies(req, stored.Cookies)

	sess, err := c.doLogin(req)
	if err != nil {
		return Session{}, err
	}
	sess.Cookies = mergeCookies(stored.Cookies, sess.Cookies)
	// Autologin refreshes the same server-side login, so the account stays the one that
	// performed the original credential login.
	sess.Username = stored.Username
	return sess, nil
}

// Authenticate is the normal reauthentication path: try autologin using a stored session
// first, falling back to full credential login when there is no stored session or
// autologin is rejected. Username and password may be empty while autologin succeeds; an
// absent fallback is rejected locally without sending an empty credential login. The
// resulting session is persisted.
//
// A stored session is reused only for the account that created it. When the stored
// session records a different account than username, or records no account at all (a
// file written by an older mbx), autologin is skipped: the stored cookies could sign in
// to the wrong mailbox. A credential login then replaces the session, or, without a
// password, Authenticate fails and tells the user to run "mbx login".
func (c *Client) Authenticate(ctx context.Context, username, password string) (Session, error) {
	stored, err := c.store.Load()
	if err != nil {
		return Session{}, err
	}

	// Autologin authenticates only with stored cookies; the previous session id is not part
	// of that request. An id left behind after all cookies expired therefore cannot help and
	// should go straight to credential login.
	unusable := ""
	if len(stored.Cookies) > 0 {
		unusable = storedSessionMismatch(stored, username)
	}
	if len(stored.Cookies) > 0 && unusable == "" {
		sess, autologinErr := c.autologin(ctx, stored)
		if autologinErr == nil {
			if saveErr := c.store.Save(sess); saveErr != nil {
				return Session{}, saveErr
			}
			return sess, nil
		}
		// Only an application-level refusal of the stored cookies justifies a credential
		// fallback. Retrying a transient autologin failure by sending the user's password
		// both masks the real failure and discloses credentials on a request the caller did
		// not need to make; a login rate limit must be waited out, not answered with
		// another login attempt.
		if !cliErrors.IsSessionRejection(autologinErr) {
			return Session{}, autologinErr
		}
	}
	if strings.TrimSpace(username) == "" || password == "" {
		message := "no reusable session; run \"mbx login\" (or set MBX_PASSWORD for non-interactive authentication)"
		if unusable != "" {
			message = unusable + "; run \"mbx login\" to replace it (or set MBX_PASSWORD for non-interactive authentication)"
		}
		return Session{}, &cliErrors.Error{Code: "auth_failed", Message: message}
	}

	sess, err := c.Login(ctx, username, password)
	if err != nil {
		return Session{}, err
	}
	if err := c.store.Save(sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// storedSessionMismatch explains why a stored session must not be used for autologin by
// username, or returns "" when it may be used. An unknown username (empty) cannot
// contradict a recorded account, so only a missing record blocks reuse in that case.
func storedSessionMismatch(stored Session, username string) string {
	if stored.Username == "" {
		return "the saved session does not record which account created it"
	}
	if account := NormalizeAccount(username); account != "" && account != stored.Username {
		return "the saved session belongs to a different account than the configured username"
	}
	return ""
}

// mergeCookies combines a previously stored cookie set with newly-received cookies,
// keyed by name: a new cookie overrides an old one with the same name, and old cookies
// absent from new are kept as-is. Autologin's response typically resends only the one
// cookie the server actually refreshed, so this preserves the rest of the stored set
// instead of letting a partial response wipe it out.
func mergeCookies(old, updated []*http.Cookie) []*http.Cookie {
	merged := make([]*http.Cookie, 0, len(old)+len(updated))
	seen := make(map[string]bool, len(updated))
	now := time.Now()
	for _, ck := range updated {
		if ck == nil {
			continue
		}
		seen[ck.Name] = true
		if !cookieExpiredAt(ck, now) {
			merged = append(merged, ck)
		}
	}
	for _, ck := range old {
		if ck != nil && !seen[ck.Name] && !cookieExpiredAt(ck, now) {
			merged = append(merged, ck)
		}
	}
	return merged
}

// httpStatusError turns a non-success HTTP status without an OX error envelope into the
// same structured error contract used for middleware failures. OX normally reports
// application errors in a JSON envelope, even with HTTP 200, so callers check that first
// and preserve its more specific code and message.
func httpStatusError(status int) *cliErrors.Error {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return nil
	}
	return &cliErrors.Error{
		Code:       "http_error",
		Message:    fmt.Sprintf("unexpected HTTP status %d (%s)", status, http.StatusText(status)),
		HTTPStatus: status,
		Retryable:  status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || (status >= http.StatusInternalServerError && status < 600),
	}
}

// withRetryAfter copies the standard HTTP backoff hint into the CLI's structured error
// contract. Retry-After permits either a whole number of seconds or an HTTP date; the
// shared parser caps the delay at retryafter.MaxDelay, like the request guard does.
func withRetryAfter(resp *http.Response, err *cliErrors.Error) *cliErrors.Error {
	if err != nil {
		err.RetryAfterMs = retryafter.Milliseconds(retryafter.Parse(resp.Header.Get("Retry-After"), time.Now()))
	}
	return err
}

// authenticatedQuery returns an owned query map for one request. Callers commonly reuse a
// url.Values value, and adding action/session directly to it both leaks credentials into
// that value and races when requests share it. An empty action is explicitly removed for
// endpoints such as /multiple whose contract omits the parameter.
func authenticatedQuery(params url.Values, action, session string, includeAction bool) url.Values {
	q := make(url.Values, len(params)+2)
	for key, values := range params {
		q[key] = append([]string(nil), values...)
	}
	if includeAction {
		q.Set("action", action)
	} else {
		q.Del("action")
	}
	q.Set("session", session)
	return q
}

func addCookies(req *http.Request, cookies []*http.Cookie) {
	for _, cookie := range cookies {
		if cookie != nil {
			req.AddCookie(cookie)
		}
	}
}

func invalidResponse(status int, message string) *cliErrors.Error {
	return &cliErrors.Error{
		Code:       "invalid_response",
		Message:    message,
		HTTPStatus: status,
	}
}

// responseEnvelopeError maps the OX middleware's top-level error field without letting
// a malformed field pass as a successful response. For non-success HTTP statuses, status
// handling remains authoritative when the purported error field itself is malformed.
func responseEnvelopeError(body []byte, status int) *cliErrors.Error {
	var probe struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || len(probe.Error) == 0 {
		return nil
	}

	errorField := bytes.TrimSpace(probe.Error)
	if bytes.Equal(errorField, []byte("null")) {
		return nil
	}

	var message string
	if err := json.Unmarshal(errorField, &message); err != nil {
		if httpStatusError(status) != nil {
			return nil
		}
		return invalidResponse(status, `response "error" field was not a string`)
	}
	if message == "" {
		return nil
	}

	var env cliErrors.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return invalidResponse(status, "could not decode error response envelope")
	}
	return cliErrors.FromEnvelope(env, status)
}

// decodeDataEnvelope unwraps a response payload when one is present. The OX editing
// contract also permits an empty success object for updates, so a missing data member is a
// valid no-payload result; commands that require a returned identifier validate it after
// this method returns.
func decodeDataEnvelope(body []byte, status int) ([]byte, error) {
	var envelope *struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, invalidResponse(status, "could not decode response envelope")
	}
	if envelope == nil {
		return nil, invalidResponse(status, "response envelope was null")
	}
	return envelope.Data, nil
}

func decodeTimestampedDataEnvelope(body []byte, status int) ([]byte, int64, error) {
	var envelope struct {
		Data      json.RawMessage `json:"data"`
		Timestamp *int64          `json:"timestamp"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, 0, invalidResponse(status, "could not decode timestamped response envelope")
	}
	if envelope.Data == nil {
		return nil, 0, invalidResponse(status, "response did not include a data field")
	}
	if envelope.Timestamp == nil || *envelope.Timestamp <= 0 {
		return nil, 0, invalidResponse(status, "response did not include a positive timestamp")
	}
	return envelope.Data, *envelope.Timestamp, nil
}

// Response size limits. Every response is read into memory, so a broken or hostile
// server could otherwise exhaust memory with an endless body. The limits are far above
// legitimate responses: JSON answers (lists, mail with bodies, batch results) stay well
// below MaxJSONResponseBytes, and document downloads (Drive files, mail and PIM
// attachments, iCal/vCard exports) are bounded by MaxDocumentResponseBytes, above the
// provider's mail size limit and typical Drive files.
const (
	MaxJSONResponseBytes     int64 = 256 << 20
	MaxDocumentResponseBytes int64 = 4 << 30
)

// responseKind selects how exchange checks a response body for an OX error.
type responseKind int

const (
	// jsonResponse is a JSON envelope; an "error" member is a failure.
	jsonResponse responseKind = iota
	// multipartCallbackResponse is JSON, possibly wrapped in an HTML callback document.
	multipartCallbackResponse
	// documentResponse is a raw document; only a certain error envelope is a failure,
	// because a stored file may itself contain an "error" key.
	documentResponse
)

func (c *Client) responseLimit(kind responseKind) int64 {
	limit := c.maxJSONBytes
	if kind == documentResponse {
		limit = c.maxDocumentBytes
	}
	if maxInt := int64(^uint(0) >> 1); limit > maxInt-1 {
		limit = maxInt - 1
	}
	return limit
}

// apiRequest describes one authenticated middleware call.
type apiRequest struct {
	method      string
	module      string
	action      string
	omitAction  bool
	sess        Session
	params      url.Values
	body        io.Reader
	contentType string
	kind        responseKind
}

// send is the single authenticated request pipeline: it builds the URL with the session
// query, adds the session cookies and runs exchange.
func (c *Client) send(ctx context.Context, r apiRequest) ([]byte, int, error) {
	q := authenticatedQuery(r.params, r.action, r.sess.ID, !r.omitAction)
	req, err := http.NewRequestWithContext(ctx, r.method, c.baseURL+r.module+"?"+q.Encode(), r.body)
	if err != nil {
		return nil, 0, err
	}
	if r.contentType != "" {
		req.Header.Set("Content-Type", r.contentType)
	}
	addCookies(req, r.sess.Cookies)
	body, resp, err := c.exchange(req, r.kind)
	if err != nil {
		return nil, 0, err
	}
	return body, resp.StatusCode, nil
}

// exchange sends req, reads the body within the size limit for kind, and maps an OX
// error envelope or a failing HTTP status to a structured error. The returned response
// has its body closed; callers use it only for the status and cookies.
func (c *Client) exchange(req *http.Request, kind responseKind) ([]byte, *http.Response, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp, c.responseLimit(kind))
	if err != nil {
		return nil, nil, err
	}

	switch kind {
	case documentResponse:
		if cliErrors.IsCertainErrorEnvelope(body) {
			var env cliErrors.Envelope
			if err := json.Unmarshal(body, &env); err != nil {
				return nil, nil, err
			}
			return nil, nil, withRetryAfter(resp, cliErrors.FromEnvelope(env, resp.StatusCode))
		}
	case multipartCallbackResponse:
		body = extractMultipartCallbackJSON(body)
		fallthrough
	default:
		if responseErr := responseEnvelopeError(body, resp.StatusCode); responseErr != nil {
			return nil, nil, withRetryAfter(resp, responseErr)
		}
	}
	if statusErr := withRetryAfter(resp, httpStatusError(resp.StatusCode)); statusErr != nil {
		return nil, nil, statusErr
	}
	return body, resp, nil
}

// readResponseBody reads at most limit bytes and fails clearly when the body is larger.
func readResponseBody(resp *http.Response, limit int64) ([]byte, error) {
	if resp.ContentLength > limit {
		return nil, responseTooLarge(resp.StatusCode, limit)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, responseTooLarge(resp.StatusCode, limit)
	}
	return body, nil
}

func responseTooLarge(status int, limit int64) *cliErrors.Error {
	return &cliErrors.Error{
		Code:       "response_too_large",
		Message:    fmt.Sprintf("response is larger than the %d-byte limit; mbx stopped reading it", limit),
		HTTPStatus: status,
	}
}

func (c *Client) doLogin(req *http.Request) (Session, error) {
	body, resp, err := c.exchange(req, jsonResponse)
	if err != nil {
		return Session{}, err
	}

	var payload struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Session{}, invalidResponse(resp.StatusCode, "could not decode login response")
	}
	if strings.TrimSpace(payload.Session) == "" {
		return Session{}, &cliErrors.Error{
			Code:       "invalid_response",
			Message:    "login response did not include a session id",
			HTTPStatus: resp.StatusCode,
		}
	}

	return Session{ID: payload.Session, Cookies: resp.Cookies()}, nil
}

// Logout invalidates the given session via GET /login?action=logout. A successful
// logout returns an empty body rather than a "data" envelope, so this does not reuse Do.
func (c *Client) Logout(ctx context.Context, sess Session) error {
	q := url.Values{"action": {"logout"}, "session": {sess.ID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/login?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	addCookies(req, sess.Cookies)
	_, _, err = c.exchange(req, jsonResponse)
	return err
}

// Do performs an authenticated request, adding the session id as a query parameter
// and sending the session's cookies, and returns the raw "data" success payload or a
// structured error mapped from the OX error envelope. module is relative to the base
// URL, e.g. "/user".
func (c *Client) Do(ctx context.Context, method, module, action string, sess Session, params url.Values) ([]byte, error) {
	body, status, err := c.send(ctx, apiRequest{method: method, module: module, action: action, sess: sess, params: params})
	if err != nil {
		return nil, err
	}
	return decodeDataEnvelope(body, status)
}

// DoWithTimestamp performs an authenticated request like Do, but also returns the
// envelope's top-level "timestamp" field. Some OX write actions (e.g. user update) require
// the caller to echo back the record's last-known timestamp for optimistic locking, so the
// caller must first read it off of a prior read via this method.
func (c *Client) DoWithTimestamp(ctx context.Context, method, module, action string, sess Session, params url.Values) ([]byte, int64, error) {
	body, status, err := c.send(ctx, apiRequest{method: method, module: module, action: action, sess: sess, params: params})
	if err != nil {
		return nil, 0, err
	}
	return decodeTimestampedDataEnvelope(body, status)
}

// DoRaw performs an authenticated request like Do, but returns the raw response body
// unchanged instead of unwrapping a JSON "data" envelope, for actions that return binary
// content directly (e.g. infostore?action=document file downloads) rather than a JSON
// envelope. It still detects and maps the OX JSON error envelope when the response is one.
// A document body is whatever the user stored, so it is tested against the strict
// envelope shape: a file that merely contains an "error" key is content, not a failure.
func (c *Client) DoRaw(ctx context.Context, method, module, action string, sess Session, params url.Values) ([]byte, error) {
	body, _, err := c.send(ctx, apiRequest{method: method, module: module, action: action, sess: sess, params: params, kind: documentResponse})
	return body, err
}

// DoRawBody performs an authenticated request like Do, but sends body as-is with the
// given Content-Type instead of no request body, for actions that accept a raw payload
// rather than JSON or multipart/form-data (e.g. mail?action=new's PUT variant, which
// stores a raw RFC 822 message instead of a structured mail object).
func (c *Client) DoRawBody(ctx context.Context, method, module, action string, sess Session, params url.Values, contentType string, body []byte) ([]byte, error) {
	respBody, status, err := c.send(ctx, apiRequest{
		method: method, module: module, action: action, sess: sess, params: params,
		body: bytes.NewReader(body), contentType: contentType,
	})
	if err != nil {
		return nil, err
	}
	return decodeDataEnvelope(respBody, status)
}

// DoMultipart performs an authenticated multipart/form-data POST with a JSON "json" part
// and a single "file" part, used by upload actions like infostore?action=new. Multipart
// upload actions on this deployment wrap their JSON response in an HTML document invoking
// a "callback_<action>(...)" JavaScript function instead of returning bare JSON; DoMultipart
// extracts and returns the JSON "data" payload from that wrapper transparently.
func (c *Client) DoMultipart(ctx context.Context, module, action string, sess Session, params url.Values, meta any, filename, contentType string, content io.Reader) ([]byte, error) {
	if content == nil {
		return nil, fmt.Errorf("multipart content reader is nil")
	}
	return c.doMultipart(ctx, module, action, sess, params, meta, "json", []Attachment{{
		FieldName: "file", Filename: filename, ContentType: contentType, Content: content,
	}})
}

// Attachment is one additional multipart file part for DoMultipartCallback, keyed by its
// form field name (e.g. "file_0", "file_1", ...) so the "json" meta part can reference it.
type Attachment struct {
	FieldName   string
	Filename    string
	ContentType string
	Content     io.Reader
}

// DoMultipartCallback performs an authenticated multipart/form-data POST with a JSON
// "json_0" part and zero or more named file parts (e.g. mail?action=new with message
// metadata plus optional attachments), and parses a response wrapped in an HTML document
// invoking a "callback_<action>(...)" JavaScript function (or bare JSON) into the same
// "data" envelope shape Do/DoJSON return. The "json_0" field name (rather than "json") is
// mandated by the OX mail?action=new request contract.
func (c *Client) DoMultipartCallback(ctx context.Context, module, action string, sess Session, params url.Values, meta any, attachments []Attachment) ([]byte, error) {
	return c.doMultipart(ctx, module, action, sess, params, meta, "json_0", attachments)
}

// doMultipart encodes a JSON meta part named jsonField plus the file parts and posts
// them through the shared pipeline, unwrapping a callback document in the response.
func (c *Client) doMultipart(ctx context.Context, module, action string, sess Session, params url.Values, meta any, jsonField string, attachments []Attachment) ([]byte, error) {
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	jsonHeader := textproto.MIMEHeader{}
	jsonHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, jsonField))
	jsonHeader.Set("Content-Type", "application/json")
	jsonPart, err := mw.CreatePart(jsonHeader)
	if err != nil {
		return nil, err
	}
	if _, err := jsonPart.Write(metaJSON); err != nil {
		return nil, err
	}

	for _, a := range attachments {
		if a.Content == nil {
			return nil, fmt.Errorf("multipart attachment %q content reader is nil", a.FieldName)
		}
		fileHeader := textproto.MIMEHeader{}
		fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, a.FieldName, a.Filename))
		fileHeader.Set("Content-Type", a.ContentType)
		filePart, err := mw.CreatePart(fileHeader)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(filePart, a.Content); err != nil {
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	body, status, err := c.send(ctx, apiRequest{
		method: http.MethodPost, module: module, action: action, sess: sess, params: params,
		body: &buf, contentType: mw.FormDataContentType(), kind: multipartCallbackResponse,
	})
	if err != nil {
		return nil, err
	}
	return decodeDataEnvelope(body, status)
}

// extractMultipartCallbackJSON pulls the JSON payload out of a multipart upload response.
// Some multipart-based OX actions return it wrapped in an HTML document that invokes a
// "callback_<action>(...)" JavaScript function; others return bare JSON. This extracts the
// first complete JSON object following that callback either way, so trailing HTML, CSS,
// or JavaScript braces cannot become part of the payload.
func extractMultipartCallbackJSON(body []byte) []byte {
	trimmed := bytes.TrimSpace(body)
	if json.Valid(trimmed) {
		return trimmed
	}

	marker := bytes.Index(body, []byte("callback_"))
	if marker < 0 {
		return body
	}
	search := body[marker+len("callback_"):]
	for len(search) > 0 {
		start := bytes.IndexByte(search, '{')
		if start < 0 {
			break
		}
		candidate := search[start:]
		decoder := json.NewDecoder(bytes.NewReader(candidate))
		var payload json.RawMessage
		if err := decoder.Decode(&payload); err == nil {
			payload = bytes.TrimSpace(payload)
			if len(payload) > 0 && payload[0] == '{' && json.Valid(payload) {
				return payload
			}
		}
		search = candidate[1:]
	}
	return body
}

// jsonRequest marshals reqBody for a JSON request body.
func jsonRequest(method, module, action string, sess Session, params url.Values, reqBody any) (apiRequest, error) {
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		return apiRequest{}, err
	}
	return apiRequest{
		method: method, module: module, action: action, sess: sess, params: params,
		body: bytes.NewReader(encoded), contentType: "application/json",
	}, nil
}

// DoJSON performs an authenticated request like Do, but marshals reqBody as JSON and
// sends it as the request body with a "application/json" Content-Type. Some OX actions
// (e.g. group search) require their parameters in a JSON body rather than the query
// string, even though the request is read-only.
func (c *Client) DoJSON(ctx context.Context, method, module, action string, sess Session, params url.Values, reqBody any) ([]byte, error) {
	r, err := jsonRequest(method, module, action, sess, params, reqBody)
	if err != nil {
		return nil, err
	}
	body, status, err := c.send(ctx, r)
	if err != nil {
		return nil, err
	}
	return decodeDataEnvelope(body, status)
}

// DoJSONRaw performs an authenticated request like DoJSON, but returns the raw response
// body unchanged instead of unwrapping a top-level "data" envelope, for actions whose
// response is not a {"data":...} object (e.g. multiple?action= batch calls, which return
// a bare JSON array of per-sub-request results). If action is "" it is omitted from the
// query string, since the multiple endpoint takes no action parameter.
func (c *Client) DoJSONRaw(ctx context.Context, method, module, action string, sess Session, params url.Values, reqBody any) ([]byte, error) {
	r, err := jsonRequest(method, module, action, sess, params, reqBody)
	if err != nil {
		return nil, err
	}
	r.omitAction = action == ""
	body, _, err := c.send(ctx, r)
	return body, err
}

// DoJSONWithTimestamp performs an authenticated request like DoJSON, but also returns
// the envelope's top-level "timestamp" field. Some OX write actions (e.g. tasks new)
// report an optimistic-locking timestamp only at the envelope's top level, not inside
// "data", so a subsequent update needs this to avoid a lost-update conflict.
func (c *Client) DoJSONWithTimestamp(ctx context.Context, method, module, action string, sess Session, params url.Values, reqBody any) ([]byte, int64, error) {
	r, err := jsonRequest(method, module, action, sess, params, reqBody)
	if err != nil {
		return nil, 0, err
	}
	body, status, err := c.send(ctx, r)
	if err != nil {
		return nil, 0, err
	}
	return decodeTimestampedDataEnvelope(body, status)
}
