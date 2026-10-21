// ABOUTME: Offline end-to-end tests for verified move identity, durable checkpoints, and resume without replay.
// ABOUTME: Partial failures emit the partial result, and unverified moves are checkpointed for resume.
package cli

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMailMove_DryRunHasStableSourceDestinationIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("dry-run unexpectedly requested %s", r.URL)
	}))
	defer srv.Close()
	stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{
		"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Trace", "--dry-run", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		`"source":{"folder":"default0/INBOX","id":"7"}`,
		`"destination":{"folder":"default0/Trace","id":null}`,
		`"verified":false`, `"action":"update"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("stdout=%s want %s", stdout, want)
		}
	}
}

func TestMailMoveMany_VerifiesCheckpointsAndResumesWithoutReplay(t *testing.T) {
	var mu sync.Mutex
	moved := false
	updateCalls := 0
	sourceFolder := "default0/INBOX"
	destinationFolder := "default0/Trace"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		switch r.URL.Path + "?" + action {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/folders?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": destinationFolder, "title": "Trace", "module": "mail", "folder_id": "default0", "subfolders": false,
				"last_modified": nil, "permissions": []any{},
			}})
		case "/ajax/mail?list":
			var requested []map[string]string
			if err := json.Unmarshal([]byte(r.URL.Query().Get("data")), &requested); err != nil || len(requested) != 1 {
				t.Errorf("mail list data=%q err=%v", r.URL.Query().Get("data"), err)
				writeJSONResponse(w, map[string]any{"data": []any{}})
				return
			}
			mu.Lock()
			isMoved := moved
			mu.Unlock()
			folder, id := requested[0]["folder"], requested[0]["id"]
			rows := []any{}
			if (!isMoved && folder == sourceFolder && id == "7") || (isMoved && folder == destinationFolder && id == "17") {
				rows = append(rows, safeMailRow(id, folder))
			}
			writeJSONResponse(w, map[string]any{"data": rows})
		case "/ajax/mail?update":
			mu.Lock()
			moved = true
			updateCalls++
			mu.Unlock()
			writeJSONResponse(w, map[string]any{"data": map[string]any{"folder_id": destinationFolder, "id": "17"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	root := t.TempDir()
	checkpoint := filepath.Join(root, "moves.jsonl")
	input := `[{"folder":"default0/INBOX","id":"7","to":"default0/Trace"}]`
	args := []string{"mail", "move-many", "--input", input, "--verify", "--checkpoint", checkpoint, "--llm"}
	stdout, stderr, code := runAgainstHTTPServer(t, srv, root, args)
	if code != 0 || !strings.Contains(stdout, `"status":"moved"`) || !strings.Contains(stdout, `"verified":true`) {
		t.Fatalf("first exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	info, err := os.Stat(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("checkpoint mode=%o want 600", info.Mode().Perm())
	}

	stdout, stderr, code = runAgainstHTTPServer(t, srv, root, args)
	if code != 0 || !strings.Contains(stdout, `"status":"resumed"`) || !strings.Contains(stdout, `"resumed":1`) {
		t.Fatalf("resume exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	mu.Lock()
	if updateCalls != 1 {
		t.Fatalf("mail update calls=%d want 1", updateCalls)
	}
	mu.Unlock()

	file, err := os.Open(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lines != 1 {
		t.Fatalf("checkpoint lines=%d want 1 after resume", lines)
	}
}

func TestMailMoveMany_LiveRequiresVerifyAndCheckpointBeforeNetwork(t *testing.T) {
	for name, extra := range map[string][]string{
		"missing verify":     {"--checkpoint", filepath.Join(t.TempDir(), "moves.jsonl")},
		"missing checkpoint": {"--verify"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatalf("invalid invocation unexpectedly requested %s", r.URL)
			}))
			defer srv.Close()
			args := []string{"mail", "move-many", "--input", `[{"folder":"a","id":"1","to":"b"}]`, "--llm"}
			args = append(args, extra...)
			stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), args)
			if code == 0 || stdout != "" || !strings.Contains(stderr, "invalid_argument") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestMailMoveMany_RejectsBlankIdentitiesAndTrailingCheckpointJSON(t *testing.T) {
	for name, input := range map[string]string{
		"blank folder":      `[{"folder":" ","id":"1","to":"b"}]`,
		"blank id":          `[{"folder":"a","id":" ","to":"b"}]`,
		"blank destination": `[{"folder":"a","id":"1","to":" "}]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMailMoveManyInput(input); err == nil || !strings.Contains(err.Error(), "must not be blank") {
				t.Fatalf("parseMailMoveManyInput error=%v, want blank-identity rejection", err)
			}
		})
	}

	move := mailMoveManyInput{Folder: "a", ID: "1", To: "b"}
	record := mailMoveCheckpoint{
		Key: mailMoveKey(move), SourceFolder: move.Folder, SourceID: move.ID,
		DestinationFolder: move.To, DestinationID: "2",
		Fingerprint: "sha256:" + strings.Repeat("a", 64), Verified: true,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(t.TempDir(), "trailing.jsonl")
	if err := os.WriteFile(checkpoint, append(encoded, []byte(` {"extra":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadMailMoveCheckpoints(checkpoint); err == nil || !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("loadMailMoveCheckpoints error=%v, want trailing-value rejection", err)
	}
}

func TestMailMoveMany_ChunkSizeFlagIsGone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("invalid invocation unexpectedly requested %s", r.URL)
	}))
	defer srv.Close()
	stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{
		"mail", "move-many", "--input", `[{"folder":"a","id":"1","to":"b"}]`, "--dry-run", "--chunk-size", "5", "--llm",
	})
	if code == 0 || stdout != "" || !strings.Contains(stderr, "unknown flag: --chunk-size") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want unknown flag --chunk-size", code, stdout, stderr)
	}
	stdout, stderr, code = runAgainstHTTPServer(t, srv, t.TempDir(), []string{"describe", "mail.move-many", "--llm"})
	if code != 0 || strings.Contains(stdout, "chunk-size") {
		t.Fatalf("exit=%d stdout=%s stderr=%s, want no chunk-size argument", code, stdout, stderr)
	}
}

// moveManyFakeServer simulates sources default0/INBOX/<n> that mail update moves to
// default0/Trace/<n+10>. failUpdate makes the update for a source id fail with an OX
// error; hideDestination makes the next k destination reads for a source id return no row.
type moveManyFakeServer struct {
	mu              sync.Mutex
	moved           map[string]bool
	failUpdate      map[string]bool
	hideDestination map[string]int
	updates         []string
}

func newMoveManyFakeServer(t *testing.T, state *moveManyFakeServer) *httptest.Server {
	t.Helper()
	state.moved = map[string]bool{}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		query := r.URL.Query()
		switch r.URL.Path + "?" + query.Get("action") {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/folders?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": "default0/Trace", "title": "Trace", "module": "mail", "folder_id": "default0", "subfolders": false,
				"last_modified": nil, "permissions": []any{},
			}})
		case "/ajax/mail?list":
			var requested []map[string]string
			_ = json.Unmarshal([]byte(query.Get("data")), &requested)
			folder, id := requested[0]["folder"], requested[0]["id"]
			rows := []any{}
			switch folder {
			case "default0/INBOX":
				if !state.moved[id] {
					rows = append(rows, safeMailRow(id, folder))
				}
			case "default0/Trace":
				n, _ := strconv.Atoi(id)
				source := strconv.Itoa(n - 10)
				if state.moved[source] {
					if state.hideDestination[source] > 0 {
						state.hideDestination[source]--
					} else {
						rows = append(rows, safeMailRow(id, folder))
					}
				}
			}
			writeJSONResponse(w, map[string]any{"data": rows})
		case "/ajax/mail?update":
			id := query.Get("id")
			state.updates = append(state.updates, id)
			if state.failUpdate[id] {
				writeJSONResponse(w, map[string]any{"error": "Mail folder is full", "code": "MSG-1001", "category": 8})
				return
			}
			state.moved[id] = true
			n, _ := strconv.Atoi(id)
			writeJSONResponse(w, map[string]any{"data": map[string]any{"folder_id": "default0/Trace", "id": strconv.Itoa(n + 10)}})
		default:
			http.NotFound(w, r)
		}
	}))
}

const moveManyThreeMoves = `[{"folder":"default0/INBOX","id":"1","to":"default0/Trace"},{"folder":"default0/INBOX","id":"2","to":"default0/Trace"},{"folder":"default0/INBOX","id":"3","to":"default0/Trace"}]`

func TestMailMoveMany_FailurePartWayEmitsThePartialResult(t *testing.T) {
	for _, useOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "stdout", true: "output file"}[useOutput], func(t *testing.T) {
			state := &moveManyFakeServer{failUpdate: map[string]bool{"2": true}}
			srv := newMoveManyFakeServer(t, state)
			defer srv.Close()
			root := t.TempDir()
			output := filepath.Join(root, "result.json")
			args := []string{"mail", "move-many", "--input", moveManyThreeMoves, "--verify", "--checkpoint", filepath.Join(root, "moves.jsonl"), "--llm"}
			if useOutput {
				args = append(args, "--output", output)
			}
			stdout, stderr, code := runAgainstHTTPServer(t, srv, root, args)
			if code == 0 {
				t.Fatalf("exit=0 stdout=%s, want a nonzero exit", stdout)
			}
			if !strings.Contains(stderr, "stopped at move 2 of 3") || !strings.Contains(stderr, "moved 1, resumed 0, 1 not attempted") {
				t.Errorf("stderr = %s, want the counts in the error", stderr)
			}
			documentJSON := stdout
			if useOutput {
				data, err := os.ReadFile(output)
				if err != nil {
					t.Fatalf("partial result was not written to --output: %v", err)
				}
				documentJSON = string(data)
				if !strings.Contains(stdout, `"saved_to"`) || !strings.Contains(stdout, `"moved":1`) || !strings.Contains(stdout, `"remaining":1`) {
					t.Errorf("stdout = %s, want the saved_to summary with counts", stdout)
				}
			}
			var document mailMoveManyDocument
			if err := json.Unmarshal([]byte(documentJSON), &document); err != nil {
				t.Fatalf("partial result is not a JSON document: %v; %s", err, documentJSON)
			}
			if document.Moved != 1 || len(document.Items) != 1 || document.Items[0].Status != "moved" ||
				document.Failed == nil || document.Failed.Index != 1 || *document.Failed.Source.ID != "2" || document.Failed.Moved ||
				document.Failed.Checkpointed || document.Failed.Code == "" || document.Remaining == nil || *document.Remaining != 1 {
				t.Errorf("partial result = %s, want one moved item, failed move index 1, one remaining", documentJSON)
			}
			if strings.Join(state.updates, ",") != "1,2" {
				t.Errorf("updates = %v, want the run to stop after the failed move", state.updates)
			}
		})
	}
}

func TestMailMoveMany_FailurePartWayHumanOutputNamesTheFailure(t *testing.T) {
	state := &moveManyFakeServer{failUpdate: map[string]bool{"2": true}}
	srv := newMoveManyFakeServer(t, state)
	defer srv.Close()
	root := t.TempDir()
	stdout, stderr, code := runAgainstHTTPServer(t, srv, root, []string{"mail", "move-many", "--input", moveManyThreeMoves, "--verify", "--checkpoint", filepath.Join(root, "moves.jsonl")})
	if code == 0 || !strings.Contains(stdout, "moved") || !strings.Contains(stdout, "failed_source") || !strings.Contains(stdout, "default0/INBOX/2") || !strings.Contains(stdout, "remaining") {
		t.Errorf("exit=%d stdout=%s stderr=%s, want the moved row and the failed move", code, stdout, stderr)
	}
}

func TestMailMoveMany_VerificationFailureIsCheckpointedAndResumes(t *testing.T) {
	state := &moveManyFakeServer{hideDestination: map[string]int{"2": 2}}
	srv := newMoveManyFakeServer(t, state)
	defer srv.Close()
	root := t.TempDir()
	checkpoint := filepath.Join(root, "moves.jsonl")
	args := []string{"mail", "move-many", "--input", moveManyThreeMoves, "--verify", "--checkpoint", checkpoint, "--llm"}

	stdout, stderr, code := runAgainstHTTPServer(t, srv, root, args)
	if code == 0 || !strings.Contains(stderr, "verification_failed") {
		t.Fatalf("first run exit=%d stderr=%s, want verification_failed", code, stderr)
	}
	var document mailMoveManyDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Failed == nil ||
		!document.Failed.Moved || !document.Failed.Checkpointed || document.Failed.Destination.ID == nil || *document.Failed.Destination.ID != "12" {
		t.Fatalf("first run stdout = %s, want a moved, checkpointed failure with destination id 12", stdout)
	}
	records, err := loadMailMoveCheckpoints(checkpoint)
	if err != nil || len(records) != 2 {
		t.Fatalf("checkpoint records = %v err=%v, want two records", records, err)
	}

	// The destination is still hidden once more: the rerun reports it without moving again.
	stdout, stderr, code = runAgainstHTTPServer(t, srv, root, args)
	if code == 0 || !strings.Contains(stderr, "verification_failed") || !strings.Contains(stderr, "still does not verify") || strings.Contains(stderr, "not found: id=2") {
		t.Fatalf("second run exit=%d stdout=%s stderr=%s, want the unverified destination reported", code, stdout, stderr)
	}

	// Now the destination verifies: the rerun resumes both earlier moves and finishes.
	stdout, stderr, code = runAgainstHTTPServer(t, srv, root, args)
	if code != 0 {
		t.Fatalf("third run exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	document = mailMoveManyDocument{}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Resumed != 2 || document.Moved != 1 || document.Failed != nil {
		t.Fatalf("third run stdout = %s, want two resumed and one moved", stdout)
	}
	if strings.Join(state.updates, ",") != "1,2,3" {
		t.Errorf("updates = %v, want each source moved exactly once", state.updates)
	}
	records, err = loadMailMoveCheckpoints(checkpoint)
	if err != nil || len(records) != 3 {
		t.Fatalf("checkpoint records = %v err=%v, want three records", records, err)
	}
	for key, record := range records {
		if !record.Verified {
			t.Errorf("record %s is still unverified after the destination verified", key)
		}
	}
}

func TestLoadMailMoveCheckpoints_VerifiedRecordMayOnlyConfirmAnUnverifiedOne(t *testing.T) {
	move := mailMoveManyInput{Folder: "a", ID: "1", To: "b"}
	unverified := mailMoveCheckpoint{
		Key: mailMoveKey(move), SourceFolder: move.Folder, SourceID: move.ID,
		DestinationFolder: move.To, DestinationID: "2", Fingerprint: "sha256:" + strings.Repeat("a", 64),
	}
	verified := unverified
	verified.Verified = true
	otherDestination := verified
	otherDestination.DestinationID = "3"
	write := func(records ...mailMoveCheckpoint) string {
		path := filepath.Join(t.TempDir(), "moves.jsonl")
		lines := []byte{}
		for _, record := range records {
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(append(lines, encoded...), '\n')
		}
		if err := os.WriteFile(path, lines, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	records, err := loadMailMoveCheckpoints(write(unverified, verified))
	if err != nil || !records[unverified.Key].Verified {
		t.Errorf("unverified then verified: records=%v err=%v, want the verified record", records, err)
	}
	for name, lines := range map[string][]mailMoveCheckpoint{
		"verified then unverified":    {verified, unverified},
		"unverified then other place": {unverified, otherDestination},
	} {
		if _, err := loadMailMoveCheckpoints(write(lines...)); err == nil || !strings.Contains(err.Error(), "conflicts") {
			t.Errorf("%s: err=%v, want a conflict", name, err)
		}
	}
}

// newOneMoveServer serves one message, 7 in INBOX, that a move takes to 17 in Trace. It
// counts mail update requests.
func newOneMoveServer(t *testing.T, updateCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	moved := false
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path + "?" + r.URL.Query().Get("action") {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/folders?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": "default0/Trace", "title": "Trace", "module": "mail", "folder_id": "default0", "subfolders": false,
				"last_modified": nil, "permissions": []any{},
			}})
		case "/ajax/mail?list":
			var requested []map[string]string
			_ = json.Unmarshal([]byte(r.URL.Query().Get("data")), &requested)
			mu.Lock()
			isMoved := moved
			mu.Unlock()
			rows := []any{}
			if len(requested) == 1 {
				folder, id := requested[0]["folder"], requested[0]["id"]
				if (!isMoved && folder == "default0/INBOX" && id == "7") || (isMoved && folder == "default0/Trace" && id == "17") {
					rows = append(rows, safeMailRow(id, folder))
				}
			}
			writeJSONResponse(w, map[string]any{"data": rows})
		case "/ajax/mail?update":
			mu.Lock()
			moved = true
			mu.Unlock()
			updateCalls.Add(1)
			writeJSONResponse(w, map[string]any{"data": map[string]any{"folder_id": "default0/Trace", "id": "17"}})
		default:
			http.NotFound(w, r)
		}
	}))
}

// A checkpoint fingerprint from an older mbx version (local-time mail dates) no longer
// matches. The run still stops, and the error says how to recover.
func TestMailMoveMany_CheckpointMismatchNamesOlderVersions(t *testing.T) {
	var updateCalls atomic.Int32
	srv := newOneMoveServer(t, &updateCalls)
	defer srv.Close()
	root := t.TempDir()
	checkpoint := filepath.Join(root, "moves.jsonl")
	args := []string{"mail", "move-many", "--input", `[{"folder":"default0/INBOX","id":"7","to":"default0/Trace"}]`, "--verify", "--checkpoint", checkpoint, "--llm"}
	if stdout, stderr, code := runAgainstHTTPServer(t, srv, root, args); code != 0 {
		t.Fatalf("first exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	content, err := os.ReadFile(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatal(err)
	}
	record["fingerprint"] = "sha256:" + strings.Repeat("0", 64)
	rewritten, _ := json.Marshal(record)
	if err := os.WriteFile(checkpoint, append(rewritten, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := runAgainstHTTPServer(t, srv, root, args)
	if code == 0 || !strings.Contains(stderr, "checkpoint_invalid") || !strings.Contains(stderr, "older mbx version") || !strings.Contains(stderr, "new checkpoint file") {
		t.Fatalf("resume exit=%d stderr=%s, want checkpoint_invalid that names older mbx versions", code, stderr)
	}
	if calls := updateCalls.Load(); calls != 1 {
		t.Fatalf("mail update calls=%d want 1", calls)
	}
}

func TestMailMove_ExpectFingerprintMismatchNamesOlderVersions(t *testing.T) {
	var updateCalls atomic.Int32
	srv := newOneMoveServer(t, &updateCalls)
	defer srv.Close()
	args := []string{"mail", "move", "--folder", "default0/INBOX", "--id", "7", "--to", "default0/Trace",
		"--expect-fingerprint", "sha256:" + strings.Repeat("0", 64), "--llm"}
	_, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), args)
	if code == 0 || !strings.Contains(stderr, "precondition_failed") || !strings.Contains(stderr, "older mbx version") {
		t.Fatalf("exit=%d stderr=%s, want precondition_failed that names older mbx versions", code, stderr)
	}
	if calls := updateCalls.Load(); calls != 0 {
		t.Fatalf("mail update calls=%d want 0", calls)
	}
}
