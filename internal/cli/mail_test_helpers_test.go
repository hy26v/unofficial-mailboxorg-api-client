// ABOUTME: Shared safe mail-row fixtures for CLI tests that exercise metadata-only decoders.
// ABOUTME: The tuple includes every optional column without exposing raw message bodies.
package cli

func safeMailRow(id, folder string) []any {
	return []any{
		id, folder, "safe subject",
		[]any{[]any{"Sender", "sender@example.org"}},
		[]any{[]any{"Recipient", "recipient@example.net"}},
		[]any{}, int64(1234), int64(1700000000000), int64(1700000001000), false, int64(mailFlagSeen),
	}
}
