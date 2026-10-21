// ABOUTME: CLI integration tests for the batch run command: local validation, --dry-run,
// ABOUTME: request translation, response passthrough, and human/LLM rendering.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func writeBatchInput(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "requests.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("could not write test input file: %v", err)
	}
	return path
}

func TestBatchRun_MissingInput_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"batch", "run", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--input") {
		t.Errorf("stderr = %q, want mention of --input", stderr)
	}
}

func TestBatchRun_EmptyArray_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	path := writeBatchInput(t, `[]`)

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"batch", "run", "--input", "@" + path, "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "empty") {
		t.Errorf("stderr = %q, want mention of empty batch", stderr)
	}
}

func TestBatchRun_RejectsInputAboveBurstLimitWithoutNetwork(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	requests := make([]map[string]any, maxBatchSubrequests+1)
	for i := range requests {
		requests[i] = map[string]any{"resource": "user", "action": "get"}
	}
	encoded, err := json.Marshal(requests)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"batch", "run", "--input", string(encoded), "--llm",
	})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; stderr=%s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "maximum is 20") || !strings.Contains(stderr, "burst protection") {
		t.Errorf("stderr = %q, want batch burst-limit explanation", stderr)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Errorf("network requests = %d, want 0", got)
	}
}

func TestBatchRun_AllowsExactlyMaximumBurstSizeInDryRun(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	requests := make([]map[string]any, maxBatchSubrequests)
	for i := range requests {
		requests[i] = map[string]any{"resource": "user", "action": "get"}
	}
	encoded, err := json.Marshal(requests)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"batch", "run", "--input", string(encoded), "--dry-run", "--llm",
	})
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q, want dry-run success", code, stderr)
	}
	var result struct {
		DryRun   bool             `json:"dry_run"`
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !result.DryRun || len(result.Requests) != maxBatchSubrequests {
		t.Errorf("dry-run result = dry_run:%v requests:%d, want true/%d", result.DryRun, len(result.Requests), maxBatchSubrequests)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Errorf("network requests = %d, want 0", got)
	}
}

func TestBatchRun_SubRequestMissingResource_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	path := writeBatchInput(t, `[{"action":"filestore"}]`)

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"batch", "run", "--input", "@" + path, "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "resource") {
		t.Errorf("stderr = %q, want mention of resource", stderr)
	}
}

func TestBatchRun_SubRequestMissingAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	path := writeBatchInput(t, `[{"resource":"quota"}]`)

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"batch", "run", "--input", "@" + path, "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "action") {
		t.Errorf("stderr = %q, want mention of action", stderr)
	}
}

func TestBatchRun_InvalidJSON_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	path := writeBatchInput(t, `not json`)

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"batch", "run", "--input", "@" + path, "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--input") {
		t.Errorf("stderr = %q, want mention of --input", stderr)
	}
}

func TestBatchRun_DryRun_LLMMode_ShowsTranslatedRequestsWithoutSending(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	path := writeBatchInput(t, `[{"resource":"quota","action":"filestore"},{"resource":"user","action":"get"}]`)

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", "@" + path, "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if !strings.Contains(stdout, `"module":"quota"`) || !strings.Contains(stdout, `"module":"user"`) {
		t.Errorf("stdout = %q, want translated module names", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d HTTP requests, want 0 (no auth, no send)", len(srv.Requests()))
	}
}

func TestBatchRun_DryRun_MailSubRequestsAskForUTCDates(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	path := writeBatchInput(t, `[{"operation":"mail.get","params":{"id":"7","folder":"default0/INBOX"}},{"operation":"mail.list","params":{"folder":"default0/INBOX","limit":2}}]`)

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", "@" + path, "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if got := strings.Count(stdout, `"timezone":"UTC"`); got != 2 {
		t.Errorf("stdout has %d timezone=UTC sub-requests, want 2: %s", got, stdout)
	}
}

func TestBatchRun_Success_LLMMode_SendsTranslatedRequestsAndReturnsResults(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{
		Body: []byte(`[{"data":{"quota":1073741824,"use":52428800}},{"data":{"email1":"fixture-user@example.invalid","user_id":42}}]`),
	})
	path := writeBatchInput(t, `[{"resource":" quota ","action":" get ","params":{"type":"filestore"}},{"resource":"user","action":"get","params":{}}]`)

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", "@" + path, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	wantOutput := `{"results":[{"operation":"quota.get","result":{"quota":1073741824,"use":52428800}},{"operation":"whoami","result":{"email":"fixture-user@example.invalid","display_name":"","user_id":42}}]}` + "\n"
	if stdout != wantOutput {
		t.Errorf("stdout = %q, want %q", stdout, wantOutput)
	}

	var multipleRequest *http.Request
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/multiple" {
			multipleRequest = r
		}
	}
	if multipleRequest == nil {
		t.Fatal("no request made to /ajax/multiple")
	}
	if multipleRequest.Method != http.MethodPut {
		t.Errorf("multiple request method = %s, want %s", multipleRequest.Method, http.MethodPut)
	}
	if multipleRequest.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", multipleRequest.Header.Get("Content-Type"))
	}
	if multipleRequest.URL.Query().Has("action") {
		t.Errorf("action query = %q, want omitted", multipleRequest.URL.Query().Get("action"))
	}

	decoder := json.NewDecoder(multipleRequest.Body)
	decoder.UseNumber()
	var gotBody []map[string]any
	if err := decoder.Decode(&gotBody); err != nil {
		t.Fatalf("decode multiple request body: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("multiple request body has trailing data: %v", err)
	}
	wantBody := []map[string]any{
		{"module": "quota", "action": "filestore"},
		{"module": "user", "action": "get"},
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("multiple request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestBatchRun_RejectsInvalidCLIShapedQuotaRequestBeforeNetwork(t *testing.T) {
	for name, input := range map[string]string{
		"missing type": `[{"resource":"quota","action":"get"}]`,
		"wrong type":   `[{"resource":"quota","action":"get","params":{"type":"mail"}}]`,
		"extra param":  `[{"resource":"quota","action":"get","params":{"type":"filestore","id":1}}]`,
	} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, []string{"batch", "run", "--input", input, "--llm"})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid quota sub-request rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "quota.get") {
				t.Fatalf("stderr = %q, want quota.get validation error", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("invalid quota sub-request made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}

func TestBatchRun_RejectsWrongResultCount(t *testing.T) {
	for name, response := range map[string][]byte{
		"missing result": []byte(`[{"data":{"quota":100,"use":10}}]`),
		"extra result":   []byte(`[{"data":{"quota":100,"use":10}},{"data":{"user_id":42}},{"data":{}}]`),
	} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/multiple", "", testutil.Response{Body: response})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			input := `[{"resource":"quota","action":"get","params":{"type":"filestore"}},{"resource":"user","action":"get"}]`
			stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", input, "--llm"})

			if code == 0 {
				t.Fatalf("exit code = 0, want wrong result count rejected; stdout=%s", stdout)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty on failure", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Errorf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestBatchRun_ResultLabelsMatchNormalizedRequest(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{
		Body: []any{map[string]any{"data": map[string]any{"quota": 100, "use": 10}}},
	})
	path := writeBatchInput(t, `[{"resource":" quota ","action":" filestore "}]`)

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", "@" + path, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var sent []map[string]any
	for _, request := range srv.Requests() {
		if request.URL.Path != "/ajax/multiple" {
			continue
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read multiple request body: %v", err)
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatalf("decode multiple request body %q: %v", body, err)
		}
	}
	if len(sent) != 1 || sent[0]["module"] != "quota" || sent[0]["action"] != "filestore" {
		t.Fatalf("sent requests = %#v, want normalized module/action", sent)
	}

	var output struct {
		Results []struct {
			Operation string `json:"operation"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout, err)
	}
	if len(output.Results) != 1 {
		t.Fatalf("results = %#v, want one result", output.Results)
	}
	if output.Results[0].Operation != "quota.get" {
		t.Errorf("result identity = %q, want canonical quota.get", output.Results[0].Operation)
	}
}

func TestBatchRun_Success_HumanMode_Table(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{
		Body: []any{
			map[string]any{"data": map[string]any{"quota": 100, "use": 10}},
		},
	})
	path := writeBatchInput(t, `[{"resource":"quota","action":"filestore"}]`)

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", "@" + path})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "quota.get") || !strings.Contains(stdout, `{"quota":100,"use":10}`) {
		t.Errorf("stdout = %q, want typed operation/result present", stdout)
	}
}

func TestBatchRun_SubRequestError_LLMMode_ReturnsTypedPerRequestError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{
		Body: []any{
			map[string]any{"error": "boom", "code": "SVL-0021"},
		},
	})
	path := writeBatchInput(t, `[{"operation":"quota.get","params":{"type":"filestore"}}]`)

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", "@" + path, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (per-sub-request errors are data, not a CLI failure); stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"operation":"quota.get"`) || !strings.Contains(stdout, `"error":{"code":"SVL-0021","message":"boom"`) {
		t.Errorf("stdout = %q, want a typed sub-request error", stdout)
	}
}

func TestBatchRun_UndecodableSubResultIsAPerEntryError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{
		Body: []byte(`[{"data":{"quota":-1,"use":5}},{"data":{"email1":"fixture-user@example.invalid","user_id":42}},{"nodata":true}]`),
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	input := `[{"operation":"quota.get","params":{"type":"filestore"}},{"operation":"whoami"},{"operation":"whoami"}]`
	stdout, stderr, code := run(t, srv, env, []string{"batch", "run", "--input", input, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 like other per-entry errors; stderr=%s", code, stderr)
	}
	var document struct {
		Results []struct {
			Operation string          `json:"operation"`
			Result    json.RawMessage `json:"result"`
			Error     *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || len(document.Results) != 3 {
		t.Fatalf("stdout = %q (err %v), want three results", stdout, err)
	}
	if document.Results[0].Error == nil || document.Results[0].Error.Code != "invalid_response" {
		t.Errorf("results[0] = %+v, want an invalid_response entry error", document.Results[0])
	}
	if document.Results[1].Error != nil || !strings.Contains(string(document.Results[1].Result), "fixture-user@example.invalid") {
		t.Errorf("results[1] = %+v, want the good whoami result kept", document.Results[1])
	}
	if document.Results[2].Error == nil || document.Results[2].Error.Code != "invalid_response" {
		t.Errorf("results[2] = %+v, want an entry error for missing data", document.Results[2])
	}
}

func TestBatchRun_RejectsSelectOfUnrequestedMailListFieldBeforeNetwork(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"batch", "run", "--input", `[{"operation":"mail.list","select":["id","seen"]}]`, "--llm",
	})
	if code != 4 || stdout != "" || !strings.Contains(stderr, `add \"seen\" to \"fields\"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want invalid_argument naming the missing field", code, stdout, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("sent %d requests for an input error", len(srv.Requests()))
	}

	stdout, stderr, code = run(t, srv, env, []string{
		"batch", "run", "--input", `[{"operation":"mail.list","params":{"fields":"seen"},"select":["id","seen"]}]`, "--dry-run", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s, want select of a requested field accepted", code, stderr)
	}
}
