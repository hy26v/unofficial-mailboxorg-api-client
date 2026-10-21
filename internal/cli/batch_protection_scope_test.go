// ABOUTME: Tests that batch sub-requests addressed by an OX module and destructive action are
// ABOUTME: rejected at batch planning under any delete-protection scope, before any request.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// OX module names do not always match the CLI resource names delete_protection is written
// in: attachments lives on the "attachment" module and share on "share/management". An
// earlier batch run forwarded such {module, action} pairs, so scoped protection had to map
// them back. Batch planning now accepts registered reads only, so each of these is rejected
// before authentication whatever the policy scope.
func TestBatchRun_ScopedProtectionCoversModuleNamedSubRequests(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, tc := range []struct {
		name     string
		selector string
		input    string
	}{
		{
			name:     "attachments detach",
			selector: "attachments",
			input:    `[{"resource":"attachment","action":"detach","params":{"id":1,"module":7,"attached":5,"folder":"33"}}]`,
		},
		{
			name:     "share deleteLink",
			selector: "share",
			input:    `[{"resource":"share/management","action":"deleteLink","params":{"module":"infostore","folder":"36","item":"36/2"}}]`,
		},
		{
			name:     "files delete by module name",
			selector: "files",
			input:    `[{"resource":"infostore","action":"delete","params":{"id":"36/2"}}]`,
		},
		{
			name:     "events delete by module name",
			selector: "events",
			input:    `[{"resource":"chronos","action":"delete","params":{"id":"42"}}]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/multiple", "", testutil.Response{Body: []byte(`[{}]`)})

			stdout, stderr, code := runWithConfig(t, srv, env,
				[]string{"batch", "run", "--input", tc.input, "--llm"}, protectionConfig(tc.selector))

			if code != 4 || stdout != "" {
				t.Fatalf("exit code = %d stdout=%q with delete_protection = [%q], want 4", code, stdout, tc.selector)
			}
			if !strings.Contains(stderr, "invalid_argument") {
				t.Errorf("stderr = %q, want a planning rejection", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("%s sent %d requests despite delete_protection = [%q]", tc.name, len(srv.Requests()), tc.selector)
			}
		})
	}
}

// Scoping still scopes: protecting one resource must not block a destructive sub-request
// against a different one, or the setting would be indistinguishable from "all".
func TestBatchRun_RejectsRawMutationEvenWhenProtectionScopesAnotherResource(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{Body: []byte(`[{}]`)})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	input := `[{"resource":"attachment","action":"detach","params":{"id":1}}]`
	stdout, stderr, code := runWithConfig(t, srv, env,
		[]string{"batch", "run", "--input", input, "--llm"}, protectionConfig("mail"))

	if code != 4 {
		t.Fatalf("exit code = %d, want invalid_argument; stderr=%s", code, strings.TrimSpace(stderr))
	}
	if stdout != "" || !strings.Contains(stderr, "unknown operation") {
		t.Errorf("stdout=%q stderr=%q, want raw action rejected", stdout, stderr)
	}
	for _, request := range srv.Requests() {
		if request.URL.Path == "/ajax/multiple" {
			t.Fatal("raw mutation reached /multiple")
		}
	}
}
