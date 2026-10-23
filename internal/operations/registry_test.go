// ABOUTME: Tests for the operation registry: registering, looking up, and listing
// ABOUTME: operation metadata by canonical name.
package operations

import "testing"

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	op := Operation{
		Name:        "whoami",
		Resource:    "whoami",
		Action:      "get",
		Summary:     "Show the authenticated user",
		Method:      "GET",
		Endpoint:    "/user",
		SideEffect:  SideEffectRead,
		Destructive: false,
		Idempotent:  true,
	}

	r.Register(op)

	got, ok := r.Get("whoami")
	if !ok {
		t.Fatal("Get(\"whoami\") not found")
	}
	if got.Summary != "Show the authenticated user" {
		t.Errorf("Summary = %q", got.Summary)
	}
}

func TestRegistry_GetUnknownReturnsFalse(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Get("nope")
	if ok {
		t.Error("Get(\"nope\") should be false")
	}
}

func TestRegistry_ListIsSortedByName(t *testing.T) {
	r := NewRegistry()
	r.Register(Operation{Name: "users.list"})
	r.Register(Operation{Name: "users.get"})

	list := r.List()
	if len(list) != 2 || list[0].Name != "users.get" || list[1].Name != "users.list" {
		t.Errorf("List() = %+v, want sorted by name", list)
	}
}

func TestRegistry_DoesNotExposeMutableMetadata(t *testing.T) {
	r := NewRegistry()
	op := Operation{
		Name:   "files.get",
		Scopes: []string{"read"},
		Args: map[string]ArgSpec{
			"id": {Type: "string", Required: true},
		},
		Output: OutputSpec{Type: "object", Fields: []string{"id"}},
	}
	r.Register(op)

	op.Scopes[0] = "write"
	delete(op.Args, "id")
	op.Output.Fields[0] = "secret"

	got, ok := r.Get("files.get")
	if !ok {
		t.Fatal("Get(\"files.get\") not found")
	}
	if len(got.Scopes) != 1 || got.Scopes[0] != "read" {
		t.Fatalf("Scopes = %v, want [read]", got.Scopes)
	}
	if _, ok := got.Args["id"]; !ok {
		t.Fatalf("Args = %v, want id", got.Args)
	}
	if len(got.Output.Fields) != 1 || got.Output.Fields[0] != "id" {
		t.Fatalf("Output.Fields = %v, want [id]", got.Output.Fields)
	}

	got.Scopes[0] = "admin"
	delete(got.Args, "id")
	got.Output.Fields[0] = "password"
	listed := r.List()
	listed[0].Scopes[0] = "owner"
	listed[0].Args["extra"] = ArgSpec{Type: "boolean"}
	listed[0].Output.Fields[0] = "token"

	again, _ := r.Get("files.get")
	if len(again.Scopes) != 1 || again.Scopes[0] != "read" {
		t.Fatalf("Scopes after caller mutation = %v, want [read]", again.Scopes)
	}
	if len(again.Args) != 1 {
		t.Fatalf("Args after caller mutation = %v, want only id", again.Args)
	}
	if _, ok := again.Args["id"]; !ok {
		t.Fatalf("Args after caller mutation = %v, want id", again.Args)
	}
	if len(again.Output.Fields) != 1 || again.Output.Fields[0] != "id" {
		t.Fatalf("Output.Fields after caller mutation = %v, want [id]", again.Output.Fields)
	}
}

func TestArgSpecTakesValue(t *testing.T) {
	for typ, want := range map[string]bool{"bool": false, "boolean": false, "string": true, "int": true, "integer": true} {
		if got := (ArgSpec{Type: typ}).TakesValue(); got != want {
			t.Errorf("ArgSpec{Type: %q}.TakesValue() = %v, want %v", typ, got, want)
		}
	}
}

func TestSearch_RanksNameAndKeywordsAboveSummary(t *testing.T) {
	r := NewRegistry()
	r.Register(Operation{Name: "mail.senders", Resource: "mail", Action: "senders", Summary: "List addresses", Keywords: []string{"alias", "from"}, SideEffect: SideEffectRead})
	r.Register(Operation{Name: "mail.draft", Resource: "mail", Action: "draft", Summary: "Save a draft", SideEffect: SideEffectWrite,
		Args: map[string]ArgSpec{"from": {Type: "string", Description: "sender address"}}})
	r.Register(Operation{Name: "events.list", Resource: "events", Action: "list", Summary: "List calendar events", SideEffect: SideEffectRead})
	names := func(ops []Operation) []string {
		var out []string
		for _, op := range ops {
			out = append(out, op.Name)
		}
		return out
	}
	if got := names(r.Search("aliases", 5, nil)); len(got) != 1 || got[0] != "mail.senders" {
		t.Errorf("aliases = %v", got)
	}
	if got := names(r.Search("from", 5, nil)); len(got) != 2 || got[0] != "mail.senders" {
		t.Errorf("from = %v, want the keyword match first", got)
	}
	if got := names(r.Search("calendar draft", 5, nil)); len(got) != 0 {
		t.Errorf("every word must match: %v", got)
	}
	if got := names(r.Search("mail", 5, func(op Operation) bool { return op.SideEffect == SideEffectRead })); len(got) != 1 {
		t.Errorf("keep filter = %v", got)
	}
	if r.Search("  ", 5, nil) != nil {
		t.Error("empty query returned operations")
	}
}

func TestSearchSome_RanksByMatchedWords(t *testing.T) {
	r := NewRegistry()
	r.Register(Operation{Name: "mail.attachment", Resource: "mail", Action: "attachment", Summary: "Get one attachment", Keywords: []string{"pdf"}})
	r.Register(Operation{Name: "mail.list", Resource: "mail", Action: "list", Summary: "List mail"})
	r.Register(Operation{Name: "files.list", Resource: "files", Action: "list", Summary: "List files"})
	if got := r.Search("open the contract pdf", 5, nil); len(got) != 0 {
		t.Fatalf("Search = %v, want no full match", got)
	}
	got := r.SearchSome("open the contract pdf of mail", 5, nil)
	if len(got) != 2 || got[0].Name != "mail.attachment" || got[1].Name != "mail.list" {
		t.Errorf("SearchSome = %v, want mail.attachment (pdf, mail) and then mail.list (mail)", got)
	}
}
