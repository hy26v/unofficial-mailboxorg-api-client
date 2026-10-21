// ABOUTME: End-to-end tests for "mbx mcp --http": runs the real binary with OAuth over loopback.
// ABOUTME: Registers a client, approves it with the owner secret, then calls tools with the token.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const testOwnerSecret = "an owner secret for tests only, 32+ chars"

func TestParseMCPFlags_HTTP(t *testing.T) {
	f, err := parseMCPFlags([]string{"--http", "127.0.0.1:8808", "--public-url", "https://mbx.example.org/", "--redirect-uri", "https://app.example/cb"})
	if err != nil || f.publicURL != "https://mbx.example.org" || f.httpAddr != "127.0.0.1:8808" || !slices.Equal(f.redirectURIs, []string{"https://app.example/cb"}) {
		t.Fatalf("flags = %+v, %v", f, err)
	}
	for raw, want := range map[string]string{
		"https://MBX.Example.org:443/": "https://mbx.example.org",
		"https://mbx.example.org:8443": "https://mbx.example.org:8443",
		"http://LOCALHOST:80":          "http://localhost",
		"http://[::1]:9":               "http://[::1]:9",
	} {
		if got, err := normalizePublicURL(raw); err != nil || got != want {
			t.Errorf("normalizePublicURL(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	if f, err := parseMCPFlags([]string{"--http", ":0", "--public-url", "http://127.0.0.1:9", "--allow-writes"}); err != nil || !f.allowWrites {
		t.Errorf("loopback http public URL = %+v, %v", f, err)
	}
	for _, args := range [][]string{
		{"--http", "127.0.0.1:1"},
		{"--public-url", "https://mbx.example.org"},
		{"--allow-writes"},
		{"--redirect-uri", "https://app.example/cb"},
		{"--http", "127.0.0.1:1", "--public-url", "http://mbx.example.org"},
		{"--http", "127.0.0.1:1", "--public-url", "https://mbx.example.org/sub"},
		{"--http", "127.0.0.1:1", "--public-url", "https://mbx.example.org?x=1"},
		{"--http", "127.0.0.1:1", "--public-url", "https://mbx.example.org", "--allow-writes", "--read-only"},
		{"--http", "127.0.0.1:1", "--public-url", "https://mbx.example.org", "--redirect-uri", "http://app.example/cb"},
	} {
		if _, err := parseMCPFlags(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startHTTPBinary starts "mbx mcp --http" and waits until it serves. It returns the base URL.
func startHTTPBinary(t *testing.T, binary string, env []string, extra ...string) string {
	t.Helper()
	port := strconv.Itoa(freePort(t))
	base := "http://127.0.0.1:" + port
	args := append([]string{"mcp", "--http", "127.0.0.1:" + port, "--public-url", base}, extra...)
	cmd := exec.Command(binary, args...)
	cmd.Env = append([]string{"MBX_HOME=" + t.TempDir(), "PATH=" + os.Getenv("PATH")}, env...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
		}
	})
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "serving MCP at") {
				ready <- scanner.Text()
			}
		}
	}()
	select {
	case line := <-ready:
		if !strings.Contains(line, base+"/mcp") {
			t.Fatalf("start line = %q", line)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("mbx mcp --http did not start")
	}
	return base
}

// oauthAccessToken runs dynamic registration, owner approval and the code exchange
// the way a hosted client does, and returns the access token.
func oauthAccessToken(t *testing.T, base string) string {
	t.Helper()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	postJSON := func(path string, body any) map[string]any {
		data, _ := json.Marshal(body)
		resp, err := noRedirect.Post(base+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode/100 != 2 {
			t.Fatalf("%s = %d %v", path, resp.StatusCode, out)
		}
		return out
	}

	// Discovery from the 401 challenge, as Claude and ChatGPT do it.
	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	challenge := resp.Header.Get("WWW-Authenticate")
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(challenge, base+"/.well-known/oauth-protected-resource/mcp") {
		t.Fatalf("unauthenticated /mcp = %d %q", resp.StatusCode, challenge)
	}

	callback := "https://claude.ai/api/mcp/auth_callback"
	reg := postJSON("/register", map[string]any{"redirect_uris": []string{callback}, "client_name": "e2e", "token_endpoint_auth_method": "none"})
	clientID := reg["client_id"].(string)
	verifier := "e2e-verifier-" + strings.Repeat("x", 40)
	sum := sha256.Sum256([]byte(verifier))
	form := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {callback}, "state": {"s"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {base + "/mcp"}, "owner_secret": {testOwnerSecret}, "action": {"approve"},
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/authorize", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err = noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	location, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || location.Query().Get("code") == "" {
		t.Fatalf("approval = %d %s", resp.StatusCode, location)
	}

	resp, err = noRedirect.PostForm(base+"/token", url.Values{
		"grant_type": {"authorization_code"}, "code": {location.Query().Get("code")}, "redirect_uri": {callback},
		"client_id": {clientID}, "code_verifier": {verifier},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var tokens map[string]any
	json.NewDecoder(resp.Body).Decode(&tokens)
	access, _ := tokens["access_token"].(string)
	if access == "" {
		t.Fatalf("token response = %d %v", resp.StatusCode, tokens)
	}
	return access
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connectHTTP(t *testing.T, base, token string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e-http", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token}},
	}, nil)
	if err != nil {
		t.Fatalf("connect over HTTP: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func TestMCPBinary_HTTPWithOAuth(t *testing.T) {
	binary := mbxBinary(t)

	t.Run("read-only by default", func(t *testing.T) {
		base := startHTTPBinary(t, binary, []string{"MBX_MCP_OWNER_SECRET=" + testOwnerSecret})
		session := connectHTTP(t, base, oauthAccessToken(t, base))
		if got := toolNames(t, session); !slices.Equal(got, readOnlyTools) {
			t.Errorf("tools = %v", got)
		}
		text, isError := callText(t, session, "mbx_describe", map[string]any{"operation": "mail.search"})
		if isError || !strings.Contains(text, `"name":"mail.search"`) {
			t.Errorf("describe = %s", text)
		}
	})

	t.Run("allow writes", func(t *testing.T) {
		base := startHTTPBinary(t, binary, []string{"MBX_MCP_OWNER_SECRET=" + testOwnerSecret}, "--allow-writes")
		session := connectHTTP(t, base, oauthAccessToken(t, base))
		if got := toolNames(t, session); !slices.Equal(got, fullTools) {
			t.Errorf("tools = %v", got)
		}
	})

	t.Run("wrong token", func(t *testing.T) {
		base := startHTTPBinary(t, binary, []string{"MBX_MCP_OWNER_SECRET=" + testOwnerSecret})
		client := mcp.NewClient(&mcp.Implementation{Name: "e2e-http", Version: "v0"}, nil)
		_, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{"forged"}}, MaxRetries: -1,
		}, nil)
		if err == nil {
			t.Fatal("connected with a forged token")
		}
	})
}

func TestMCPBinary_HTTPNeedsOwnerSecret(t *testing.T) {
	binary := mbxBinary(t)
	cmd := exec.Command(binary, "mcp", "--http", "127.0.0.1:0", "--public-url", "http://127.0.0.1:1")
	cmd.Env = []string{"MBX_HOME=" + t.TempDir(), "MBX_MCP_OWNER_SECRET=short"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 3 || !strings.Contains(stderr.String(), "MBX_MCP_OWNER_SECRET") {
		t.Errorf("short owner secret = %v, %q", err, stderr.String())
	}
}

// TestMCPBinary_HTTPWithSDKOAuthClient connects with the SDK's own OAuth client, an
// independent implementation of discovery, registration, PKCE and the RFC 9207 check.
// The fetcher stands in for the browser: it opens the consent page and approves it.
func TestMCPBinary_HTTPWithSDKOAuthClient(t *testing.T) {
	binary := mbxBinary(t)
	base := startHTTPBinary(t, binary, []string{"MBX_MCP_OWNER_SECRET=" + testOwnerSecret})
	browser := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	consentShown := false

	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{"http://127.0.0.1:1/callback"},
			ClientName:   "sdk e2e",
			GrantTypes:   []string{"authorization_code", "refresh_token"},
		}},
		AuthorizationCodeFetcher: func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			page, err := browser.Get(args.URL)
			if err != nil {
				return nil, err
			}
			body, _ := io.ReadAll(page.Body)
			page.Body.Close()
			consentShown = page.StatusCode == http.StatusOK && strings.Contains(string(body), "sdk e2e")
			authorizeURL, err := url.Parse(args.URL)
			if err != nil {
				return nil, err
			}
			form := authorizeURL.Query()
			form.Set("owner_secret", testOwnerSecret)
			form.Set("action", "approve")
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/authorize", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			resp, err := browser.Do(req)
			if err != nil {
				return nil, err
			}
			resp.Body.Close()
			location, err := url.Parse(resp.Header.Get("Location"))
			if err != nil {
				return nil, err
			}
			q := location.Query()
			return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "sdk-oauth", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: base + "/mcp", OAuthHandler: handler}, nil)
	if err != nil {
		t.Fatalf("connect with SDK OAuth: %v", err)
	}
	defer session.Close()
	if !consentShown {
		t.Error("the consent page was not shown with the client name")
	}
	if got := toolNames(t, session); !slices.Equal(got, readOnlyTools) {
		t.Errorf("tools = %v", got)
	}
}

func TestParseMCPFlags_CloudflareAccess(t *testing.T) {
	access := []string{"--http", "0.0.0.0:8808", "--public-url", "https://mbx.example.org", "--auth", "cloudflare-access",
		"--access-team-domain", "Team.cloudflareaccess.com", "--access-aud", "aud-1", "--access-email", "a@example.org"}
	f, err := parseMCPFlags(access)
	if err != nil || f.auth != authCloudflareAccess || f.accessTeam != "https://team.cloudflareaccess.com" || f.accessAUD != "aud-1" || !slices.Equal(f.accessEmails, []string{"a@example.org"}) {
		t.Fatalf("flags = %+v, %v", f, err)
	}
	for _, args := range [][]string{
		{"--auth", "cloudflare-access"},
		{"--access-aud", "aud-1"},
		{"--http", "127.0.0.1:1", "--public-url", "https://mbx.example.org", "--auth", "basic"},
		{"--http", "127.0.0.1:1", "--public-url", "https://mbx.example.org", "--access-aud", "aud-1"},
		{"--http", "127.0.0.1:1", "--public-url", "https://mbx.example.org", "--auth", "cloudflare-access", "--access-aud", "aud-1", "--access-email", "a@example.org"},
		{"--http", "127.0.0.1:1", "--public-url", "https://mbx.example.org", "--auth", "cloudflare-access", "--access-team-domain", "t.cloudflareaccess.com", "--access-aud", "aud-1"},
		append(append([]string{}, access...), "--redirect-uri", "https://app.example/cb"),
	} {
		if _, err := parseMCPFlags(args); err == nil {
			t.Errorf("%q accepted", args)
		}
	}
}

// TestMCPBinary_HTTPWithCloudflareAccess needs no owner secret and refuses every request
// without an Access assertion, including the OAuth endpoints of the other mode.
func TestMCPBinary_HTTPWithCloudflareAccess(t *testing.T) {
	binary := mbxBinary(t)
	base := startHTTPBinary(t, binary, nil, "--auth", "cloudflare-access",
		"--access-team-domain", "example.cloudflareaccess.com", "--access-aud", "aud-1", "--access-email", "a@example.org")
	for _, path := range []string{"/mcp", "/.well-known/oauth-protected-resource", "/register"} {
		resp, err := http.Post(base+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s = %d, want 403", path, resp.StatusCode)
		}
	}
}
