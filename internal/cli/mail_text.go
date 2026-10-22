// ABOUTME: Plain-text cleanup for mail read bodies: link rendering, tracking-parameter removal
// ABOUTME: and detection of provider "text" parts that are display HTML with <br> and anchors.
package cli

import (
	"html"
	"regexp"
	"strings"
)

// mailLinkMode selects how mail read shows links in a text body.
type mailLinkMode string

const (
	// mailLinksClean shows "text (url)" for a link with text, with tracking parameters
	// removed from the URL, and leaves out links without text, such as logo images.
	mailLinksClean mailLinkMode = "clean"
	// mailLinksNone shows only the link text.
	mailLinksNone mailLinkMode = "none"
	// mailLinksFull shows "text (url)" for every link, with the URL unchanged.
	mailLinksFull mailLinkMode = "full"
)

func parseMailLinkMode(value string) (mailLinkMode, bool) {
	switch mode := mailLinkMode(strings.TrimSpace(value)); mode {
	case mailLinksClean, mailLinksNone, mailLinksFull:
		return mode, true
	}
	return "", false
}

// displayHTMLPattern finds the markup that the provider's "text" view puts into a
// text/plain part: it formats the text for display, with <br> for line breaks, anchors
// around URLs and entities for special characters. Real plain text seldom contains these
// tags, while entities alone are not enough evidence, because plain text often holds
// "<https://...>" forms that the HTML converter would drop as tags.
var displayHTMLPattern = regexp.MustCompile(`(?i)<br\s*/?>|<a\s[^>]*href\s*=`)

// looksLikeDisplayHTML reports whether a text/plain body part is display HTML.
func looksLikeDisplayHTML(content string) bool {
	return displayHTMLPattern.MatchString(content)
}

// mailTrackingParams are query parameters that only identify the recipient or the
// campaign for click tracking. Names are compared in lower case.
var mailTrackingParams = map[string]bool{
	"_hsenc": true, "_hsmi": true, "__hstc": true, "__hssc": true, "__hsfp": true, "hsctatracking": true,
	"mc_cid": true, "mc_eid": true, "mkt_tok": true, "fbclid": true, "gclid": true, "gbraid": true,
	"wbraid": true, "dclid": true, "msclkid": true, "yclid": true, "igshid": true, "_ke": true,
	"oly_anon_id": true, "oly_enc_id": true, "vero_id": true, "vero_conv": true, "wickedid": true,
	"rb_clickid": true, "s_cid": true, "sc_cid": true, "trk": true, "trkcampaign": true,
}

func isMailTrackingParam(name string) bool {
	name = strings.ToLower(name)
	return mailTrackingParams[name] || strings.HasPrefix(name, "utm_")
}

// cleanTrackingURL removes tracking parameters from the query of an http or https URL.
// Every other part, and the order and encoding of the other parameters, stays as it was.
func cleanTrackingURL(raw string) string {
	lower := strings.ToLower(raw)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return raw
	}
	queryStart := strings.IndexByte(raw, '?')
	if queryStart < 0 {
		return raw
	}
	base, query, fragment := raw[:queryStart], raw[queryStart+1:], ""
	if hash := strings.IndexByte(query, '#'); hash >= 0 {
		query, fragment = query[:hash], query[hash:]
	}
	var kept []string
	for _, part := range strings.Split(query, "&") {
		name := part
		if equals := strings.IndexByte(part, '='); equals >= 0 {
			name = part[:equals]
		}
		if part == "" || isMailTrackingParam(name) {
			continue
		}
		kept = append(kept, part)
	}
	if len(kept) == 0 {
		return base + fragment
	}
	return base + "?" + strings.Join(kept, "&") + fragment
}

// bareURLPattern finds http and https URLs in plain text.
var bareURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"'()\[\]{}]+`)

// cleanTextURLs removes tracking parameters from every URL in plain text. Punctuation
// that ends a sentence after a URL is not part of it.
func cleanTextURLs(text string) string {
	return bareURLPattern.ReplaceAllStringFunc(text, func(match string) string {
		trimmed := strings.TrimRight(match, ".,;:!?")
		return cleanTrackingURL(trimmed) + match[len(trimmed):]
	})
}

// htmlLinkSuffix is what the HTML converter writes after a link's text: " (url)", or
// nothing. text is the link text as written so far, with entities not yet decoded. The
// result is entity-escaped, because the converter decodes the whole text once at the end.
func htmlLinkSuffix(text, href string, mode mailLinkMode) string {
	if mode == mailLinksNone {
		return ""
	}
	href = strings.TrimSpace(html.UnescapeString(href))
	lower := strings.ToLower(href)
	if href == "" || strings.HasPrefix(lower, "#") || strings.HasPrefix(lower, "javascript:") {
		return ""
	}
	shown := strings.TrimSpace(html.UnescapeString(text))
	if sameLinkTarget(shown, href) {
		return ""
	}
	if mode == mailLinksClean {
		if shown == "" {
			return ""
		}
		href = cleanTrackingURL(href)
		// Link text that is itself a URL is cleaned later, like any URL in the text.
		shown = cleanTrackingURL(shown)
	}
	display := href
	if strings.HasPrefix(lower, "mailto:") {
		display = href[len("mailto:"):]
		if mode == mailLinksClean {
			if query := strings.IndexByte(display, '?'); query >= 0 {
				display = display[:query]
			}
		}
	}
	if sameLinkTarget(shown, display) {
		return ""
	}
	return " (" + html.EscapeString(display) + ")"
}

// sameLinkTarget reports whether the link text already shows the URL, so the URL would
// only repeat it.
func sameLinkTarget(shown, display string) bool {
	trim := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		for _, prefix := range []string{"https://", "http://", "mailto:"} {
			s = strings.TrimPrefix(s, prefix)
		}
		return strings.TrimSuffix(s, "/")
	}
	return trim(shown) == trim(display)
}

// htmlTagAttribute returns the value of one attribute of a start tag, given the tag text
// from "<" to ">", or "" when the tag does not have it. Names are compared without case.
func htmlTagAttribute(tag, name string) string {
	i := 1
	for i < len(tag) && tag[i] != ' ' && tag[i] != '\t' && tag[i] != '\n' && tag[i] != '\r' && tag[i] != '\f' && tag[i] != '>' && tag[i] != '/' {
		i++
	}
	isSpace := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }
	for i < len(tag) {
		for i < len(tag) && (isSpace(tag[i]) || tag[i] == '/') {
			i++
		}
		if i >= len(tag) || tag[i] == '>' {
			return ""
		}
		nameStart := i
		for i < len(tag) && !isSpace(tag[i]) && tag[i] != '=' && tag[i] != '>' && tag[i] != '/' {
			i++
		}
		attribute := tag[nameStart:i]
		for i < len(tag) && isSpace(tag[i]) {
			i++
		}
		value := ""
		if i < len(tag) && tag[i] == '=' {
			i++
			for i < len(tag) && isSpace(tag[i]) {
				i++
			}
			if i < len(tag) && (tag[i] == '"' || tag[i] == '\'') {
				quote := tag[i]
				end := strings.IndexByte(tag[i+1:], quote)
				if end < 0 {
					value, i = tag[i+1:], len(tag)
				} else {
					value, i = tag[i+1:i+1+end], i+2+end
				}
			} else {
				valueStart := i
				for i < len(tag) && !isSpace(tag[i]) && tag[i] != '>' {
					i++
				}
				value = tag[valueStart:i]
			}
		}
		if strings.EqualFold(attribute, name) {
			return value
		}
	}
	return ""
}
