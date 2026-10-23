// ABOUTME: End-to-end tests for "mbx mcp": builds the real binary and talks MCP to it over stdio.
// ABOUTME: MBX_MCP_TEST_BINARY tests a prebuilt binary instead, such as one unpacked from an .mcpb.
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestParseMCPFlags(t *testing.T) {
	if f, err := parseMCPFlags(nil); err != nil || f.readOnly {
		t.Errorf("no flags = %+v, %v", f, err)
	}
	if f, err := parseMCPFlags([]string{"--read-only"}); err != nil || !f.readOnly {
		t.Errorf("--read-only = %+v, %v", f, err)
	}
	if _, err := parseMCPFlags([]string{"--bogus"}); err == nil {
		t.Error("unknown flag accepted")
	}
	if _, err := parseMCPFlags([]string{"extra"}); err == nil {
		t.Error("positional argument accepted")
	}
}

// mbxBinary returns MBX_MCP_TEST_BINARY, or builds mbx into a temporary directory.
func mbxBinary(t *testing.T) string {
	t.Helper()
	if prebuilt := os.Getenv("MBX_MCP_TEST_BINARY"); prebuilt != "" {
		return prebuilt
	}
	if testing.Short() {
		t.Skip("builds the mbx binary")
	}
	name := "mbx"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
	return binary
}

// connectBinary starts "mbx mcp" with an isolated MBX_HOME and no credentials.
func connectBinary(t *testing.T, binary string, config string, args ...string) *mcp.ClientSession {
	t.Helper()
	return connectBinaryWithEnv(t, binary, config, nil, args...)
}

func connectBinaryWithEnv(t *testing.T, binary string, config string, env []string, args ...string) *mcp.ClientSession {
	t.Helper()
	home := t.TempDir()
	if config != "" {
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(binary, append([]string{"mcp"}, args...)...)
	cmd.Env = append([]string{"MBX_HOME=" + home, "PATH=" + os.Getenv("PATH")}, env...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "v0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to mbx mcp: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func toolNames(t *testing.T, session *mcp.ClientSession) []string {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

func callText(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", tool, err)
	}
	var texts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	return strings.Join(texts, "\n"), result.IsError
}

// readOnlyTools and fullTools are the tools of "mbx mcp" with the default task tools.
var (
	readOnlyTools = []string{"mbx_commands", "mbx_contacts_list", "mbx_describe", "mbx_events_list", "mbx_mail_attachment", "mbx_mail_correspondents", "mbx_mail_list", "mbx_mail_read", "mbx_mail_search", "mbx_mail_senders", "mbx_mail_thread", "mbx_read"}
	fullTools     = []string{"mbx_commands", "mbx_contacts_list", "mbx_delete", "mbx_describe", "mbx_events_list", "mbx_mail_attachment", "mbx_mail_correspondents", "mbx_mail_draft", "mbx_mail_list", "mbx_mail_move", "mbx_mail_read", "mbx_mail_search", "mbx_mail_senders", "mbx_mail_thread", "mbx_read", "mbx_write"}
)

func TestMCPBinary_StdioSession(t *testing.T) {
	binary := mbxBinary(t)

	t.Run("full", func(t *testing.T) {
		session := connectBinary(t, binary, "[safety]\ndelete_protection = [\"all\"]\n")
		if info := session.InitializeResult().ServerInfo; info.Name != "mbx" {
			t.Errorf("server name = %q, want mbx", info.Name)
		}
		if got, want := toolNames(t, session), fullTools; !slices.Equal(got, want) {
			t.Errorf("tools = %v, want %v", got, want)
		}
		text, isError := callText(t, session, "mbx_describe", map[string]any{"operation": "mail.list"})
		if isError || !strings.Contains(text, `"name":"mail.list"`) {
			t.Errorf("describe = %s", text)
		}
		// The delete is refused by config before authentication or network access.
		text, isError = callText(t, session, "mbx_delete", map[string]any{
			"operation": "mail.delete", "args": map[string]any{"folder": "default0/INBOX", "id": "1", "confirm": "1"},
		})
		if !isError || !strings.Contains(text, "delete_protected") {
			t.Errorf("protected delete = %s", text)
		}
	})

	t.Run("read-only flag", func(t *testing.T) {
		session := connectBinary(t, binary, "", "--read-only")
		if got, want := toolNames(t, session), readOnlyTools; !slices.Equal(got, want) {
			t.Errorf("tools = %v, want %v", got, want)
		}
	})

	t.Run("without task tools", func(t *testing.T) {
		session := connectBinary(t, binary, "", "--task-tools=false")
		want := []string{"mbx_commands", "mbx_delete", "mbx_describe", "mbx_read", "mbx_write"}
		if got := toolNames(t, session); !slices.Equal(got, want) {
			t.Errorf("tools = %v, want %v", got, want)
		}
	})

	// Claude Desktop passes the extension settings from packaging/mcpb/manifest.json as
	// environment variables, with an empty password when none is set.
	t.Run("desktop extension environment", func(t *testing.T) {
		desktop := func(readOnly string) []string {
			return []string{"MBX_USERNAME=fixture-user@example.invalid", "MBX_PASSWORD=", "MBX_READ_ONLY=" + readOnly}
		}
		session := connectBinaryWithEnv(t, binary, "", desktop("true"))
		if got := toolNames(t, session); !slices.Equal(got, readOnlyTools) {
			t.Errorf("read_only=true tools = %v", got)
		}
		session = connectBinaryWithEnv(t, binary, "", desktop("false"))
		if got := toolNames(t, session); !slices.Equal(got, fullTools) {
			t.Errorf("read_only=false tools = %v", got)
		}
	})

	t.Run("read-only config", func(t *testing.T) {
		session := connectBinary(t, binary, "[safety]\nread_only = true\n")
		if got := toolNames(t, session); slices.Contains(got, "mbx_write") {
			t.Errorf("tools = %v, want no write tool", got)
		}
	})
}

func TestMCPBinary_HelpAndBadFlags(t *testing.T) {
	binary := mbxBinary(t)
	home := t.TempDir()
	env := []string{"MBX_HOME=" + home}

	help := exec.Command(binary, "mcp", "--help")
	help.Env = env
	output, err := help.Output()
	if err != nil || !strings.Contains(string(output), "mbx_read") {
		t.Errorf("mbx mcp --help = %q, %v", output, err)
	}

	bad := exec.Command(binary, "mcp", "--bogus")
	bad.Env = env
	output, err = bad.CombinedOutput()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 4 || !strings.Contains(string(output), "bogus") {
		t.Errorf("mbx mcp --bogus = %q, %v", output, err)
	}
}
