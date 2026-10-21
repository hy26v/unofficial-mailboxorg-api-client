// ABOUTME: HTML pages of the OAuth approval step: the owner consent form and error pages.
// ABOUTME: Sets strict browser headers so the pages cannot be framed, cached or scripted.
package mcpauth

import (
	"html/template"
	"net/http"
	"net/url"
)

func setPageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
}

// addFormActionOrigin allows the browser to follow the redirect to the client after the
// form submission; Chrome applies form-action to such redirects.
func addFormActionOrigin(w http.ResponseWriter, redirectURI string) {
	u, err := url.Parse(redirectURI)
	if err != nil || u.Host == "" {
		return
	}
	origin := u.Scheme + "://" + u.Host
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' "+origin+"; frame-ancestors 'none'; base-uri 'none'")
}

type consentPage struct {
	Request authorizeRequest
	Values  url.Values
	Error   string
}

func (p consentPage) ClientName() string {
	if p.Request.client.Name != "" {
		return p.Request.client.Name
	}
	return "An unnamed application"
}

// ClientRef shows the start of the client id and its registration time, so the owner
// can tell this connection from one that someone else prepared.
func (p consentPage) ClientRef() string {
	id := p.Request.client.ID
	if len(id) > 8 {
		id = id[:8]
	}
	return id + ", registered " + p.Request.client.CreatedAt.UTC().Format("2006-01-02 15:04 UTC")
}

func (p consentPage) RedirectHost() string {
	u, err := url.Parse(p.Request.redirectURI)
	if err != nil {
		return p.Request.redirectURI
	}
	return u.Host
}

// Hidden repeats the authorization request parameters in the form.
func (p consentPage) Hidden() map[string]string {
	fields := map[string]string{}
	for _, key := range []string{"response_type", "client_id", "redirect_uri", "code_challenge", "code_challenge_method", "state", "resource", "scope"} {
		if value := p.Values.Get(key); value != "" {
			fields[key] = value
		}
	}
	fields["redirect_uri"] = p.Request.redirectURI
	return fields
}

const pageStyle = `body{font-family:system-ui,sans-serif;max-width:32rem;margin:3rem auto;padding:0 1rem;line-height:1.5}
.warn{background:#fff4e5;border:1px solid #f0b46a;padding:.75rem;border-radius:.5rem}
.error{color:#b00020;font-weight:600}input[type=password]{width:100%;padding:.5rem;margin:.5rem 0 1rem}
button{padding:.5rem 1rem;margin-right:.5rem}`

var consentTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Allow access to mbx</title><style>` + pageStyle + `</style></head><body>
<h1>Allow access to your mailbox?</h1>
<p><strong>{{.ClientName}}</strong> ({{.ClientRef}}) asks for access to the mailbox.org account of this mbx server. After approval it returns to <strong>{{.RedirectHost}}</strong>.</p>
<p class="warn">Approve only if you started this connection yourself just now. Do not approve a link that someone sent you. The application can then use every tool that this server offers.</p>
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
<form method="post" action="/authorize">
{{range $key, $value := .Hidden}}<input type="hidden" name="{{$key}}" value="{{$value}}">
{{end}}<label for="owner_secret">Owner secret (MBX_MCP_OWNER_SECRET)</label>
<input type="password" id="owner_secret" name="owner_secret" autocomplete="current-password" autofocus>
<button type="submit" name="action" value="approve">Approve</button>
<button type="submit" name="action" value="deny">Deny</button>
</form></body></html>
`))

var errorTemplate = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>mbx authorization error</title><style>` + pageStyle + `</style></head><body>
<h1>Authorization failed</h1><p class="error">{{.}}</p></body></html>
`))

func renderConsent(w http.ResponseWriter, status int, page consentPage) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = consentTemplate.Execute(w, page)
}

func renderError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = errorTemplate.Execute(w, message)
}
