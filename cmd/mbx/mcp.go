// ABOUTME: "mbx mcp" entry point: serves the mbx MCP server to Claude, ChatGPT and other clients.
// ABOUTME: Parses the mcp flags, decides read-only mode and serves stdio, or HTTP behind OAuth or Cloudflare Access.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/cfaccess"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/cli"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/config"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/mcpauth"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ownerSecretEnv names the variable with the secret that approves remote MCP clients.
const ownerSecretEnv = "MBX_MCP_OWNER_SECRET"

// HTTP authentication modes: the built-in OAuth server, or Cloudflare Access in front of
// the server, which does the OAuth and signs each request it lets through.
const (
	authOAuth            = "oauth"
	authCloudflareAccess = "cloudflare-access"
)

type mcpFlags struct {
	readOnly     bool
	httpAddr     string
	publicURL    string
	allowWrites  bool
	redirectURIs []string
	auth         string
	accessTeam   string
	accessAUD    string
	accessEmails []string
	taskTools    bool
}

type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func parseMCPFlags(args []string) (mcpFlags, error) {
	var f mcpFlags
	set := flag.NewFlagSet("mbx mcp", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.BoolVar(&f.readOnly, "read-only", false, "offer only discovery and read tools")
	set.BoolVar(&f.taskTools, "task-tools", true, "also offer typed tools for frequent tasks, such as mbx_mail_search")
	set.StringVar(&f.httpAddr, "http", "", "serve Streamable HTTP with OAuth on this address")
	set.StringVar(&f.publicURL, "public-url", "", "public HTTPS base URL of the HTTP server")
	set.BoolVar(&f.allowWrites, "allow-writes", false, "offer write and delete tools over HTTP")
	set.Var((*stringList)(&f.redirectURIs), "redirect-uri", "extra OAuth redirect URI to accept")
	set.StringVar(&f.auth, "auth", authOAuth, "HTTP authentication: oauth or cloudflare-access")
	set.StringVar(&f.accessTeam, "access-team-domain", "", "Cloudflare Access team domain")
	set.StringVar(&f.accessAUD, "access-aud", "", "audience (AUD) tag of the Cloudflare Access application")
	set.Var((*stringList)(&f.accessEmails), "access-email", "email address that Cloudflare Access may let through")
	if err := set.Parse(args); err != nil {
		return f, err
	}
	if set.NArg() > 0 {
		return f, fmt.Errorf("unexpected argument: %s", set.Arg(0))
	}
	accessFlags := f.accessTeam != "" || f.accessAUD != "" || len(f.accessEmails) > 0
	if f.httpAddr == "" {
		if f.publicURL != "" || f.allowWrites || len(f.redirectURIs) > 0 || f.auth != authOAuth || accessFlags {
			return f, errors.New("--public-url, --allow-writes, --redirect-uri, --auth and the --access flags need --http")
		}
		return f, nil
	}
	switch f.auth {
	case authOAuth:
		if accessFlags {
			return f, errors.New("the --access flags need --auth cloudflare-access")
		}
	case authCloudflareAccess:
		if len(f.redirectURIs) > 0 {
			return f, errors.New("--redirect-uri is for --auth oauth; Cloudflare Access registers the clients")
		}
		if f.accessTeam == "" || f.accessAUD == "" || len(f.accessEmails) == 0 {
			return f, errors.New("--auth cloudflare-access needs --access-team-domain, --access-aud and at least one --access-email")
		}
		team, err := cfaccess.NormalizeTeamDomain(f.accessTeam)
		if err != nil {
			return f, err
		}
		f.accessTeam = team
	default:
		return f, fmt.Errorf("--auth %q must be oauth or cloudflare-access", f.auth)
	}
	if f.publicURL == "" {
		return f, errors.New("--http needs --public-url, the HTTPS address that clients use")
	}
	if f.allowWrites && f.readOnly {
		return f, errors.New("--allow-writes and --read-only cannot be combined")
	}
	public, err := normalizePublicURL(f.publicURL)
	if err != nil {
		return f, err
	}
	f.publicURL = public
	for _, uri := range f.redirectURIs {
		u, err := url.Parse(uri)
		if err != nil || u.Host == "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && mcpauth.IsLoopbackURL(u))) {
			return f, fmt.Errorf("--redirect-uri %q must be an absolute https URL without a fragment", uri)
		}
	}
	return f, nil
}

// normalizePublicURL accepts an https origin, or an http origin on a loopback host for
// local tests, and returns it without a trailing slash.
func normalizePublicURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("--public-url %q must be a base URL such as https://mbx.example.org, without a path", raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && mcpauth.IsLoopbackURL(u)) {
		return "", fmt.Errorf("--public-url %q must use https; plain http is allowed only for localhost tests", raw)
	}
	// Clients compare the issuer and resource URLs as strings, so use the canonical form:
	// a lowercase host without the default port.
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port := u.Port(); port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
		host += ":" + port
	}
	return u.Scheme + "://" + host, nil
}

// runMCP serves MCP over stdin and stdout until the client disconnects or the process
// gets SIGINT or SIGTERM. Diagnostics go to stderr; stdout carries only protocol data.
func runMCP(args []string, paths config.Paths, httpClient *http.Client, getenv func(string) string, stdout, stderr io.Writer) int {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" || arg == "-help" {
			return cli.Run(stdout, stderr, []string{"help", "mcp"}, httpClient, baseURL, paths.ConfigFile, paths.SessionFile, getenv)
		}
	}
	flags, err := parseMCPFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "Error: mbx mcp: %v; run \"mbx help mcp\"\n", err)
		return 4
	}
	server, readOnly, err := newMCPServer(flags, paths, httpClient, getenv)
	if err != nil {
		fmt.Fprintln(stderr, "Error: mbx mcp:", err)
		return 3
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	mode := "read and write"
	if readOnly {
		mode = "read-only"
	}
	if flags.httpAddr != "" {
		return runMCPHTTP(ctx, flags, server, mode, paths, getenv, stderr)
	}
	fmt.Fprintf(stderr, "mbx mcp: serving MCP over stdio (%s)\n", mode)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		fmt.Fprintln(stderr, "Error: mbx mcp:", err)
		return 1
	}
	return 0
}

// runMCPHTTP serves MCP over Streamable HTTP at <public URL>/mcp until ctx ends, behind
// the built-in OAuth server or behind Cloudflare Access. A tunnel or reverse proxy
// provides TLS.
func runMCPHTTP(ctx context.Context, flags mcpFlags, server *mcp.Server, mode string, paths config.Paths, getenv func(string) string, stderr io.Writer) int {
	logger := log.New(stderr, "", log.LstdFlags)
	var handler http.Handler
	if flags.auth == authCloudflareAccess {
		verifier, err := cfaccess.New(cfaccess.Options{
			TeamDomain: flags.accessTeam,
			Audience:   flags.accessAUD,
			Emails:     flags.accessEmails,
			Logger:     logger,
		})
		if err != nil {
			fmt.Fprintln(stderr, "Error: mbx mcp:", err)
			return 3
		}
		handler = newMCPAccessHandler(server, verifier)
		mode += ", Cloudflare Access"
	} else {
		authServer, err := mcpauth.New(mcpauth.Options{
			Issuer:            flags.publicURL,
			Resource:          flags.publicURL + "/mcp",
			OwnerSecret:       getenv(ownerSecretEnv),
			StatePath:         filepath.Join(paths.Root, "mcp-oauth.json"),
			ExtraRedirectURIs: flags.redirectURIs,
			Logger:            logger,
		})
		if err != nil {
			fmt.Fprintf(stderr, "Error: mbx mcp: %v; set %s to a long random value\n", err, ownerSecretEnv)
			return 3
		}
		handler = newMCPHTTPHandler(server, authServer)
	}
	listener, err := net.Listen("tcp", flags.httpAddr)
	if err != nil {
		fmt.Fprintln(stderr, "Error: mbx mcp:", err)
		return 1
	}
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}
	if addr, ok := listener.Addr().(*net.TCPAddr); ok && !addr.IP.IsLoopback() {
		fmt.Fprintf(stderr, "mbx mcp: warning: listening on %s, not only on loopback; put TLS in front and prefer 127.0.0.1\n", listener.Addr())
	}
	fmt.Fprintf(stderr, "mbx mcp: serving MCP at %s/mcp, listening on %s (%s)\n", flags.publicURL, listener.Addr(), mode)

	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()
	select {
	case err := <-served:
		fmt.Fprintln(stderr, "Error: mbx mcp:", err)
		return 1
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	return 0
}

// newMCPHTTPHandler routes the OAuth endpoints and the bearer-protected MCP endpoint.
func newMCPHTTPHandler(server *mcp.Server, authServer *mcpauth.Server) http.Handler {
	mux := http.NewServeMux()
	authServer.Register(mux)
	mux.Handle("/mcp", auth.RequireBearerToken(authServer.Verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: authServer.ResourceMetadataURL(),
	})(newStreamableHandler(server)))
	return mux
}

// newMCPAccessHandler serves only the MCP endpoint, and only for requests with a valid
// Cloudflare Access assertion. Access serves the OAuth endpoints itself.
func newMCPAccessHandler(server *mcp.Server, verifier *cfaccess.Verifier) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", newStreamableHandler(server))
	return verifier.Middleware(mux)
}

func newStreamableHandler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		// A tunnel or proxy on the same machine forwards requests from a loopback address
		// with the public Host header, which this check would refuse. Every request needs
		// an OAuth access token or an Access assertion instead.
		DisableLocalhostProtection: true,
		SessionTimeout:             30 * time.Minute,
	})
}

// newMCPServer builds the server for the configured account. Read-only mode comes from
// the --read-only flag, safety.read_only in config.toml or MBX_READ_ONLY. Over HTTP it is
// also on unless --allow-writes is given.
func newMCPServer(flags mcpFlags, paths config.Paths, httpClient *http.Client, getenv func(string) string) (*mcp.Server, bool, error) {
	settings, err := config.Load(paths.ConfigFile)
	if err != nil {
		return nil, false, err
	}
	readOnly, err := cli.ReadOnlyEnabled(settings.Safety.ReadOnly, getenv)
	if err != nil {
		return nil, false, err
	}
	readOnly = readOnly || flags.readOnly || (flags.httpAddr != "" && !flags.allowWrites)
	var allowedWrites map[string]bool
	if names := settings.MCP.AllowedWrites; names != nil {
		if err := mcpserver.ValidateAllowedWrites(*names); err != nil {
			return nil, false, fmt.Errorf("%s: %w", paths.ConfigFile, err)
		}
		allowedWrites = map[string]bool{}
		for _, name := range *names {
			allowedWrites[strings.TrimSpace(name)] = true
		}
	}
	server := mcpserver.New(mcpserver.Options{
		Run: func(stdout, stderr io.Writer, args []string, getenv func(string) string) int {
			return cli.Run(stdout, stderr, args, httpClient, baseURL, paths.ConfigFile, paths.SessionFile, getenv)
		},
		Getenv:        getenv,
		ReadOnly:      readOnly,
		AllowedWrites: allowedWrites,
		TaskTools:     flags.taskTools,
		Version:       cli.Version(),
	})
	return server, readOnly, nil
}
