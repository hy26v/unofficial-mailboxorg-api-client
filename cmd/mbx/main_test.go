// ABOUTME: Verifies that the shipped CLI, not just the client library, enables request guarding.
// ABOUTME: Pins the shared state path and conservative production pacing defaults.
package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/requestguard"
)

func TestNewHTTPClientEnablesSharedRequestGuard(t *testing.T) {
	root := t.TempDir()
	client := newHTTPClient(root)
	guard, ok := client.Transport.(*requestguard.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *requestguard.Transport", client.Transport)
	}
	if want := filepath.Join(root, "request-guard.json"); guard.StatePath != want {
		t.Errorf("state path = %q, want %q", guard.StatePath, want)
	}
	if guard.MinInterval != requestguard.DefaultMinInterval {
		t.Errorf("minimum interval = %v, want %v", guard.MinInterval, requestguard.DefaultMinInterval)
	}
	if guard.InitialLoginBackoff != requestguard.DefaultInitialLoginBackoff ||
		guard.MaxLoginBackoff != requestguard.DefaultMaxLoginBackoff {
		t.Errorf("login backoff = %v..%v, want %v..%v",
			guard.InitialLoginBackoff, guard.MaxLoginBackoff,
			requestguard.DefaultInitialLoginBackoff, requestguard.DefaultMaxLoginBackoff)
	}
}

func TestNewTerminalPasswordPrompt_NonTerminalDoesNotOfferPrompt(t *testing.T) {
	readCalled := false
	prompt := newTerminalPasswordPrompt(
		os.Stdin,
		&bytes.Buffer{},
		func(int) bool { return false },
		func(int) ([]byte, error) {
			readCalled = true
			return nil, nil
		},
	)

	if prompt != nil {
		t.Fatal("non-terminal stdin returned a prompt")
	}
	if readCalled {
		t.Fatal("password reader was called for non-terminal stdin")
	}
}

func TestTerminalLoginPrompts_NonTerminalOffersNoInteractiveInput(t *testing.T) {
	prompts := newTerminalLoginPrompts(
		os.Stdin,
		&bytes.Buffer{},
		func(int) bool { return false },
		func(int) ([]byte, error) { return nil, errors.New("must not read") },
	)
	if prompts.Username != nil || prompts.Password != nil || prompts.Secret != nil {
		t.Fatalf("non-terminal prompts = %#v, want no interactive inputs", prompts)
	}
}

func TestNewTerminalUsernamePrompt_ReadsVisibleLine(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	defer reader.Close()
	if _, err := writer.WriteString("  first-run@example.org  \n"); err != nil {
		writer.Close()
		t.Fatalf("write username input: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close username input: %v", err)
	}

	var stderr bytes.Buffer
	prompt := newTerminalUsernamePrompt(reader, &stderr, func(int) bool { return true })
	username, err := prompt()
	if err != nil {
		t.Fatalf("username prompt failed: %v", err)
	}
	if username != "  first-run@example.org  " {
		t.Fatalf("username = %q, want terminal line", username)
	}
	if stderr.String() != "mailbox.org email: " {
		t.Fatalf("prompt output = %q", stderr.String())
	}
}

func TestReadTerminalUsername_HandlesCRLFAndBoundsInput(t *testing.T) {
	username, err := readTerminalUsername(strings.NewReader("user@example.org\r\nignored"))
	if err != nil || username != "user@example.org" {
		t.Fatalf("CRLF username = %q, %v", username, err)
	}

	tooLong := strings.Repeat("x", maxTerminalUsernameInput+1) + "\n"
	if _, err := readTerminalUsername(strings.NewReader(tooLong)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("long-input error = %v", err)
	}
}

func TestNewTerminalPasswordPrompt_ReadsAndClearsTerminalBytes(t *testing.T) {
	rawPassword := []byte("terminal-secret")
	var stderr bytes.Buffer
	seenFD := -1
	prompt := newTerminalPasswordPrompt(
		os.Stdin,
		&stderr,
		func(int) bool { return true },
		func(fd int) ([]byte, error) {
			seenFD = fd
			return rawPassword, nil
		},
	)

	password, err := prompt()
	if err != nil {
		t.Fatalf("prompt failed: %v", err)
	}
	if password != "terminal-secret" {
		t.Fatalf("password = %q, want terminal value", password)
	}
	if seenFD != int(os.Stdin.Fd()) {
		t.Fatalf("read fd = %d, want %d", seenFD, os.Stdin.Fd())
	}
	if stderr.String() != "mailbox.org password: \n" {
		t.Fatalf("prompt output = %q", stderr.String())
	}
	for i, value := range rawPassword {
		if value != 0 {
			t.Fatalf("password byte %d was not cleared", i)
		}
	}
}

func TestNewTerminalPasswordPrompt_ReadFailureDoesNotExposeSecret(t *testing.T) {
	var stderr bytes.Buffer
	prompt := newTerminalPasswordPrompt(
		os.Stdin,
		&stderr,
		func(int) bool { return true },
		func(int) ([]byte, error) { return []byte("partial-secret"), errors.New("interrupted") },
	)

	password, err := prompt()
	if err == nil || password != "" {
		t.Fatalf("password=%q error=%v, want empty password and error", password, err)
	}
	if strings.Contains(stderr.String(), "partial-secret") || strings.Contains(err.Error(), "partial-secret") {
		t.Fatal("terminal read failure exposed password bytes")
	}
}

func TestNewTerminalPasswordPrompt_RejectsEmptyPassword(t *testing.T) {
	var stderr bytes.Buffer
	prompt := newTerminalPasswordPrompt(
		os.Stdin,
		&stderr,
		func(int) bool { return true },
		func(int) ([]byte, error) { return []byte{}, nil },
	)

	password, err := prompt()
	if err == nil || password != "" {
		t.Fatalf("password=%q error=%v, want empty-password error", password, err)
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %q, want empty-password guidance", err)
	}
}

func TestNewTerminalSecretPrompt_ShowsLabelAndClearsBytes(t *testing.T) {
	rawPassword := []byte("new-terminal-secret")
	var stderr bytes.Buffer
	prompt := newTerminalSecretPrompt(
		os.Stdin,
		&stderr,
		func(int) bool { return true },
		func(int) ([]byte, error) { return rawPassword, nil },
	)
	if prompt == nil {
		t.Fatal("terminal stdin returned no secret prompt")
	}

	value, err := prompt("New mailbox.org password: ")
	if err != nil || value != "new-terminal-secret" {
		t.Fatalf("prompt = %q, %v; want terminal value", value, err)
	}
	if stderr.String() != "New mailbox.org password: \n" {
		t.Fatalf("prompt output = %q", stderr.String())
	}
	for i, b := range rawPassword {
		if b != 0 {
			t.Fatalf("secret byte %d was not cleared", i)
		}
	}
}
