// ABOUTME: Tests the Cloudflare Access HTTP handler of "mbx mcp" with a locally signed assertion.
// ABOUTME: A fake team key endpoint stands in for Cloudflare; an MCP client lists the tools.
package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/cfaccess"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type headerTransport struct{ name, value string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(h.name, h.value)
	return http.DefaultTransport.RoundTrip(r)
}

func TestMCPAccessHandler(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	team := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer team.Close()
	verifier, err := cfaccess.New(cfaccess.Options{TeamDomain: team.URL, Audience: "aud-1", Emails: []string{"owner@example.org"}, HTTPClient: team.Client()})
	if err != nil {
		t.Fatal(err)
	}
	sign := func(email string) string {
		h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1"})
		c, _ := json.Marshal(map[string]any{"iss": team.URL, "aud": []string{"aud-1"}, "email": email, "exp": time.Now().Add(time.Hour).Unix()})
		input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
		digest := sha256.Sum256([]byte(input))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		return input + "." + base64.RawURLEncoding.EncodeToString(sig)
	}

	server := mcpserver.New(mcpserver.Options{
		Run:           func(io.Writer, io.Writer, []string, func(string) string) int { return 0 },
		Getenv:        func(string) string { return "" },
		AllowedWrites: map[string]bool{"mail.draft": true},
	})
	origin := httptest.NewServer(newMCPAccessHandler(server, verifier))
	defer origin.Close()

	connect := func(assertion string) (*mcp.ClientSession, error) {
		client := mcp.NewClient(&mcp.Implementation{Name: "access-test", Version: "v0"}, nil)
		return client.Connect(context.Background(), &mcp.StreamableClientTransport{
			Endpoint:   origin.URL + "/mcp",
			HTTPClient: &http.Client{Transport: headerTransport{cfaccess.Header, assertion}},
			MaxRetries: -1,
		}, nil)
	}
	if _, err := connect(sign("intruder@example.org")); err == nil {
		t.Fatal("connected with another identity")
	}
	session, err := connect(sign("owner@example.org"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()
	if got := toolNames(t, session); !slices.Equal(got, []string{"mbx_commands", "mbx_describe", "mbx_read", "mbx_write"}) {
		t.Errorf("tools = %v", got)
	}
}
