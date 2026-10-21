// ABOUTME: Self-tests for the testutil fake server and CLI-invocation helpers.
package testutil

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestServer_HandleAndRequests(t *testing.T) {
	s := NewServer()
	defer s.Close()

	s.Handle("/ajax/login", "login", Response{Body: map[string]string{"session": "abc"}})

	resp, err := http.Get(s.URL + "/ajax/login?action=login")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"session":"abc"`) {
		t.Errorf("body = %s, want session abc", body)
	}

	reqs := s.Requests()
	if len(reqs) != 1 || reqs[0].URL.Path != "/ajax/login" {
		t.Errorf("Requests() = %+v, want one request to /ajax/login", reqs)
	}
}

func TestServer_UnregisteredReturns404(t *testing.T) {
	s := NewServer()
	defer s.Close()

	resp, err := http.Get(s.URL + "/ajax/unknown?action=x")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRunCLI_CapturesOutputAndExitCode(t *testing.T) {
	run := func(stdin *strings.Reader, stdout, stderr *bytes.Buffer, args []string) int {
		stdout.WriteString("out:" + args[0])
		stderr.WriteString("err")
		return 7
	}

	res := RunCLI(run, "", []string{"hello"})

	if res.Stdout != "out:hello" || res.Stderr != "err" || res.ExitCode != 7 {
		t.Errorf("res = %+v, want stdout=out:hello stderr=err exit=7", res)
	}
}
