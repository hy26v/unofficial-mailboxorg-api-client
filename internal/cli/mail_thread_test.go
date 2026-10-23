// ABOUTME: CLI tests for "mail thread": subject normalization, the search across folders with
// ABOUTME: Sent, the oldest-first order, the start message outside the searched folders, and limits.
package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestThreadSubject_StripsReplyAndForwardPrefixes(t *testing.T) {
	for subject, want := range map[string]string{
		"Re: PROJECT ATLAS - Pedido (#4711)": "PROJECT ATLAS - Pedido (#4711)",
		"AW: WG: Re[2]:  Angebot   2026":     "Angebot 2026",
		"[team] Fwd: RE: Plan":               "Plan",
		"Reunión mañana":                     "Reunión mañana",
		"Re:":                                "",
		"Récord de ventas":                   "Récord de ventas",
		"TR : Facture":                       "Facture",
	} {
		if got := threadSubject(subject); got != want {
			t.Errorf("threadSubject(%q) = %q, want %q", subject, got, want)
		}
	}
}

func runMailThreadDocument(t *testing.T, args ...string) (mailThreadDocument, *threadRequests) {
	t.Helper()
	srv := newScopeServer(t)
	t.Cleanup(srv.Close)
	stdout, stderr, code := run(t, srv, mailSearchEnv, append([]string{"mail", "thread", "--llm"}, args...))
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr)
	}
	var document mailThreadDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout = %s: %v", stdout, err)
	}
	var searches []string
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") == "search" {
			body, _ := io.ReadAll(req.Body)
			searches = append(searches, req.URL.Query().Get("folder")+" "+string(body))
		}
	}
	return document, &threadRequests{searches: searches}
}

type threadRequests struct{ searches []string }

func TestMailThread_ListsTheConversationAcrossFoldersOldestFirst(t *testing.T) {
	document, requests := runMailThreadDocument(t, "--id", "1", "--folder", "default0/INBOX/Kit")
	if document.Subject != "Project Atlas" || document.Basis != "subject" || itemRefsOf(document.Items) != "default0/Sent#1 default0/INBOX/Kit#1" || document.HasMore {
		t.Errorf("document = %+v, want the sent reply and then the received one", document)
	}
	if len(document.Items[0].To) != 1 || document.Items[0].To[0].Address != "support@supplier.example" {
		t.Errorf("sent item = %+v, want its recipient", document.Items[0])
	}
	if len(requests.searches) != 4 || !strings.Contains(requests.searches[0], `{"filter":["=",{"field":"subject"},"Project Atlas"]}`) {
		t.Errorf("searches = %v, want a subject search in each folder of folder all", requests.searches)
	}
}

func TestMailThread_KeepsTheStartMessageAndTheNewestWithinTheLimit(t *testing.T) {
	document, _ := runMailThreadDocument(t, "--id", "4", "--folder", "default0/Trash/Old")
	if itemRefsOf(document.Items) != "default0/Trash/Old#4" {
		t.Errorf("items = %s, want the start message from Trash", itemRefsOf(document.Items))
	}
	document, _ = runMailThreadDocument(t, "--id", "1", "--folder", "default0/INBOX/Kit", "--limit", "1", "--snippets")
	if itemRefsOf(document.Items) != "default0/INBOX/Kit#1" || !document.HasMore || document.Available != 2 ||
		document.Items[0].Snippet == nil || *document.Items[0].Snippet != "Second" {
		t.Errorf("document = %+v, want the newest message with its snippet", document)
	}
}

func itemRefsOf(items []mailListItem) string {
	var refs []string
	for _, item := range items {
		refs = append(refs, item.Folder+"#"+item.ID)
	}
	return strings.Join(refs, " ")
}
