// ABOUTME: Regression tests for safety and output-contract defects found during code review.
// ABOUTME: Mutation guards assert that rejected invocations make no provider writes or local output files.
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestMailMoveMany_DeleteProtectionAndExecutionShareFrozenPlan(t *testing.T) {
	const (
		source      = "default0/INBOX"
		destination = "default0/Archive"
	)
	var movedTo string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.Query().Get("action") {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/folders?get":
			id := r.URL.Query().Get("id")
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": id, "title": "Destination", "module": "mail", "folder_id": "default0",
				"subfolders": false, "last_modified": nil, "permissions": []any{},
			}})
		case "/ajax/mail?list":
			var requested []map[string]string
			if err := json.Unmarshal([]byte(r.URL.Query().Get("data")), &requested); err != nil || len(requested) != 1 {
				t.Errorf("mail metadata request = %q, err=%v", r.URL.Query().Get("data"), err)
				writeJSONResponse(w, map[string]any{"data": []any{}})
				return
			}
			writeJSONResponse(w, map[string]any{"data": []any{safeMailRow(requested[0]["id"], requested[0]["folder"])}})
		case "/ajax/mail?update":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode move body: %v", err)
			}
			movedTo = body["folder_id"]
			writeJSONResponse(w, map[string]any{"data": map[string]any{"folder_id": movedTo, "id": "17"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	planPath := filepath.Join(root, "moves.json")
	checkpointPath := filepath.Join(root, "moves.jsonl")
	configPath := filepath.Join(root, "config.toml")
	initialPlan := `[{"folder":"` + source + `","id":"7","to":"` + destination + `"}]`
	replacementPlan := `[{"folder":"` + source + `","id":"7","to":"` + mailTrashDefaultFolder + `"}]`
	if err := os.WriteFile(planPath, []byte(initialPlan), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(protectionConfig("mail")), 0o600); err != nil {
		t.Fatal(err)
	}

	var replaceOnce sync.Once
	var replacementErr error
	getenv := func(key string) string {
		replaceOnce.Do(func() {
			replacementErr = os.WriteFile(planPath, []byte(replacementPlan), 0o600)
		})
		switch key {
		case "MBX_USERNAME":
			return "fixture-user@example.invalid"
		case "MBX_PASSWORD":
			return "pw"
		default:
			return ""
		}
	}
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, []string{
		"mail", "move-many", "--input", "@" + planPath, "--verify", "--checkpoint", checkpointPath, "--llm",
	}, srv.Client(), srv.URL+"/ajax", configPath, filepath.Join(root, "session.json"), getenv)
	if replacementErr != nil {
		t.Fatal(replacementErr)
	}
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if movedTo != destination {
		t.Fatalf("executed destination=%q, want frozen checked destination %q", movedTo, destination)
	}
	contents, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != replacementPlan {
		t.Fatalf("test did not replace plan file: %s", contents)
	}
}

func TestMailMove_RejectsExplicitBlankFingerprintBeforeNetwork(t *testing.T) {
	for name, fingerprint := range map[string]string{"empty": "", "whitespace": "  "} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, map[string]string{
				"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw",
			}, []string{
				"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Archive",
				"--expect-fingerprint", fingerprint, "--llm",
			})
			if code != 4 || stdout != "" || !strings.Contains(stderr, "expect-fingerprint") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("blank fingerprint made provider requests: %+v", srv.Requests())
			}
		})
	}
}

func TestWriteSelectorsAreValidatedBeforeMutation(t *testing.T) {
	tests := []struct {
		name       string
		args       func(string) []string
		localPaths bool
	}{
		{
			name: "unverified move fingerprint",
			args: func(string) []string {
				return []string{"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Archive", "--format", "json", "--select", "fingerprint"}
			},
		},
		{
			name: "folders create preview request",
			args: func(string) []string {
				return []string{"folders", "create", "--parent", "1", "--title", "Trace", "--module", "mail", "--format", "json", "--select", "request"}
			},
		},
		{
			name: "folders ensure preview request",
			args: func(string) []string {
				return []string{"folders", "ensure", "--parent", "1", "--title", "Trace", "--module", "mail", "--format", "json", "--select", "request"}
			},
		},
		{
			name:       "move-many item with output",
			localPaths: true,
			args: func(root string) []string {
				return []string{
					"mail", "move-many", "--input", `[{"folder":"default0/INBOX","id":"7","to":"default0/Archive"}]`,
					"--verify", "--checkpoint", filepath.Join(root, "checkpoint.jsonl"), "--output", filepath.Join(root, "result.json"),
					"--format", "json", "--select", "status",
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			root := t.TempDir()
			stdout, stderr, code := run(t, srv, map[string]string{
				"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw",
			}, test.args(root))
			if code != 4 || stdout != "" || !strings.Contains(stderr, "invalid_argument") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("invalid projection made provider requests: %+v", srv.Requests())
			}
			if test.localPaths {
				for _, name := range []string{"checkpoint.jsonl", "result.json"} {
					if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatalf("invalid projection created %s: %v", name, err)
					}
				}
			}
		})
	}
}

func TestFoldersEnsure_ConcurrentInvocationsCreateOnce(t *testing.T) {
	var mu sync.Mutex
	created, listCalls, createCalls := false, 0, 0
	newStarted := make(chan struct{}, 1)
	listObserved := make(chan int, 8)
	releaseCreate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseCreate) }) }
	defer release()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.Query().Get("action") {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/folders?list":
			mu.Lock()
			listCalls++
			call := listCalls
			exists := created
			mu.Unlock()
			listObserved <- call
			rows := []any{}
			if exists {
				rows = append(rows, []any{"default0/Trace", "Trace", "mail", 1, false})
			}
			writeJSONResponse(w, map[string]any{"data": rows})
		case "/ajax/folders?new":
			mu.Lock()
			createCalls++
			mu.Unlock()
			select {
			case newStarted <- struct{}{}:
			default:
			}
			<-releaseCreate
			mu.Lock()
			created = true
			mu.Unlock()
			writeJSONResponse(w, map[string]any{"data": "default0/Trace"})
		case "/ajax/folders?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": "default0/Trace", "title": "Trace", "module": "mail", "folder_id": "1",
				"subfolders": false, "last_modified": nil, "permissions": []any{},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	type invocationResult struct {
		stdout string
		stderr string
		code   int
	}
	root := t.TempDir()
	args := []string{"folders", "ensure", "--parent", "1", "--title", "Trace", "--module", "mail", "--llm"}
	results := make(chan invocationResult, 2)
	runEnsure := func() {
		stdout, stderr, code := runAgainstHTTPServer(t, srv, root, args)
		results <- invocationResult{stdout: stdout, stderr: stderr, code: code}
	}
	go runEnsure()
	select {
	case <-newStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first folders ensure did not reach create")
	}
	go runEnsure()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		calls := listCalls
		mu.Unlock()
		if calls >= 3 {
			break
		}
		select {
		case <-listObserved:
		case <-deadline:
			release()
			t.Fatal("second folders ensure did not finish its parent validation")
		}
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	prematureLists := listCalls
	mu.Unlock()
	release()

	for i := 0; i < 2; i++ {
		result := <-results
		if result.code != 0 {
			t.Fatalf("concurrent ensure exit=%d stdout=%s stderr=%s", result.code, result.stdout, result.stderr)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if prematureLists != 3 {
		t.Fatalf("second check/create lookup ran before the first create completed: list calls=%d, want 3", prematureLists)
	}
	if createCalls != 1 {
		t.Fatalf("folders new calls=%d, want exactly 1", createCalls)
	}
}

func TestMailMoveMany_OutputCannotOverwriteFileInput(t *testing.T) {
	for _, hardLink := range []bool{false, true} {
		name := "same path"
		if hardLink {
			name = "hard link"
		}
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			root := t.TempDir()
			planPath := filepath.Join(root, "moves.json")
			original := []byte(`[{"folder":"a","id":"1","to":"b"}]`)
			if err := os.WriteFile(planPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			outputPath := planPath
			if hardLink {
				outputPath = filepath.Join(root, "result.json")
				if err := os.Link(planPath, outputPath); err != nil {
					t.Fatal(err)
				}
			}
			stdout, stderr, code := run(t, srv, map[string]string{}, []string{
				"mail", "move-many", "--input", "@" + planPath, "--dry-run", "--output", outputPath, "--llm",
			})
			if code != 4 || stdout != "" || !strings.Contains(stderr, "input") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			contents, err := os.ReadFile(planPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(contents, original) {
				t.Fatalf("input plan was overwritten: %s", contents)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("colliding paths made provider requests: %+v", srv.Requests())
			}
		})
	}
}

func TestMailMoveMany_OutputCannotAliasCheckpoint(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	root := t.TempDir()
	checkpointPath := filepath.Join(root, "moves.jsonl")
	outputPath := filepath.Join(root, "result.json")
	original := []byte("checkpoint evidence\n")
	if err := os.WriteFile(checkpointPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(checkpointPath, outputPath); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"mail", "move-many", "--input", `[{"folder":"a","id":"1","to":"b"}]`,
		"--dry-run", "--checkpoint", checkpointPath, "--output", outputPath, "--llm",
	})
	if code != 4 || stdout != "" || !strings.Contains(stderr, "checkpoint") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	contents, err := os.ReadFile(checkpointPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, original) {
		t.Fatalf("checkpoint was overwritten: %s", contents)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("colliding paths made provider requests: %+v", srv.Requests())
	}
}

func TestMailMoveMany_ItemSelectionWithoutOutputStillWorks(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"mail", "move-many", "--input", `[{"folder":"a","id":"1","to":"b"}]`,
		"--dry-run", "--format", "json", "--select", "status",
	})
	if code != 0 || stderr != "" || !strings.Contains(stdout, `"status":"planned"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("dry-run selection made provider requests: %+v", srv.Requests())
	}
}

func TestMailList_PreservesExplicitEmptyFields(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{Body: map[string]any{"data": []any{
		[]any{"7", "subject", []any{[]any{"Sender", "sender@example.org"}}, int64(1700000000000), []any{}, []any{}, nil},
	}}})
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	args := []string{"mail", "list", "--fields", "to,cc,sent_date"}

	stdout, stderr, code := run(t, srv, env, append(append([]string{}, args...), "--llm"))
	if code != 0 {
		t.Fatalf("full output exit=%d stderr=%s", code, stderr)
	}
	assertExplicitEmptyMailFields(t, stdout)

	stdout, stderr, code = run(t, srv, env, append(append([]string{}, args...), "--format", "json", "--select", "to,cc,sent_date"))
	if code != 0 {
		t.Fatalf("selected output exit=%d stderr=%s", code, stderr)
	}
	assertExplicitEmptyMailFields(t, stdout)
}

func assertExplicitEmptyMailFields(t *testing.T, output string) {
	t.Helper()
	var document struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil || len(document.Items) != 1 {
		t.Fatalf("mail output=%q decode=%v", output, err)
	}
	for _, field := range []string{"to", "cc"} {
		if string(document.Items[0][field]) != "[]" {
			t.Fatalf("%s=%s, want [] in %s", field, document.Items[0][field], output)
		}
	}
	if string(document.Items[0]["sent_date"]) != "null" {
		t.Fatalf("sent_date=%s, want null in %s", document.Items[0]["sent_date"], output)
	}
}

func TestBatchRun_GlobalSelectionUsesResultRowFields(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{Body: []any{
		map[string]any{"data": map[string]any{"email1": "fixture-user@example.invalid", "user_id": 42}},
	}})
	stdout, stderr, code := run(t, srv, map[string]string{
		"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw",
	}, []string{
		"batch", "run", "--input", `[{"operation":"whoami"}]`, "--format", "jsonl", "--select", "operation,result",
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &row); err != nil {
		t.Fatalf("output=%q: %v", stdout, err)
	}
	if string(row["operation"]) != `"whoami"` || row["result"] == nil {
		t.Fatalf("selected batch row=%s", stdout)
	}
}

func TestBatchRun_NormalizesPerRequestSelectors(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{Body: []any{
		map[string]any{"data": map[string]any{"email1": "fixture-user@example.invalid", "user_id": 42}},
	}})
	stdout, stderr, code := run(t, srv, map[string]string{
		"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw",
	}, []string{
		"batch", "run", "--input", `[{"operation":"whoami","select":[" email "]}]`, "--llm",
	})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, `"result":{"email":"fixture-user@example.invalid"}`) {
		t.Fatalf("normalized selector result missing: %s", stdout)
	}
}

func TestMailList_RejectsBlankExplicitSenderBeforeNetwork(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	stdout, stderr, code := run(t, srv, map[string]string{
		"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw",
	}, []string{"mail", "list", "--all", "--from", " \t ", "--llm"})
	if code != 4 || stdout != "" || !strings.Contains(stderr, "--from must not be blank") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("blank sender scanned mail: %+v", srv.Requests())
	}
}
