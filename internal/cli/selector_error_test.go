// ABOUTME: Machine-readable selector failures include repair metadata for agent callers.
package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestInvalidSelector_ReturnsAllowedFieldsAndSuggestion(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "list", "--select", "recieved_date", "--llm"})
	if code != 4 || stdout != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	var envelope struct {
		Error struct {
			AllowedFields []string `json:"allowed_fields"`
			Suggestion    string   `json:"suggestion"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatalf("decode stderr: %v", err)
	}
	if envelope.Error.Suggestion != "received_date" {
		t.Fatalf("suggestion=%q want received_date", envelope.Error.Suggestion)
	}
	if len(envelope.Error.AllowedFields) == 0 || !strings.Contains(stderr, `"received_date"`) {
		t.Fatalf("missing allowed_fields: %s", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("selector validation made %d requests", len(srv.Requests()))
	}
}
