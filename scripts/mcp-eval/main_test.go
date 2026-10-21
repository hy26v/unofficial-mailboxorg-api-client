// ABOUTME: Tests the MCP evaluation without a model: an ideal call sequence per task must pass its checks.
// ABOUTME: Keeps the fake mailbox, the task file and the checks working with the real MCP server.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type step struct {
	tool string
	args map[string]any
}

var idealCalls = map[string][]step{
	"senders":          {{"mbx_mail_senders", nil}},
	"draft-from-alias": {{"mbx_mail_draft", map[string]any{"from": "support@example.org", "to": []string{"bob@example.com"}, "subject": "Project kickoff", "body": "Hi Bob. Shall we meet next week?"}}},
	"invoice-amount": {
		{"mbx_mail_search", map[string]any{"query": "invoice acme"}},
		{"mbx_mail_read", map[string]any{"folder": "default0/INBOX", "id": "101"}},
	},
	"calendar":         {{"mbx_events_list", nil}},
	"trash-newsletter": {{"mbx_mail_move", map[string]any{"folder": "default0/INBOX", "id": "102", "to": "default0/Trash"}}},
	"reply-draft": {
		{"mbx_mail_read", map[string]any{"folder": "default0/INBOX", "id": "103"}},
		{"mbx_mail_draft", map[string]any{"to": []string{"alice@example.net"}, "subject": "Re: Lunch on Friday?", "body": "Yes, see you then."}},
	},
	"correspondent-address": {{"mbx_mail_correspondents", map[string]any{"query": "acme billing"}}},
	"contact-email":         {{"mbx_contacts_list", nil}},
}

func TestIdealCallsPassEveryTask(t *testing.T) {
	data, err := os.ReadFile("tasks.json")
	if err != nil {
		t.Fatal(err)
	}
	var tasks []task
	if err := json.Unmarshal(data, &tasks); err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		t.Run(tk.Name, func(t *testing.T) {
			steps, ok := idealCalls[tk.Name]
			if !ok {
				t.Fatalf("no ideal calls for task %s", tk.Name)
			}
			logPath := filepath.Join(t.TempDir(), "requests.jsonl")
			fake, err := newFakeServer(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer fake.Close()
			server := newEvalServer(fake, t.TempDir(), true)
			serverTransport, clientTransport := mcp.NewInMemoryTransports()
			ctx := context.Background()
			serverSession, err := server.Connect(ctx, serverTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer serverSession.Close()
			session, err := mcp.NewClient(&mcp.Implementation{Name: "eval-test", Version: "v0"}, nil).Connect(ctx, clientTransport, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()

			var answer strings.Builder
			for _, s := range steps {
				res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: s.tool, Arguments: s.args})
				if err != nil {
					t.Fatalf("%s: %v", s.tool, err)
				}
				for _, content := range res.Content {
					answer.WriteString(content.(*mcp.TextContent).Text + "\n")
				}
				if res.IsError {
					t.Fatalf("%s failed: %s", s.tool, answer.String())
				}
			}
			requests, err := readLog(logPath)
			if err != nil {
				t.Fatal(err)
			}
			for i, c := range tk.Checks {
				if !c.passes(requests, answer.String()) {
					t.Errorf("check %d failed; output: %s", i+1, answer.String())
				}
			}
		})
	}
}

func TestParseStreamCountsToolCallsAndErrors(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"x"},{"type":"tool_use","name":"mcp__mbx__mbx_mail_search"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":[{"type":"text","text":"bad argument"}]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__mbx__mbx_mail_read"}]}}`,
		`{"type":"result","result":"EUR 1,234.56","num_turns":3,"total_cost_usd":0.01}`,
	}, "\n")
	r := parseStream([]byte(stream))
	if r.ToolCalls != 2 || r.ToolErrors != 1 || r.Tools["mbx_mail_read"] != 1 || r.Answer != "EUR 1,234.56" || r.Turns != 3 || len(r.Errors) != 1 || r.Errors[0] != "bad argument" {
		t.Fatalf("parsed = %+v", r)
	}
}
