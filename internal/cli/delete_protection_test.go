// ABOUTME: CLI integration tests for user-configured delete protection.
// ABOUTME: Covers scoped and global policies, dry runs, structured errors, and batch bypasses.
package cli

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func protectionConfig(selectors ...string) string {
	quoted := make([]string, len(selectors))
	for i, selector := range selectors {
		quoted[i] = strconv.Quote(selector)
	}
	return "[safety]\ndelete_protection = [" + strings.Join(quoted, ", ") + "]\n"
}

func TestDeleteProtection_ScopedResourcesBlockBeforeNetwork(t *testing.T) {
	tests := []struct {
		name      string
		selector  string
		operation string
		args      []string
	}{
		{
			name:      "mail",
			selector:  "mail",
			operation: "mail.delete",
			args:      []string{"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--confirm", "7", "--llm"},
		},
		{
			name:      "folders",
			selector:  "folders",
			operation: "folders.delete",
			args:      []string{"folders", "delete", "--id", "default0/Archive", "--confirm", "default0/Archive", "--llm"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			env := map[string]string{
				"MBX_USERNAME": "fixture-user@example.invalid",
				"MBX_PASSWORD": "pw",
			}
			stdout, stderr, code := runWithConfig(t, srv, env, tt.args, protectionConfig(tt.selector))

			if code == 0 {
				t.Fatal("exit code = 0, want delete protection failure")
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			for _, want := range []string{`"code":"delete_protected"`, tt.operation, "safety.delete_protection"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want %q", stderr, want)
				}
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("protected delete made HTTP requests: %+v", srv.Requests())
			}
		})
	}
}

func TestDeleteProtection_AllBlocksEveryDestructiveOperation(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{
		"MBX_USERNAME": "fixture-user@example.invalid",
		"MBX_PASSWORD": "pw",
	}
	stdout, stderr, code := runWithConfig(t, srv, env, []string{
		"contacts", "delete",
		"--id", "17",
		"--folder", "default0/Contacts",
		"--confirm", "17",
		"--llm",
	}, protectionConfig("all"))

	if code == 0 {
		t.Fatal("exit code = 0, want delete protection failure")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `"code":"delete_protected"`) || !strings.Contains(stderr, "contacts.delete") {
		t.Fatalf("stderr = %q, want protected contacts.delete error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("protected delete made HTTP requests: %+v", srv.Requests())
	}
}

func TestDeleteProtection_AllBlocksChangePasswordWithoutLeakingArguments(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	_, stderr, code := runWithConfig(t, srv, map[string]string{}, []string{
		"change-password",
		"--current-password", "old-secret",
		"--new-password", "new-secret",
		"--confirm", "fixture-user@example.invalid",
		"--llm",
	}, protectionConfig("all"))

	if code == 0 {
		t.Fatal("exit code = 0, want delete protection failure")
	}
	if !strings.Contains(stderr, `"code":"delete_protected"`) || !strings.Contains(stderr, "change-password") {
		t.Fatalf("stderr = %q, want protected change-password error", stderr)
	}
	if strings.Contains(stderr, "old-secret") || strings.Contains(stderr, "new-secret") {
		t.Fatalf("stderr leaked password values: %q", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("protected operation made HTTP requests: %+v", srv.Requests())
	}
}

func TestDeleteProtection_DryRunRemainsAvailable(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(t, srv, map[string]string{}, []string{
		"mail", "delete",
		"--id", "7",
		"--folder", "default0/INBOX",
		"--confirm", "7",
		"--dry-run",
		"--llm",
	}, protectionConfig("all"))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Fatalf("stdout = %q, want dry-run result", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("dry run made HTTP requests: %+v", srv.Requests())
	}
}

func TestDeleteProtection_DryRunFlagUsedAsAValueDoesNotBypassPolicy(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(t, srv, map[string]string{}, []string{
		"mail", "delete",
		"--id", "--dry-run",
		"--folder", "default0/INBOX",
		"--confirm", "--dry-run",
		"--llm",
	}, protectionConfig("mail"))

	if code == 0 {
		t.Fatal("exit code = 0, want delete protection failure")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `"code":"delete_protected"`) {
		t.Fatalf("stderr = %q, want delete protection error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("protected delete made HTTP requests: %+v", srv.Requests())
	}
}

func TestDeleteProtection_ScopeDoesNotBlockAnotherResource(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	_, stderr, code := runWithConfig(
		t,
		srv,
		map[string]string{},
		[]string{"folders", "delete", "--llm"},
		protectionConfig("mail"),
	)

	if code == 0 {
		t.Fatal("exit code = 0, want normal argument validation failure")
	}
	if strings.Contains(stderr, "delete_protected") {
		t.Fatalf("stderr = %q, mail scope must not protect folders.delete", stderr)
	}
	if !strings.Contains(stderr, "id") {
		t.Fatalf("stderr = %q, want folders.delete argument validation", stderr)
	}
}

func TestDeleteProtection_ExactOperationSelector(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	_, stderr, code := runWithConfig(
		t,
		srv,
		map[string]string{},
		[]string{"contacts", "delete", "--llm"},
		protectionConfig("contacts.delete"),
	)

	if code == 0 {
		t.Fatal("exit code = 0, want delete protection failure")
	}
	if !strings.Contains(stderr, `"code":"delete_protected"`) || !strings.Contains(stderr, "contacts.delete") {
		t.Fatalf("stderr = %q, want protected contacts.delete error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("protected delete made HTTP requests: %+v", srv.Requests())
	}
}

func TestDeleteProtection_InvalidSelectorFailsClosedForDestructiveCommands(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(t, srv, map[string]string{}, []string{
		"mail", "delete",
		"--id", "7",
		"--folder", "default0/INBOX",
		"--confirm", "7",
		"--llm",
	}, protectionConfig("mail", "typo"))

	if code == 0 {
		t.Fatal("exit code = 0, want invalid protection configuration failure")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	for _, want := range []string{`"code":"invalid_configuration"`, "safety.delete_protection", "typo"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid protection configuration made HTTP requests: %+v", srv.Requests())
	}
}

func TestDeleteProtection_ParsingArrays(t *testing.T) {
	tests := []struct {
		name       string
		values     []string
		wantAll    bool
		wantScopes int
	}{
		{name: "omitted", values: nil, wantScopes: 0},
		{name: "empty", values: []string{}, wantScopes: 0},
		{name: "all", values: []string{"ALL"}, wantAll: true},
		{name: "spaced scopes", values: []string{" mail ", " folders "}, wantScopes: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			protection, err := parseDeleteProtection(tt.values)
			if err != nil {
				t.Fatalf("parseDeleteProtection(%q) error: %v", tt.values, err)
			}
			if protection.all != tt.wantAll {
				t.Errorf("all = %t, want %t", protection.all, tt.wantAll)
			}
			if len(protection.selectors) != tt.wantScopes {
				t.Errorf("selectors = %v, want %d entries", protection.selectors, tt.wantScopes)
			}
		})
	}
}

func TestDeleteProtection_AllCannotBeCombinedWithScopes(t *testing.T) {
	_, err := parseDeleteProtection([]string{"all", "mail"})
	if err == nil || err.Code != "invalid_configuration" {
		t.Fatalf("error = %#v, want invalid configuration", err)
	}
}

func TestDeleteProtection_DoesNotBlockReads(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(
		t,
		srv,
		map[string]string{},
		[]string{"commands", "--llm"},
		protectionConfig("all"),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"commands"`) {
		t.Fatalf("stdout = %q, want commands result", stdout)
	}
}

func TestDeleteProtection_LegacyEnvironmentVariableIsIgnored(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	_, stderr, code := run(
		t,
		srv,
		map[string]string{"MBX_DELETE_PROTECTION": "all"},
		[]string{"mail", "delete", "--llm"},
	)

	if code == 0 {
		t.Fatal("exit code = 0, want normal argument validation failure")
	}
	if strings.Contains(stderr, "delete_protected") {
		t.Fatalf("stderr = %q, legacy environment variable must not control policy", stderr)
	}
	if !strings.Contains(stderr, "--id") {
		t.Fatalf("stderr = %q, want mail.delete argument validation", stderr)
	}
}

func TestDeleteProtection_BlocksDestructiveBatchSubRequest(t *testing.T) {
	tests := []struct {
		name     string
		selector string
		input    string
		want     string
	}{
		{
			name:     "mail delete",
			selector: "mail",
			input:    `[{"resource":"mail","action":"delete","params":{"id":"7","folder":"default0/INBOX"}}]`,
			want:     "mail.delete",
		},
		{
			name:     "folder delete",
			selector: "folders",
			input:    `[{"resource":"folders","action":"delete","params":{"id":"default0/Archive"}}]`,
			want:     "folders.delete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			path := writeBatchInput(t, tt.input)

			env := map[string]string{
				"MBX_USERNAME": "fixture-user@example.invalid",
				"MBX_PASSWORD": "pw",
			}
			stdout, stderr, code := runWithConfig(
				t,
				srv,
				env,
				[]string{"batch", "run", "--input", "@" + path, "--llm"},
				protectionConfig(tt.selector),
			)

			// batch planning rejects every non-read sub-request before authentication, so
			// the protected delete never runs, whatever the policy says.
			if code != 4 {
				t.Fatalf("exit code = %d, want 4 (rejected at batch planning); stderr=%s", code, stderr)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, `"code":"invalid_argument"`) || !strings.Contains(stderr, tt.want) {
				t.Fatalf("stderr = %q, want a planning rejection naming %s", stderr, tt.want)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("protected batch made HTTP requests: %+v", srv.Requests())
			}
		})
	}
}

func TestDeleteProtection_BatchMutationDryRunIsStillRejectedBySafeBatchPolicy(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	path := writeBatchInput(t, `[{"resource":"mail","action":"delete","params":{"id":"7"}}]`)

	stdout, stderr, code := runWithConfig(
		t,
		srv,
		map[string]string{},
		[]string{"batch", "run", "--input", "@" + path, "--dry-run", "--llm"},
		protectionConfig("all"),
	)

	if code != 4 {
		t.Fatalf("exit code = %d, want invalid_argument; stderr=%s", code, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "batch.run rejects all mutations") {
		t.Fatalf("stderr = %q, want safe batch mutation refusal", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("batch dry run made HTTP requests: %+v", srv.Requests())
	}
}

func TestDeleteProtection_HelpDocumentsConfiguration(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"help", "delete-protection"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"config.toml", "delete_protection", `["all"]`, "mail", "folders", "--dry-run", "batch"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
}

// TestDestructiveCommands_ConfirmMismatch_UseOneErrorContract guards the confirmation
// contract agents script against: refusing a destructive command because --confirm does not
// match is the same failure everywhere, so it must report the same error code and the same
// exit code regardless of which resource was addressed. files.delete, attachments.delete,
// and change-password used to report "confirmation_required", which ExitCodeFor does not
// map and so collapsed into the generic exit 1 shared with api_error, while the other
// twelve destructive commands reported invalid_argument and exit 4.
func TestDestructiveCommands_ConfirmMismatch_UseOneErrorContract(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, op := range operations.Default.List() {
		if !operationIsDestructive(op) {
			continue
		}
		if _, ok := op.Args["confirm"]; !ok {
			continue
		}

		t.Run(op.Name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			args := append(strings.Split(op.Name, "."), "--llm")
			for name, spec := range op.Args {
				switch {
				case name == "confirm":
					args = append(args, "--confirm", "not-the-required-value")
				case spec.Type == "bool" || spec.Type == "boolean":
					continue
				case spec.Required:
					args = append(args, "--"+name, "1")
				}
			}

			stdout, stderr, code := run(t, srv, env, args)
			if code != 4 {
				t.Errorf("exit code = %d, want 4 (invalid_argument); stdout=%s stderr=%s", code, stdout, stderr)
			}
			if !strings.Contains(stderr, `"code":"invalid_argument"`) {
				t.Errorf("stderr = %q, want an invalid_argument error", stderr)
			}
			if !strings.Contains(stderr, "confirm") {
				t.Errorf("stderr = %q, want the message to name --confirm", stderr)
			}
		})
	}
}

// destructiveProbeArgs builds a plausible, self-consistent argument list for a destructive
// operation, so the protection probe below reaches the point where a request would be sent.
func destructiveProbeArgs(op operations.Operation) []string {
	names := make([]string, 0, len(op.Args))
	for name := range op.Args {
		names = append(names, name)
	}
	sort.Strings(names)

	var args []string
	for _, name := range names {
		spec := op.Args[name]
		if !spec.Required {
			continue
		}
		if spec.Type == "bool" || spec.Type == "boolean" {
			args = append(args, "--"+name)
			continue
		}
		value := "5"
		switch name {
		case "timestamp":
			value = "1"
		case "folder":
			value = "35"
		case "rule":
			value = `{"rulename":"r","actioncmds":[]}`
		case "module", "object-id":
			value = "7"
		}
		args = append(args, "--"+name, value)
	}
	return args
}

// TestDestructiveOperations_ReachNoNetworkUnderDeleteProtection is the general guard behind
// the documented promise that delete protection "has no command-line override". The policy
// check decides whether a run is a dry run by scanning the raw arguments, skipping the value
// of each flag the operation registers. A parser that accepts a flag the operation does not
// register breaks that scan: the extra flag swallows --dry-run for the parser but not for the
// scan, so protection steps aside while the command performs a real deletion. Confirmed
// against tasks.delete, whose parser is shared with create/update and so accepted --title and
// --note. Every flag registered anywhere is tried against every destructive operation.
func TestDestructiveOperations_ReachNoNetworkUnderDeleteProtection(t *testing.T) {
	allFlags := map[string]bool{}
	for _, op := range operations.Default.List() {
		for name := range op.Args {
			allFlags[name] = true
		}
	}
	flagNames := make([]string, 0, len(allFlags))
	for name := range allFlags {
		flagNames = append(flagNames, name)
	}
	sort.Strings(flagNames)

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, op := range operations.Default.List() {
		if !operationIsDestructive(op) {
			continue
		}
		for _, flag := range flagNames {
			srv := testutil.NewServer()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/folders", "list", testutil.Response{
				Body: map[string]any{"data": []any{[]any{"35", "tasks"}}},
			})
			srv.Handle("/ajax/user", "get", testutil.Response{
				Body: map[string]any{"data": map[string]any{"folder_id": "33"}},
			})
			for _, module := range []string{
				"tasks", "mail", "contacts", "folders", "infostore", "chronos", "attachment",
				"mailfilter", "snippet", "group", "resource", "account", "reminder", "share",
			} {
				for _, action := range []string{"delete", "detach", "get", "list", "all"} {
					srv.Handle("/ajax/"+module, action, testutil.Response{Body: map[string]any{"data": []any{}}})
				}
			}

			args := append(strings.Split(op.Name, "."), "--"+flag, "--dry-run")
			args = append(args, destructiveProbeArgs(op)...)
			args = append(args, "--llm")
			runWithConfig(t, srv, env, args, protectionConfig("all"))

			for _, r := range srv.Requests() {
				if r.URL.Query().Get("action") != "login" {
					t.Errorf("%s with --%s reached %s?action=%s despite delete protection",
						op.Name, flag, r.URL.Path, r.URL.Query().Get("action"))
				}
			}
			srv.Close()
		}
	}
}

// TestBatchRun_ProtectionCoversDestructiveActionsWithoutAnOperation is the regression test
// for a delete-protection bypass through batch run. "folders clear" and "mail clear" empty a
// folder and "attachment detach" removes attachments, and none of them is a registered
// operation. Batch planning accepts registered reads only, so it rejects all of them before
// authentication.
func TestBatchRun_ProtectionCoversDestructiveActionsWithoutAnOperation(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, sub := range []map[string]any{
		{"resource": "folders", "action": "clear", "params": map[string]any{"id": "36"}},
		{"resource": "mail", "action": "clear", "params": map[string]any{"folder": "default0/INBOX"}},
		{"resource": "attachment", "action": "detach", "params": map[string]any{"id": 1}},
		{"resource": "mail", "action": "expunge", "params": map[string]any{"folder": "default0/Trash"}},
	} {
		name := sub["resource"].(string) + " " + sub["action"].(string)
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/multiple", "", testutil.Response{Body: []byte(`[{}]`)})

			input, err := json.Marshal([]any{sub})
			if err != nil {
				t.Fatal(err)
			}
			stdout, stderr, code := runWithConfig(t, srv, env,
				[]string{"batch", "run", "--input", string(input), "--llm"}, protectionConfig("all"))

			if code != 4 || stdout != "" {
				t.Fatalf("exit code = %d stdout=%q, want 4 (rejected at batch planning)", code, stdout)
			}
			if !strings.Contains(stderr, "unknown operation") {
				t.Errorf("stderr = %q, want the raw action rejected as an unknown operation", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("%s sent %d requests, want none", name, len(srv.Requests()))
			}
		})
	}
}

// TestBatchRun_ProtectionLeavesReadSubRequestsAlone pins that a read batch runs normally
// with protection enabled.
func TestBatchRun_ProtectionLeavesReadSubRequestsAlone(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{Body: []byte(`[{"data":[]}]`)})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	input := `[{"resource":"mail","action":"all","params":{"folder":"default0/INBOX"}}]`
	stdout, stderr, code := runWithConfig(t, srv, env,
		[]string{"batch", "run", "--input", input, "--llm"}, protectionConfig("all"))

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"results"`) {
		t.Errorf("stdout = %q, want batch results", stdout)
	}
}

// TestBatchPlanning_RejectsEveryNonReadOperation is why batch run has no delete-protection
// check of its own: every registered write or destructive operation, in each spelling the
// batch input accepts, is rejected by planning, which runs before authentication.
func TestBatchPlanning_RejectsEveryNonReadOperation(t *testing.T) {
	checked := 0
	for _, op := range operations.Default.List() {
		if op.SideEffect == operations.SideEffectRead {
			continue
		}
		checked++
		spellings := []batchRequest{
			{Operation: op.Name},
			{Resource: op.Resource, Action: op.Action},
			{Resource: op.Endpoint, Action: op.Action},
		}
		for _, req := range spellings {
			if plans, err := planBatchRequests([]batchRequest{req}); err == nil {
				t.Errorf("planBatchRequests(%+v) = %+v, want %s rejected", req, plans, op.Name)
			}
		}
	}
	if checked == 0 {
		t.Fatal("registry has no write or destructive operations to check")
	}
	for _, req := range []batchRequest{
		{Resource: "mail", Action: "update", Params: map[string]any{"folder_id": "default0/Trash"}},
		{Resource: "share/management", Action: "deleteLink"},
		{Resource: "attachment", Action: "detach"},
		{Resource: "mail", Action: "expunge"},
		{Resource: "folders", Action: "clear"},
	} {
		if _, err := planBatchRequests([]batchRequest{req}); err == nil {
			t.Errorf("planBatchRequests(%+v) accepted a raw mutation", req)
		}
	}
}

func TestBatchRun_ProtectedDestructiveOperationSendsNothing(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/multiple", "", testutil.Response{Body: []byte(`[{}]`)})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := runWithConfig(t, srv, env, []string{
		"batch", "run", "--input", `[{"operation":"files.delete","params":{"id":"36/2","confirm":"36/2"}}]`, "--llm",
	}, protectionConfig("all"))
	if code != 4 || stdout != "" || !strings.Contains(stderr, "batch.run rejects all mutations") {
		t.Fatalf("code=%d stdout=%q stderr=%q, want the mutation rejected at planning", code, stdout, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("sent %d requests, want none", len(srv.Requests()))
	}
}
