// ABOUTME: Security regressions for typed batch execution, projection, and bounded private output.
// ABOUTME: These tests prove /multiple cannot bypass the safe mail metadata decoder.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestBatchRun_MailGetUsesSafeTypedAdapterAndProjection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{Body: []any{
		map[string]any{"data": []any{safeMailRow("7", "default0/INBOX")}},
	}})

	input := `[{"operation":"mail.get","params":{"id":"7","folder":"default0/INBOX"},"select":["id","subject","fingerprint"]}]`
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", input, "--llm"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "headers") || strings.Contains(stdout, "body") || strings.Contains(stdout, "recipient@example.net") {
		t.Fatalf("batch output leaked unselected/raw mail data: %s", stdout)
	}
	if !strings.Contains(stdout, `"operation":"mail.get"`) || !strings.Contains(stdout, `"fingerprint":"sha256:`) {
		t.Fatalf("typed projected result missing identity/fingerprint: %s", stdout)
	}

	var multipleBody []map[string]any
	for _, request := range srv.Requests() {
		if request.URL.Path != "/ajax/multiple" {
			continue
		}
		if err := json.NewDecoder(request.Body).Decode(&multipleBody); err != nil {
			t.Fatal(err)
		}
	}
	if len(multipleBody) != 1 || multipleBody[0]["action"] != "list" || multipleBody[0]["columns"] != mailGetColumns {
		t.Fatalf("mail.get batch request = %#v, want safe mail?action=list columns", multipleBody)
	}
}

func TestBatchRun_RejectsMutationAndUnknownRawActionBeforeNetwork(t *testing.T) {
	for name, input := range map[string]string{
		"registered mutation": `[{"operation":"mail.delete","params":{"id":"7","folder":"default0/INBOX","confirm":"7"}}]`,
		"unknown raw action":  `[{"resource":"mail","action":"headers","params":{"id":"7"}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, map[string]string{}, []string{"batch", "run", "--input", input, "--llm"})
			if code == 0 || stdout != "" || !strings.Contains(stderr, "invalid_argument") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("rejected batch made %d network requests", len(srv.Requests()))
			}
		})
	}
}

func TestBatchRun_LargeResultRequiresPrivateOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	longName := strings.Repeat("x", 400)
	results := make([]any, maxBatchSubrequests)
	requests := make([]map[string]any, maxBatchSubrequests)
	for i := range results {
		results[i] = map[string]any{"data": map[string]any{"email1": "fixture-user@example.invalid", "display_name": longName, "user_id": 42}}
		requests[i] = map[string]any{"operation": "whoami"}
	}
	srv.Handle("/ajax/multiple", "", testutil.Response{Body: results})
	input, _ := json.Marshal(requests)
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", string(input), "--max-output-bytes", "256", "--llm"})
	if code == 0 || stdout != "" || !strings.Contains(stderr, "output_too_large") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	output := filepath.Join(t.TempDir(), "batch.json")
	stdout, stderr, code = run(t, srv, env, []string{"batch", "run", "--input", string(input), "--max-output-bytes", "256", "--output", output, "--llm"})
	if code != 0 {
		t.Fatalf("private output exit=%d stderr=%s", code, stderr)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode=%o want 600", info.Mode().Perm())
	}
	if !strings.Contains(stdout, `"saved_to"`) || strings.Contains(stdout, longName) {
		t.Fatalf("stdout should contain only file summary: %s", stdout)
	}
}
