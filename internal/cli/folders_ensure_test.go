// ABOUTME: End-to-end offline tests for normalized folder plans and idempotent folders.ensure.
// ABOUTME: Also covers ensure verification failures, which must name the folder that was created.
package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func runAgainstHTTPServer(t *testing.T, srv *httptest.Server, root string, args []string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	env := func(key string) string {
		switch key {
		case "MBX_USERNAME":
			return "fixture-user@example.invalid"
		case "MBX_PASSWORD":
			return "pw"
		default:
			return ""
		}
	}
	code := Run(&stdout, &stderr, args, srv.Client(), srv.URL+"/ajax", filepath.Join(root, "config.toml"), filepath.Join(root, "session.json"), env)
	return stdout.String(), stderr.String(), code
}

func writeJSONResponse(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestFoldersCreate_DryRunUsesSameNormalizedRootPlan(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("dry-run unexpectedly requested %s", r.URL)
	}))
	defer srv.Close()
	stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{"folders", "create", "--parent", "1", "--title", "Trace", "--module", "mail", "--dry-run", "--llm"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{`"requested_parent":"1"`, `"parent":"default0"`, `"folder_id":"default0"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("preview=%s, want %s", stdout, want)
		}
	}
}

func TestFoldersEnsure_CreatesOnceThenReturnsExistingFolder(t *testing.T) {
	var mu sync.Mutex
	created := false
	createCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		switch r.URL.Path + "?" + action {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/folders?list":
			mu.Lock()
			exists := created
			mu.Unlock()
			rows := []any{}
			if exists {
				rows = append(rows, []any{"default0/Trace", "Trace", "mail", 1, false})
			}
			writeJSONResponse(w, map[string]any{"data": rows})
		case "/ajax/folders?new":
			if r.URL.Query().Get("folder_id") != "default0" {
				t.Errorf("folder_id=%q want default0", r.URL.Query().Get("folder_id"))
			}
			mu.Lock()
			created = true
			createCalls++
			mu.Unlock()
			writeJSONResponse(w, map[string]any{"data": "default0/Trace"})
		case "/ajax/folders?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": "default0/Trace", "title": "Trace", "module": "mail", "folder_id": "1", "subfolders": false,
				"last_modified": nil, "permissions": []any{},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	root := t.TempDir()
	args := []string{"folders", "ensure", "--parent", "1", "--title", "Trace", "--module", "mail", "--llm"}
	stdout, stderr, code := runAgainstHTTPServer(t, srv, root, args)
	if code != 0 || !strings.Contains(stdout, `"created":true`) {
		t.Fatalf("first exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	stdout, stderr, code = runAgainstHTTPServer(t, srv, root, args)
	if code != 0 || !strings.Contains(stdout, `"created":false`) || !strings.Contains(stdout, `"id":"default0/Trace"`) {
		t.Fatalf("second exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	if createCalls != 1 {
		t.Fatalf("folders new calls=%d want 1", createCalls)
	}
}

// TestFoldersEnsure_VerificationFailureNamesCreatedFolder is the regression test for an
// ensure whose create succeeded but whose read-back did not match: the error dropped the
// new folder id, so the caller could not inspect or remove what was created.
func TestFoldersEnsure_VerificationFailureNamesCreatedFolder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.Query().Get("action") {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/folders?list":
			writeJSONResponse(w, map[string]any{"data": []any{}})
		case "/ajax/folders?new":
			writeJSONResponse(w, map[string]any{"data": "default0/Trace"})
		case "/ajax/folders?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": "default0/Trace", "title": "Other", "module": "mail", "folder_id": "1", "subfolders": false,
				"last_modified": nil, "permissions": []any{},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	_, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{"folders", "ensure", "--parent", "1", "--title", "Trace", "--module", "mail", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want verification failure; stderr=%s", stderr)
	}
	if !strings.Contains(stderr, "verification_failed") || !strings.Contains(stderr, "default0/Trace") {
		t.Fatalf("stderr = %q, want verification_failed naming the created folder default0/Trace", stderr)
	}
}
