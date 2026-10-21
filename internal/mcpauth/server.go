// ABOUTME: Single-owner OAuth 2.1 authorization server for the remote mbx MCP server.
// ABOUTME: Serves discovery metadata, client registration, owner approval, tokens and bearer checks.
package mcpauth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/time/rate"
)

// Defaults for Options.
const (
	DefaultAccessTokenTTL  = time.Hour
	DefaultRefreshTokenTTL = 30 * 24 * time.Hour
	codeTTL                = 2 * time.Minute
	maxBodyBytes           = 16 << 10
	bodyReadTimeout        = 10 * time.Second
	// Registration is open to anyone, as clients register themselves. Registered
	// clients wait in memory for approval for pendingTTL; the rate limit keeps their
	// number below maxPendingClients, so a flood cannot evict a pending client.
	pendingTTL         = 30 * time.Minute
	maxPendingClients  = 1000
	registrationsBurst = 30
	registrationsEvery = 2 * time.Second
	// MinOwnerSecretLength makes guessing the approval secret impossible, so wrong
	// attempts need only a delay, not a lockout that would also lock out the owner.
	MinOwnerSecretLength = 32
)

// wrongSecretDelay slows down each wrong owner secret. Tests set it to zero.
var wrongSecretDelay = time.Second

// Fixed OAuth redirect URIs of the hosted Claude and ChatGPT apps.
var defaultRedirectURIs = []string{
	"https://claude.ai/api/mcp/auth_callback",
	"https://claude.com/api/mcp/auth_callback",
	"https://chatgpt.com/connector_platform_oauth_redirect",
}

// ChatGPT also uses per-connector callbacks below this prefix.
const chatGPTConnectorPrefix = "https://chatgpt.com/connector/oauth/"

var chatGPTConnectorID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Options configures New.
type Options struct {
	// Issuer is the public base URL, for example https://mbx.example.org. It is the
	// OAuth issuer and the base of every endpoint URL.
	Issuer string
	// Resource is the protected MCP endpoint URL, for example https://mbx.example.org/mcp.
	Resource string
	// OwnerSecret approves new clients. It must have at least MinOwnerSecretLength bytes.
	OwnerSecret string
	// StatePath is the owner-only file for clients and token hashes.
	StatePath string
	// ExtraRedirectURIs are accepted in addition to the Claude, ChatGPT and loopback ones.
	ExtraRedirectURIs []string
	// Logger receives one line per security event. Nil discards them.
	Logger *log.Logger
	// Now replaces time.Now in tests.
	Now             func() time.Time
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

// Server is the authorization server. It is safe for concurrent use.
type Server struct {
	opts       Options
	secretHash [sha256.Size]byte

	mu      sync.Mutex
	state   *state
	pending map[string]*client   // registered, not yet approved
	codes   map[string]*authCode // keyed by code hash
	limiter *rate.Limiter        // for registrations
	// saveMu orders state file writes, which run without mu.
	saveMu sync.Mutex
}

type authCode struct {
	ClientID    string
	RedirectURI string
	Challenge   string
	ExpiresAt   time.Time
}

// New loads the state file and returns a Server.
func New(opts Options) (*Server, error) {
	if len(opts.OwnerSecret) < MinOwnerSecretLength {
		return nil, fmt.Errorf("the owner secret must have at least %d characters", MinOwnerSecretLength)
	}
	if opts.Issuer == "" || opts.Resource == "" || opts.StatePath == "" {
		return nil, fmt.Errorf("mcpauth: Issuer, Resource and StatePath are required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AccessTokenTTL <= 0 {
		opts.AccessTokenTTL = DefaultAccessTokenTTL
	}
	if opts.RefreshTokenTTL <= 0 {
		opts.RefreshTokenTTL = DefaultRefreshTokenTTL
	}
	if opts.Logger == nil {
		opts.Logger = log.New(io.Discard, "", 0)
	}
	st, err := loadState(opts.StatePath)
	if err != nil {
		return nil, err
	}
	return &Server{
		opts:       opts,
		secretHash: sha256.Sum256([]byte(opts.OwnerSecret)),
		state:      st,
		pending:    map[string]*client{},
		codes:      map[string]*authCode{},
		limiter:    rate.NewLimiter(rate.Every(registrationsEvery), registrationsBurst),
	}, nil
}

// ResourceMetadataURL is the RFC 9728 metadata URL for the MCP endpoint, for the
// WWW-Authenticate header of unauthenticated requests.
func (s *Server) ResourceMetadataURL() string {
	return s.opts.Issuer + "/.well-known/oauth-protected-resource" + resourcePath(s.opts.Resource)
}

func resourcePath(resource string) string {
	u, err := url.Parse(resource)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Path, "/")
}

// Register adds the OAuth endpoints to mux.
func (s *Server) Register(mux *http.ServeMux) {
	resourceMeta := http.HandlerFunc(s.serveResourceMetadata)
	mux.Handle("GET /.well-known/oauth-protected-resource", resourceMeta)
	if path := resourcePath(s.opts.Resource); path != "" {
		mux.Handle("GET /.well-known/oauth-protected-resource"+path, resourceMeta)
	}
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.serveServerMetadata)
	mux.Handle("POST /register", withBodyDeadline(http.HandlerFunc(s.serveRegister)))
	protection := http.NewCrossOriginProtection()
	mux.Handle("GET /authorize", http.HandlerFunc(s.serveAuthorize))
	mux.Handle("POST /authorize", withBodyDeadline(protection.Handler(http.HandlerFunc(s.serveAuthorize))))
	mux.Handle("POST /token", withBodyDeadline(http.HandlerFunc(s.serveToken)))
}

// withBodyDeadline stops a client from holding a connection open with a slow body.
func withBodyDeadline(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyReadTimeout))
		h.ServeHTTP(w, r)
	})
}

// persist writes the state file. It runs without mu, so a slow disk does not delay
// token checks; saveMu keeps the writes in snapshot order.
func (s *Server) persist() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	data, err := json.MarshalIndent(s.state, "", "  ")
	s.mu.Unlock()
	if err != nil {
		return err
	}
	return writeState(s.opts.StatePath, data)
}

// lookupClient returns an approved client, or a registered one that waits for approval.
// The caller holds mu.
func (s *Server) lookupClient(id string, now time.Time) (*client, bool) {
	if c, ok := s.state.Clients[id]; ok {
		return c, true
	}
	c, ok := s.pending[id]
	if ok && now.Sub(c.CreatedAt) > pendingTTL {
		delete(s.pending, id)
		return nil, false
	}
	return c, ok
}

// Verify is an auth.TokenVerifier for the MCP endpoint.
func (s *Server) Verify(_ context.Context, bearer string, _ *http.Request) (*auth.TokenInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.state.Tokens[hashSecret(bearer)]
	if !ok || t.Kind != tokenKindAccess || !s.opts.Now().Before(t.ExpiresAt) {
		return nil, fmt.Errorf("%w: unknown or expired access token", auth.ErrInvalidToken)
	}
	return &auth.TokenInfo{Expiration: t.ExpiresAt, UserID: t.ClientID}, nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

func allowPublicCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

func (s *Server) serveResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	allowPublicCORS(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.opts.Resource,
		"authorization_servers":    []string{s.opts.Issuer},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "mbx (mailbox.org)",
	})
}

func (s *Server) serveServerMetadata(w http.ResponseWriter, _ *http.Request) {
	allowPublicCORS(w)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.opts.Issuer,
		"authorization_endpoint":                         s.opts.Issuer + "/authorize",
		"token_endpoint":                                 s.opts.Issuer + "/token",
		"registration_endpoint":                          s.opts.Issuer + "/register",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_post", "client_secret_basic"},
		"authorization_response_iss_parameter_supported": true,
	})
}

// redirectAllowed reports whether a client may register uri as a redirect URI: the
// hosted Claude and ChatGPT callbacks, loopback URIs for local clients (RFC 8252), and
// the operator's extra URIs.
func (s *Server) redirectAllowed(uri string) bool {
	if slices.Contains(defaultRedirectURIs, uri) || slices.Contains(s.opts.ExtraRedirectURIs, uri) {
		return true
	}
	u, err := url.Parse(uri)
	if err != nil || u.Fragment != "" || u.User != nil || u.Opaque != "" {
		return false
	}
	if rest, ok := strings.CutPrefix(uri, chatGPTConnectorPrefix); ok {
		return chatGPTConnectorID.MatchString(rest)
	}
	if u.Scheme != "http" {
		return false
	}
	switch host := u.Hostname(); host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

type registration struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
}

// serveRegister implements RFC 7591 dynamic client registration.
func (s *Server) serveRegister(w http.ResponseWriter, r *http.Request) {
	var req registration
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "the body must be a JSON object")
		return
	}
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "give 1 to 10 redirect_uris")
		return
	}
	for _, uri := range req.RedirectURIs {
		if !s.redirectAllowed(uri) {
			s.opts.Logger.Printf("mcp oauth: refused a client registration for redirect URI %q", uri)
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				"this server accepts only the Claude, ChatGPT and loopback redirect URIs, and URIs its owner added with --redirect-uri")
			return
		}
	}
	method := req.TokenEndpointAuthMethod
	switch method {
	case "":
		method = "client_secret_basic" // the RFC 7591 default
	case "none", "client_secret_post", "client_secret_basic":
	default:
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	for _, grant := range req.GrantTypes {
		if grant != "authorization_code" && grant != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant type "+grant)
			return
		}
	}
	for _, responseType := range req.ResponseTypes {
		if responseType != "code" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported response type "+responseType)
			return
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 100 {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "client_name must be valid text of at most 100 characters")
		return
	}

	now := s.opts.Now()
	c := &client{ID: randomToken(), Name: name, RedirectURIs: req.RedirectURIs, AuthMethod: method, CreatedAt: now}
	var secret string
	if method != "none" {
		secret = randomToken()
		c.SecretHash = hashSecret(secret)
	}

	s.mu.Lock()
	for id, p := range s.pending {
		if now.Sub(p.CreatedAt) > pendingTTL {
			delete(s.pending, id)
		}
	}
	if len(s.pending) >= maxPendingClients || !s.limiter.AllowN(now, 1) {
		s.mu.Unlock()
		s.opts.Logger.Printf("mcp oauth: refused a registration: too many registrations")
		w.Header().Set("Retry-After", "60")
		oauthError(w, http.StatusTooManyRequests, "temporarily_unavailable", "too many registrations; try again in a minute")
		return
	}
	s.pending[c.ID] = c
	s.mu.Unlock()
	s.opts.Logger.Printf("mcp oauth: registered client %q", name)

	response := map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        now.Unix(),
		"client_name":                name,
		"redirect_uris":              c.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": method,
	}
	if secret != "" {
		response["client_secret"] = secret
		response["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, response)
}

var codeChallengePattern = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// authorizeRequest is a validated authorization request.
type authorizeRequest struct {
	client      *client
	redirectURI string
	challenge   string
	state       string
	resource    string
}

// parseAuthorize validates an authorization request. pageErr is set when the client or
// redirect URI is wrong, so the error must not be sent to the redirect URI. redirectErr
// is an OAuth error code for the redirect URI.
func (s *Server) parseAuthorize(values url.Values) (req authorizeRequest, pageErr, redirectErr, description string) {
	s.mu.Lock()
	c, ok := s.lookupClient(values.Get("client_id"), s.opts.Now())
	s.mu.Unlock()
	if !ok {
		return req, "This application is not registered with this server, or its registration expired. Connect it again.", "", ""
	}
	req.client = c
	req.redirectURI = values.Get("redirect_uri")
	if req.redirectURI == "" && len(c.RedirectURIs) == 1 {
		req.redirectURI = c.RedirectURIs[0]
	}
	if !slices.Contains(c.RedirectURIs, req.redirectURI) {
		return req, "The redirect URI does not match the registered application.", "", ""
	}
	req.state = values.Get("state")
	req.resource = values.Get("resource")
	switch {
	case values.Get("response_type") != "code":
		return req, "", "unsupported_response_type", "response_type must be code"
	case values.Get("code_challenge_method") != "S256" || !codeChallengePattern.MatchString(values.Get("code_challenge")):
		return req, "", "invalid_request", "PKCE with code_challenge_method S256 is required"
	case req.resource != "" && !s.resourceMatches(req.resource):
		return req, "", "invalid_target", "unknown resource"
	}
	req.challenge = values.Get("code_challenge")
	return req, "", "", ""
}

func (s *Server) resourceMatches(resource string) bool {
	resource = strings.TrimSuffix(resource, "/")
	return resource == strings.TrimSuffix(s.opts.Resource, "/") || resource == s.opts.Issuer
}

func (s *Server) redirectTo(w http.ResponseWriter, r *http.Request, req authorizeRequest, params url.Values) {
	u, err := url.Parse(req.redirectURI)
	if err != nil {
		http.Error(w, "invalid redirect URI", http.StatusBadRequest)
		return
	}
	query := u.Query()
	for key, values := range params {
		query[key] = values
	}
	if req.state != "" {
		query.Set("state", req.state)
	}
	query.Set("iss", s.opts.Issuer) // RFC 9207
	u.RawQuery = query.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	setPageHeaders(w)
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		if err := r.ParseForm(); err != nil {
			renderError(w, http.StatusBadRequest, "The form could not be read.")
			return
		}
	}
	values := r.Form
	if r.Method == http.MethodGet {
		values = r.URL.Query()
	}
	req, pageErr, redirectErr, description := s.parseAuthorize(values)
	if pageErr != "" {
		renderError(w, http.StatusBadRequest, pageErr)
		return
	}
	if redirectErr != "" {
		s.redirectTo(w, r, req, url.Values{"error": {redirectErr}, "error_description": {description}})
		return
	}
	// The browser must be allowed to follow the redirect after the form submission.
	addFormActionOrigin(w, req.redirectURI)

	if r.Method == http.MethodGet {
		renderConsent(w, http.StatusOK, consentPage{Request: req, Values: values})
		return
	}
	if r.PostForm.Get("action") == "deny" {
		s.opts.Logger.Printf("mcp oauth: owner denied client %q", req.client.Name)
		s.redirectTo(w, r, req, url.Values{"error": {"access_denied"}, "error_description": {"the owner denied access"}})
		return
	}

	// A correct secret always works. With MinOwnerSecretLength, guessing is hopeless,
	// so wrong attempts are only slowed down; a lockout would let anyone lock out the owner.
	given := sha256.Sum256([]byte(r.PostForm.Get("owner_secret")))
	if subtle.ConstantTimeCompare(given[:], s.secretHash[:]) != 1 {
		s.opts.Logger.Printf("mcp oauth: wrong owner secret for client %q", req.client.Name)
		time.Sleep(wrongSecretDelay)
		renderConsent(w, http.StatusUnauthorized, consentPage{Request: req, Values: values, Error: "The secret is wrong."})
		return
	}
	now := s.opts.Now()
	code := randomToken()
	s.mu.Lock()
	c, ok := s.lookupClient(req.client.ID, now)
	if ok && c.ApprovedAt.IsZero() {
		c.ApprovedAt = now
		delete(s.pending, c.ID)
		s.state.Clients[c.ID] = c
	}
	if ok {
		s.codes[hashSecret(code)] = &authCode{
			ClientID: req.client.ID, RedirectURI: req.redirectURI, Challenge: req.challenge, ExpiresAt: now.Add(codeTTL),
		}
	}
	for hash, stored := range s.codes {
		if !now.Before(stored.ExpiresAt) {
			delete(s.codes, hash)
		}
	}
	s.mu.Unlock()
	if !ok {
		renderError(w, http.StatusBadRequest, "The registration of this application expired. Connect it again.")
		return
	}
	if err := s.persist(); err != nil {
		renderError(w, http.StatusInternalServerError, "The approval could not be saved.")
		return
	}
	s.opts.Logger.Printf("mcp oauth: owner approved client %q", req.client.Name)
	s.redirectTo(w, r, req, url.Values{"code": {code}})
}

// authenticateClient checks client credentials at the token endpoint: client_secret_basic,
// client_secret_post, or only client_id for public clients.
func (s *Server) authenticateClient(r *http.Request) (*client, bool) {
	id, secret, basic := r.BasicAuth()
	if basic {
		var err1, err2 error
		id, err1 = url.QueryUnescape(id)
		secret, err2 = url.QueryUnescape(secret)
		if err1 != nil || err2 != nil {
			return nil, false
		}
	} else {
		id = r.PostForm.Get("client_id")
		secret = r.PostForm.Get("client_secret")
	}
	if formID := r.PostForm.Get("client_id"); formID != "" && formID != id {
		return nil, false
	}
	// Only approved clients can hold a code or a refresh token.
	s.mu.Lock()
	c, ok := s.state.Clients[id]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	if c.SecretHash == "" {
		return c, secret == ""
	}
	given := hashSecret(secret)
	return c, subtle.ConstantTimeCompare([]byte(given), []byte(c.SecretHash)) == 1
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "the body must be form-encoded")
		return
	}
	c, ok := s.authenticateClient(r)
	if !ok {
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return
	}
	now := s.opts.Now()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		verifier := r.PostForm.Get("code_verifier")
		s.mu.Lock()
		hash := hashSecret(r.PostForm.Get("code"))
		code, found := s.codes[hash]
		delete(s.codes, hash) // a code works once, also when the exchange fails
		s.mu.Unlock()
		switch {
		case !found || !now.Before(code.ExpiresAt) || code.ClientID != c.ID:
			oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown, used or expired code")
			return
		case r.PostForm.Get("redirect_uri") != "" && r.PostForm.Get("redirect_uri") != code.RedirectURI:
			oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match")
			return
		case !codeChallengePattern.MatchString(verifier) || s256(verifier) != code.Challenge:
			oauthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match")
			return
		}
		s.issueTokens(w, c, now)
	case "refresh_token":
		s.mu.Lock()
		hash := hashSecret(r.PostForm.Get("refresh_token"))
		t, found := s.state.Tokens[hash]
		valid := found && t.Kind == tokenKindRefresh && t.ClientID == c.ID && now.Before(t.ExpiresAt)
		if valid {
			delete(s.state.Tokens, hash) // rotation: a refresh token works once
		}
		s.mu.Unlock()
		if !valid {
			oauthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired refresh token")
			return
		}
		s.issueTokens(w, c, now)
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "use authorization_code or refresh_token")
	}
}

// issueTokens stores a new access and refresh token for c.
func (s *Server) issueTokens(w http.ResponseWriter, c *client, now time.Time) {
	access, refresh := randomToken(), randomToken()
	s.mu.Lock()
	s.state.prune(now)
	s.state.Tokens[hashSecret(access)] = &token{Kind: tokenKindAccess, ClientID: c.ID, ExpiresAt: now.Add(s.opts.AccessTokenTTL)}
	s.state.Tokens[hashSecret(refresh)] = &token{Kind: tokenKindRefresh, ClientID: c.ID, ExpiresAt: now.Add(s.opts.RefreshTokenTTL)}
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		oauthError(w, http.StatusInternalServerError, "server_error", "could not save the token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(s.opts.AccessTokenTTL / time.Second),
		"refresh_token": refresh,
	})
}

func s256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// IsLoopbackURL reports whether u names a loopback host, which may be used without TLS
// for local tests.
func IsLoopbackURL(u *url.URL) bool {
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
