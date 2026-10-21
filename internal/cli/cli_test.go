// ABOUTME: CLI integration tests for the whoami command: human and LLM output,
// ABOUTME: authentication failures, API failures, and stdout/stderr/exit-code separation.
package cli

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// assertGolden compares got against the fixed contents of the golden file at path. Golden
// files are not updated automatically; a mismatch means the machine-readable contract
// changed and the golden file must be reviewed and updated intentionally.
func assertGolden(t *testing.T, path, got string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file %s: %v", path, err)
	}
	if got != string(want) {
		t.Errorf("output does not match golden file %s:\n got:  %q\n want: %q", path, got, string(want))
	}
}

func run(t *testing.T, srv *testutil.Server, env map[string]string, args []string) (stdout, stderr string, code int) {
	t.Helper()
	return runWithConfig(t, srv, env, args, "")
}

func runWithConfig(t *testing.T, srv *testutil.Server, env map[string]string, args []string, contents string) (stdout, stderr string, code int) {
	return runWithConfigAndPasswordPrompt(t, srv, env, args, contents, nil)
}

func runWithConfigAndPasswordPrompt(
	t *testing.T,
	srv *testutil.Server,
	env map[string]string,
	args []string,
	contents string,
	prompt PasswordPrompt,
) (stdout, stderr string, code int) {
	stdout, stderr, code, _ = runWithConfigAndLoginPrompts(
		t, srv, env, args, contents, LoginPrompts{Password: prompt},
	)
	return stdout, stderr, code
}

func runWithConfigAndLoginPrompts(
	t *testing.T,
	srv *testutil.Server,
	env map[string]string,
	args []string,
	contents string,
	prompts LoginPrompts,
) (stdout, stderr string, code int, configPath string) {
	t.Helper()
	var out, errOut bytes.Buffer
	getenv := func(k string) string { return env[k] }
	root := t.TempDir()
	configPath = filepath.Join(root, "config.toml")
	sessionPath := filepath.Join(root, "session.json")
	if contents != "" {
		if err := os.WriteFile(configPath, []byte(contents), 0o600); err != nil {
			t.Fatalf("writing test config: %v", err)
		}
	}

	code = RunWithLoginPrompts(&out, &errOut, args, srv.Client(), srv.URL+"/ajax", configPath, sessionPath, getenv, prompts)
	return out.String(), errOut.String(), code, configPath
}

func TestWhoami_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"email1": "fixture-user@example.invalid", "user_id": 42, "display_name": "CLI Test",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"whoami"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "fixture-user@example.invalid") {
		t.Errorf("stdout = %q, want email present", stdout)
	}
}

func TestWhoami_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"email1": "fixture-user@example.invalid", "user_id": 42, "display_name": "CLI Test",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"whoami", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("stdout not JSON: %q", stdout)
	}
	if !strings.Contains(stdout, `"email":"fixture-user@example.invalid"`) {
		t.Errorf("stdout = %q, want email field", stdout)
	}
}

func TestWhoami_RejectsIncompleteIdentity(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
	}{
		{name: "missing email", data: map[string]any{"user_id": 42}},
		{name: "missing user id", data: map[string]any{"email1": "fixture-user@example.invalid"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/user", "get", testutil.Response{Body: map[string]any{"data": tt.data}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{"whoami", "--llm"})

			if code == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestWhoami_UsernameComesFromConfigFile(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"email1": "config-user@mailbox.org", "user_id": 42, "display_name": "Config User",
		}},
	})

	env := map[string]string{"MBX_PASSWORD": "pw"}
	stdout, stderr, code := runWithConfig(
		t,
		srv,
		env,
		[]string{"whoami", "--llm"},
		`username = "config-user@mailbox.org"`,
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"email":"config-user@mailbox.org"`) {
		t.Fatalf("stdout = %q, want configured username result", stdout)
	}
	requests := srv.Requests()
	if len(requests) == 0 {
		t.Fatal("no login request received")
	}
	body, err := io.ReadAll(requests[0].Body)
	if err != nil {
		t.Fatalf("reading login request body: %v", err)
	}
	if !strings.Contains(string(body), "config-user%40mailbox.org") {
		t.Fatalf("login request did not use configured username: %+v", requests)
	}
}

func TestWhoami_MissingCredentials_HumanError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"whoami"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "MBX_USERNAME") && !strings.Contains(stderr, "credentials") {
		t.Errorf("stderr = %q, want credentials guidance", stderr)
	}
}

func TestWhoami_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"whoami", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.HasPrefix(strings.TrimSpace(stderr), `{"error":`) {
		t.Errorf("stderr = %q, want structured error JSON", stderr)
	}
}

func TestMalformedConfig_FailsBeforeNetworkWithStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(
		t,
		srv,
		map[string]string{"MBX_USERNAME": "ignored", "MBX_PASSWORD": "ignored"},
		[]string{"whoami", "--llm"},
		`[safety]
delete_protecton = ["all"]
`,
	)

	if code == 0 {
		t.Fatal("exit code = 0, want invalid config failure")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `"code":"invalid_configuration"`) || !strings.Contains(stderr, "delete_protecton") {
		t.Fatalf("stderr = %q, want structured config error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid config made HTTP requests: %+v", srv.Requests())
	}
}

func TestHelpRemainsAvailableWithMalformedConfig(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(
		t,
		srv,
		map[string]string{},
		[]string{"help", "config"},
		`this is not valid TOML`,
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "~/.mbx/config.toml") {
		t.Fatalf("stdout = %q, want config repair guidance", stdout)
	}
}

func TestWhoami_BadCredentials_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "The user name or password is incorrect.", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "bad", "MBX_PASSWORD": "creds"}
	stdout, stderr, code := run(t, srv, env, []string{"whoami", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "LGI-0006") {
		t.Errorf("stderr = %q, want LGI-0006 code", stderr)
	}
}

func TestWhoami_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"whoami", "--bogus"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Errorf("stderr should describe the invalid flag")
	}
}

func TestWhoami_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"whoami"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

var _ = http.MethodGet

func TestCommands_LLM_ListsOnlyTopLevelGroups(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{`"name":"mail"`, `"name":"tasks"`, `"name":"whoami"`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %s", stdout, want)
		}
	}
	for _, unwanted := range []string{`"name":"mail.copy"`, `"name":"tasks.list"`} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("stdout = %q, must not contain operation %s", stdout, unwanted)
		}
	}
	assertGolden(t, "testdata/commands_llm.golden", stdout)
}

func TestCommands_HumanListsGroupsWithoutSubcommands(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands"})
	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "mail\t") || !strings.Contains(stdout, `mbx commands mail`) {
		t.Fatalf("stdout = %q, want mail group and drill-down guidance", stdout)
	}
	if strings.Contains(stdout, "mail.copy") || strings.Contains(stdout, "mail.delete") {
		t.Fatalf("stdout = %q, must not contain mail subcommands", stdout)
	}
}

func TestCommands_ResourceListsOnlyThatGroupsOperations(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "tasks", "--llm"})
	if code != 0 || stderr != "" {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	for _, want := range []string{"tasks.create", "tasks.delete", "tasks.get", "tasks.list", "tasks.update"} {
		if !strings.Contains(stdout, `"name":"`+want+`"`) {
			t.Errorf("stdout = %q, want %s", stdout, want)
		}
	}
	if strings.Contains(stdout, `"name":"mail.list"`) {
		t.Errorf("stdout = %q, must not contain unrelated command", stdout)
	}
}

func TestCommands_UnknownGroupSuggestsTopLevelDiscovery(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "bogus", "--llm"})
	if code == 0 || stdout != "" {
		t.Fatalf("unknown group: code=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stderr, `run \"mbx commands\"`) {
		t.Fatalf("stderr = %q, want top-level discovery guidance", stderr)
	}
}

func TestDescribe_LLM_Whoami(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "whoami", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("stdout not JSON: %q", stdout)
	}
	if !strings.Contains(stdout, `"name":"whoami"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
	if !strings.Contains(stdout, `"usage":"mbx whoami"`) {
		t.Errorf("stdout = %q, want usage field", stdout)
	}
	assertGolden(t, "testdata/describe_whoami_llm.golden", stdout)
}

func TestDescribe_FullZeroValueMetadataRemainsSelectable(t *testing.T) {
	for _, tc := range []struct {
		field string
		want  string
	}{
		{field: "destructive", want: "{\"destructive\":false}\n"},
		{field: "scopes", want: "{\"scopes\":[]}\n"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, []string{
				"describe", "whoami", "--full", "--format", "json", "--select", tc.field,
			})

			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if stdout != tc.want {
				t.Errorf("stdout = %q, want %q", stdout, tc.want)
			}
		})
	}
}

func TestDescribe_LLM_UnknownCommand_Error(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "bogus", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.HasPrefix(strings.TrimSpace(stderr), `{"error":`) {
		t.Errorf("stderr = %q, want structured error JSON", stderr)
	}
}

func TestDescribe_MissingCommandArg_Error(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if stderr == "" {
		t.Errorf("stderr should describe the missing argument")
	}
}

func TestNoArgs_ShowsGettingStartedHelp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, nil)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"Usage:", "Getting started", "config.toml", "MBX_PASSWORD", "mbx tutorial"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
}

func TestHelpForOperation_ShowsAllArgumentsWithoutCredentials(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "list", "--help"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	for _, want := range []string{"contacts.list", "Usage: mbx contacts list", "--limit", "max contacts"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("help contacted API: %+v", srv.Requests())
	}
}

func TestHelpResource_ListsOnlyResourceOperations(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"help", "quota"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "quota.get") {
		t.Fatalf("stdout = %q, want quota.get", stdout)
	}
	if strings.Contains(stdout, "contacts.list") {
		t.Fatalf("resource help includes unrelated command: %q", stdout)
	}
}

func TestTutorialAndOutputHelp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	tutorial, tutorialErr, tutorialCode := run(t, srv, map[string]string{}, []string{"tutorial"})
	if tutorialCode != 0 || tutorialErr != "" {
		t.Fatalf("tutorial exit=%d stderr=%q", tutorialCode, tutorialErr)
	}
	for _, want := range []string{"1.", "login", "commands", "--dry-run", "--format"} {
		if !strings.Contains(tutorial, want) {
			t.Errorf("tutorial = %q, want %q", tutorial, want)
		}
	}

	output, outputErr, outputCode := run(t, srv, map[string]string{}, []string{"help", "output"})
	if outputCode != 0 || outputErr != "" {
		t.Fatalf("help output exit=%d stderr=%q", outputCode, outputErr)
	}
	for _, want := range []string{"jsonl", "tsv", "values", "--select"} {
		if !strings.Contains(output, want) {
			t.Errorf("output help = %q, want %q", output, want)
		}
	}
}

func TestCommands_ValuesSelectionReturnsOnlyTopLevelGroups(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	legacy, legacyErr, legacyCode := run(t, srv, map[string]string{}, []string{"commands", "--llm"})
	if legacyCode != 0 || legacyErr != "" {
		t.Fatalf("legacy exit=%d stderr=%q", legacyCode, legacyErr)
	}
	values, valuesErr, valuesCode := run(t, srv, map[string]string{}, []string{"commands", "--format", "values", "--select", "name"})
	if valuesCode != 0 || valuesErr != "" {
		t.Fatalf("values exit=%d stderr=%q", valuesCode, valuesErr)
	}

	if !strings.Contains(values, "mail\n") || !strings.Contains(values, "whoami\n") || strings.Contains(values, "{") {
		t.Fatalf("values output = %q", values)
	}
	if strings.Contains(values, "mail.list") || strings.Contains(values, "tasks.list") {
		t.Fatalf("values output includes subcommands: %q", values)
	}
	if len(values) >= len(legacy) {
		t.Fatalf("values output not smaller than JSON: values=%d JSON=%d", len(values), len(legacy))
	}
}

func TestWhoami_TSVSelection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"email1": "fixture-user@example.invalid", "user_id": 42, "display_name": "CLI Test",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"whoami", "--format=tsv", "--select=email,user_id"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := "email\tuser_id\nfixture-user@example.invalid\t42\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestStructuredFormat_UnknownSelectionIsStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"email1": "fixture-user@example.invalid", "user_id": 42, "display_name": "CLI Test",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"whoami", "--format", "json", "--select", "bogus"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.HasPrefix(stderr, `{"error":`) || !strings.Contains(stderr, "bogus") {
		t.Fatalf("stderr = %q, want structured missing-field error", stderr)
	}
}

func TestUnknownCommandSuggestsClosestMatch(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	_, stderr, code := run(t, srv, map[string]string{}, []string{"commmands"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if !strings.Contains(stderr, `did you mean "commands"`) {
		t.Fatalf("stderr = %q, want suggestion", stderr)
	}
}

// TestRun_ConflictingStructuredFormats_InvalidArgument is the regression test for a
// silently ignored flag: two different --format values used to leave the last one winning,
// even though the same collision between --format human and a structured format was
// already rejected. Silently discarding one of two explicit output requests hides a
// scripting mistake behind output the caller did not ask for.
func TestRun_ConflictingStructuredFormats_InvalidArgument(t *testing.T) {
	for _, args := range [][]string{
		{"commands", "--format", "json", "--format", "tsv"},
		{"commands", "--format=json", "--format=tsv"},
		{"commands", "--format", "tsv", "--format=jsonl"},
		{"commands", "--format", "json", "--format", "human"},
		{"commands", "--format", "human", "--format", "json"},
	} {
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code == 0 {
				t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
			}
			if !strings.Contains(stderr, "--format") {
				t.Errorf("stderr = %q, want mention of --format", stderr)
			}
		})
	}
}

// TestRun_RepeatedIdenticalFormat_IsAccepted pins that only a genuine conflict is rejected,
// so a wrapper script that appends the same format twice keeps working.
func TestRun_RepeatedIdenticalFormat_IsAccepted(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "--format", "tsv", "--format=tsv"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, "name\tsummary\n") {
		t.Errorf("stdout = %q, want TSV output", stdout)
	}
}

func TestCommands_Search(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	stdout, stderr, code := run(t, srv, nil, []string{"commands", "--search", "sender aliases", "--llm"})
	if code != 0 || !strings.HasPrefix(stdout, `{"commands":[{"name":"mail.senders",`) {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	stdout, _, code = run(t, srv, nil, []string{"commands", "mail", "--search=read message", "--format", "tsv", "--select", "name,required"})
	if code != 0 || !strings.Contains(stdout, "mail.read\t") {
		t.Errorf("group search = %d %q", code, stdout)
	}
	if _, _, code = run(t, srv, nil, []string{"commands", "--search", " "}); code != 4 {
		t.Errorf("blank search exit = %d, want 4", code)
	}
}
