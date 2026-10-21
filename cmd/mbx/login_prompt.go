// ABOUTME: Reads login fields and other hidden values (such as change-password input) from a terminal.
// ABOUTME: Persists only the username while keeping every hidden password process-local.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/cli"
	"golang.org/x/term"
)

const maxTerminalUsernameInput = 1024

func terminalLoginPrompts(stdin *os.File, stderr io.Writer) cli.LoginPrompts {
	return newTerminalLoginPrompts(stdin, stderr, term.IsTerminal, term.ReadPassword)
}

func newTerminalLoginPrompts(
	stdin *os.File,
	stderr io.Writer,
	isTerminal func(int) bool,
	readPassword func(int) ([]byte, error),
) cli.LoginPrompts {
	return cli.LoginPrompts{
		Username: newTerminalUsernamePrompt(stdin, stderr, isTerminal),
		Password: newTerminalPasswordPrompt(stdin, stderr, isTerminal, readPassword),
		Secret:   newTerminalSecretPrompt(stdin, stderr, isTerminal, readPassword),
	}
}

func newTerminalUsernamePrompt(
	stdin *os.File,
	stderr io.Writer,
	isTerminal func(int) bool,
) cli.UsernamePrompt {
	if stdin == nil || stderr == nil || isTerminal == nil || !isTerminal(int(stdin.Fd())) {
		return nil
	}
	return func() (string, error) {
		_, _ = io.WriteString(stderr, "mailbox.org email: ")
		username, err := readTerminalUsername(stdin)
		if err != nil {
			return "", fmt.Errorf("read from terminal: %w", err)
		}
		return username, nil
	}
}

func readTerminalUsername(reader io.Reader) (string, error) {
	input := make([]byte, 0, 128)
	buffer := []byte{0}
	tooLong := false
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			switch buffer[0] {
			case '\n':
				if tooLong {
					return "", fmt.Errorf("input exceeds %d bytes", maxTerminalUsernameInput)
				}
				return string(input), nil
			case '\r':
				// Ignore the CR in a Windows-style CRLF terminal line.
			default:
				if len(input) < maxTerminalUsernameInput {
					input = append(input, buffer[0])
				} else {
					tooLong = true
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if tooLong {
					return "", fmt.Errorf("input exceeds %d bytes", maxTerminalUsernameInput)
				}
				if len(input) > 0 {
					return string(input), nil
				}
			}
			return "", err
		}
	}
}

func newTerminalPasswordPrompt(
	stdin *os.File,
	stderr io.Writer,
	isTerminal func(int) bool,
	readPassword func(int) ([]byte, error),
) cli.PasswordPrompt {
	secret := newTerminalSecretPrompt(stdin, stderr, isTerminal, readPassword)
	if secret == nil {
		return nil
	}
	return func() (string, error) {
		return secret("mailbox.org password: ")
	}
}

// newTerminalSecretPrompt reads one hidden value after showing a label on stderr. It
// returns nil when stdin is not a terminal, so non-interactive callers never block.
func newTerminalSecretPrompt(
	stdin *os.File,
	stderr io.Writer,
	isTerminal func(int) bool,
	readPassword func(int) ([]byte, error),
) cli.SecretPrompt {
	if stdin == nil || stderr == nil || isTerminal == nil || readPassword == nil {
		return nil
	}
	fd := int(stdin.Fd())
	if !isTerminal(fd) {
		return nil
	}

	return func(label string) (string, error) {
		_, _ = io.WriteString(stderr, label)
		passwordBytes, err := readPassword(fd)
		_, _ = fmt.Fprintln(stderr)
		if err != nil {
			clear(passwordBytes)
			return "", fmt.Errorf("read from terminal: %w", err)
		}
		if len(passwordBytes) == 0 {
			return "", errors.New("password cannot be empty")
		}

		password := string(passwordBytes)
		clear(passwordBytes)
		return password, nil
	}
}
