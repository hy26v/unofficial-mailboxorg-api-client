// ABOUTME: Checks the Claude Code plugin and marketplace files that start "mbx mcp".
// ABOUTME: Keeps the plugin's copy of the agent skill identical to the canonical skill.
package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const repoRoot = "../.."

func readJSON(t *testing.T, path string, dst any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, path))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestPlugin_ManifestsStartMCPServer(t *testing.T) {
	var marketplace struct {
		Name     string                `json:"name"`
		Owner    struct{ Name string } `json:"owner"`
		Metadata map[string]string     `json:"metadata"`
		Plugins  []struct {
			Name        string `json:"name"`
			Source      string `json:"source"`
			Description string `json:"description"`
		} `json:"plugins"`
	}
	readJSON(t, ".claude-plugin/marketplace.json", &marketplace)
	if len(marketplace.Plugins) != 1 || marketplace.Plugins[0].Source != "./plugins/mbx" || marketplace.Owner.Name == "" {
		t.Fatalf("marketplace = %+v", marketplace)
	}

	var plugin map[string]any
	readJSON(t, "plugins/mbx/.claude-plugin/plugin.json", &plugin)
	if plugin["name"] != marketplace.Plugins[0].Name {
		t.Errorf("plugin name = %v, marketplace entry = %s", plugin["name"], marketplace.Plugins[0].Name)
	}

	var servers struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	readJSON(t, "plugins/mbx/.mcp.json", &servers)
	server, ok := servers.MCPServers["mbx"]
	if !ok || server.Command != "mbx" || !slices.Equal(server.Args, []string{"mcp"}) {
		t.Errorf("plugin MCP servers = %+v, want mbx mcp", servers)
	}
}

// TestPlugin_SkillCopyMatchesCanonicalSkill fails when the canonical skill changes
// without "scripts/sync-plugin-skill.sh".
func TestPlugin_SkillCopyMatchesCanonicalSkill(t *testing.T) {
	canonical := filepath.Join(repoRoot, ".agents/skills/use-mailboxorg-cli")
	copied := filepath.Join(repoRoot, "plugins/mbx/skills/use-mailboxorg-cli")
	files := func(root string) map[string][]byte {
		out := map[string][]byte{}
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if d.Type()&fs.ModeSymlink != 0 {
				t.Errorf("%s is a symlink; plugins install real files only", path)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			out[filepath.ToSlash(rel)] = data
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	want, got := files(canonical), files(copied)
	if len(want) == 0 {
		t.Fatal("canonical skill has no files")
	}
	for name, data := range want {
		if !bytes.Equal(got[name], data) {
			t.Errorf("plugin skill file %s differs from the canonical skill; run scripts/sync-plugin-skill.sh", name)
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("plugin skill has extra file %s; run scripts/sync-plugin-skill.sh", name)
		}
	}
}
