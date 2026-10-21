// ABOUTME: Checks the Claude Desktop extension manifest template in packaging/mcpb.
// ABOUTME: Keeps its command, environment mapping and tool list in line with "mbx mcp".
package main

import (
	"slices"
	"testing"
)

func TestMCPBManifest_StartsMCPServerWithKnownSettings(t *testing.T) {
	var manifest struct {
		ManifestVersion string                `json:"manifest_version"`
		Name            string                `json:"name"`
		DisplayName     string                `json:"display_name"`
		Version         string                `json:"version"`
		Description     string                `json:"description"`
		LongDescription string                `json:"long_description"`
		Author          struct{ Name string } `json:"author"`
		Repository      map[string]string     `json:"repository"`
		Homepage        string                `json:"homepage"`
		Documentation   string                `json:"documentation"`
		Support         string                `json:"support"`
		License         string                `json:"license"`
		Keywords        []string              `json:"keywords"`
		Server          struct {
			Type       string `json:"type"`
			EntryPoint string `json:"entry_point"`
			MCPConfig  struct {
				Command string            `json:"command"`
				Args    []string          `json:"args"`
				Env     map[string]string `json:"env"`
			} `json:"mcp_config"`
		} `json:"server"`
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
		Compatibility struct {
			Platforms []string `json:"platforms"`
		} `json:"compatibility"`
		UserConfig map[string]struct {
			Type        string `json:"type"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Required    bool   `json:"required"`
			Sensitive   bool   `json:"sensitive"`
			Default     any    `json:"default"`
		} `json:"user_config"`
	}
	readJSON(t, "packaging/mcpb/manifest.json", &manifest)

	server := manifest.Server
	if server.Type != "binary" || server.MCPConfig.Command != "${__dirname}/"+server.EntryPoint ||
		!slices.Equal(server.MCPConfig.Args, []string{"mcp"}) {
		t.Errorf("server = %+v, want the bundled binary running mcp", server)
	}
	wantEnv := map[string]string{
		"MBX_USERNAME":  "${user_config.username}",
		"MBX_PASSWORD":  "${user_config.password}",
		"MBX_READ_ONLY": "${user_config.read_only}",
	}
	if len(server.MCPConfig.Env) != len(wantEnv) {
		t.Errorf("env = %v, want %v", server.MCPConfig.Env, wantEnv)
	}
	for key, value := range wantEnv {
		if server.MCPConfig.Env[key] != value {
			t.Errorf("env %s = %q, want %q", key, server.MCPConfig.Env[key], value)
		}
	}

	password := manifest.UserConfig["password"]
	if !password.Sensitive || password.Required || password.Default != "" {
		t.Errorf("password setting = %+v, want sensitive, optional, empty default", password)
	}
	if readOnly := manifest.UserConfig["read_only"]; readOnly.Type != "boolean" || readOnly.Default != true {
		t.Errorf("read_only setting = %+v, want boolean, on by default", readOnly)
	}

	var tools []string
	for _, tool := range manifest.Tools {
		tools = append(tools, tool.Name)
	}
	slices.Sort(tools)
	if want := fullTools; !slices.Equal(tools, want) {
		t.Errorf("tools = %v, want %v", tools, want)
	}
}
