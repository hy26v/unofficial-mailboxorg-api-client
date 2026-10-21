// ABOUTME: No-network regressions for delete-protected mail moves into Trash.
// ABOUTME: Covers direct and raw batch paths, both policy scopes, dry-runs, and ordinary moves.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestDeleteProtection_MailMoveToTrashBlocksBeforeNetwork(t *testing.T) {
	for _, selector := range []string{"all", "mail"} {
		t.Run(selector, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			env := map[string]string{
				"MBX_USERNAME": "fixture-user@example.invalid",
				"MBX_PASSWORD": "pw",
			}
			stdout, stderr, code := runWithConfig(t, srv, env, []string{
				"mail", "move",
				"--id", "7",
				"--folder", "default0/Drafts",
				"--to", mailTrashDefaultFolder,
				"--llm",
			}, protectionConfig(selector))

			if code == 0 {
				t.Fatal("exit code = 0, want delete protection failure")
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			for _, want := range []string{`"code":"delete_protected"`, "mail.move", deleteProtectionSetting} {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want %q", stderr, want)
				}
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("protected Trash move made HTTP requests: %+v", srv.Requests())
			}
		})
	}
}

func TestDeleteProtection_MailMoveDryRunTokenUsedAsValueDoesNotBypass(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{
		"MBX_USERNAME": "fixture-user@example.invalid",
		"MBX_PASSWORD": "pw",
	}
	stdout, stderr, code := runWithConfig(t, srv, env, []string{
		"mail", "move",
		"--id", "--dry-run",
		"--folder", "default0/Drafts",
		"--to", mailTrashDefaultFolder,
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
		t.Fatalf("protected Trash move made HTTP requests: %+v", srv.Requests())
	}
}

func TestDeleteProtection_BatchMailUpdateToTrashBlocksBeforeNetwork(t *testing.T) {
	for _, selector := range []string{"all", "mail"} {
		for name, input := range map[string]string{
			"provider data body": `[{"resource":"mail","action":"update","params":{"folder":"default0/Drafts","id":"7","data":{"folder_id":"default0/Trash"}}}]`,
			"flattened body":     `[{"resource":"mail","action":"update","params":{"folder":"default0/Drafts","id":"7","folder_id":"default0/Trash"}}]`,
		} {
			t.Run(selector+"/"+name, func(t *testing.T) {
				srv := testutil.NewServer()
				defer srv.Close()

				env := map[string]string{
					"MBX_USERNAME": "fixture-user@example.invalid",
					"MBX_PASSWORD": "pw",
				}
				stdout, stderr, code := runWithConfig(t, srv, env, []string{
					"batch", "run", "--input", input, "--llm",
				}, protectionConfig(selector))

				// Batch planning accepts registered reads only, so the raw mail update is
				// rejected before authentication whatever the policy says.
				if code != 4 {
					t.Fatalf("exit code = %d, want 4 (rejected at batch planning); stderr=%s", code, stderr)
				}
				if stdout != "" {
					t.Fatalf("stdout = %q, want empty", stdout)
				}
				for _, want := range []string{`"code":"invalid_argument"`, "mail.update"} {
					if !strings.Contains(stderr, want) {
						t.Errorf("stderr = %q, want %q", stderr, want)
					}
				}
				if len(srv.Requests()) != 0 {
					t.Fatalf("protected batch Trash move made HTTP requests: %+v", srv.Requests())
				}
			})
		}
	}
}

func TestDeleteProtection_TrashMoveDryRunsRemainAvailableWithoutNetwork(t *testing.T) {
	const batchInput = `[{"resource":"mail","action":"update","params":{"folder":"default0/Drafts","id":"7","data":{"folder_id":"default0/Trash"}}}]`
	paths := []struct {
		name        string
		args        []string
		batchReject bool
	}{
		{
			name: "direct",
			args: []string{
				"mail", "move",
				"--id", "7",
				"--folder", "default0/Drafts",
				"--to", mailTrashDefaultFolder,
				"--dry-run", "--llm",
			},
		},
		{
			name:        "batch",
			args:        []string{"batch", "run", "--input", batchInput, "--dry-run", "--llm"},
			batchReject: true,
		},
	}

	for _, selector := range []string{"all", "mail"} {
		for _, path := range paths {
			t.Run(selector+"/"+path.name, func(t *testing.T) {
				srv := testutil.NewServer()
				defer srv.Close()

				stdout, stderr, code := runWithConfig(
					t, srv, map[string]string{}, path.args, protectionConfig(selector),
				)
				if path.batchReject {
					if code != 4 || !strings.Contains(stderr, "unknown operation") {
						t.Fatalf("batch exit=%d stderr=%s, want raw mutation rejected", code, stderr)
					}
				} else if code != 0 {
					t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
				}
				if !path.batchReject && stderr != "" {
					t.Fatalf("stderr = %q, want empty", stderr)
				}
				if !path.batchReject && !strings.Contains(stdout, `"dry_run":true`) {
					t.Fatalf("stdout = %q, want dry-run result", stdout)
				}
				if len(srv.Requests()) != 0 {
					t.Fatalf("Trash move dry-run made HTTP requests: %+v", srv.Requests())
				}
			})
		}
	}
}

func TestDeleteProtection_OrdinaryMailMovesRemainUnblockedWithoutNetwork(t *testing.T) {
	const batchInput = `[{"resource":"mail","action":"update","params":{"folder":"default0/INBOX","id":"7","data":{"folder_id":"default0/Archive"}}}]`
	paths := []struct {
		name        string
		args        []string
		batchReject bool
	}{
		{
			name: "direct",
			args: []string{
				"mail", "move",
				"--id", "7",
				"--folder", "default0/INBOX",
				"--to", "default0/Archive",
				"--llm",
			},
		},
		{
			name:        "batch",
			args:        []string{"batch", "run", "--input", batchInput, "--llm"},
			batchReject: true,
		},
	}

	for _, selector := range []string{"all", "mail"} {
		for _, path := range paths {
			t.Run(selector+"/"+path.name, func(t *testing.T) {
				srv := testutil.NewServer()
				defer srv.Close()

				stdout, stderr, code := runWithConfig(
					t, srv, map[string]string{}, path.args, protectionConfig(selector),
				)
				if code == 0 {
					t.Fatal("exit code = 0 without credentials, want local credential failure")
				}
				if stdout != "" {
					t.Fatalf("stdout = %q, want empty", stdout)
				}
				if strings.Contains(stderr, "delete_protected") {
					t.Fatalf("stderr = %q, ordinary move must not be delete-protected", stderr)
				}
				if path.batchReject {
					if !strings.Contains(stderr, "unknown operation") {
						t.Fatalf("stderr = %q, want raw batch mutation rejected", stderr)
					}
				} else if !strings.Contains(stderr, "missing credentials") {
					t.Fatalf("stderr = %q, want evidence that the move passed policy evaluation", stderr)
				}
				if len(srv.Requests()) != 0 {
					t.Fatalf("credential-less ordinary move made HTTP requests: %+v", srv.Requests())
				}
			})
		}
	}
}
