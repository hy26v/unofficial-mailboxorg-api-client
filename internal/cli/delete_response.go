// ABOUTME: Shared reading of an OX action=delete response, whose success body names the
// ABOUTME: objects the server declined to delete rather than failing the request outright.
package cli

import (
	"encoding/json"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

// deleteRefusalError reports whether an OX action=delete response declined the deletion.
// The middleware does not fail such a request: it answers 200 with an array naming the
// objects it did not delete, which is how a timestamp older than the object's last change
// reaches the caller. A body that is not an array carries no refusal list, so it is read as
// a plain success; that is the shape some modules return for an accepted delete.
func deleteRefusalError(data []byte, subject string) *cliErrors.Error {
	var refused []json.RawMessage
	if err := json.Unmarshal(data, &refused); err != nil || len(refused) == 0 {
		return nil
	}
	return &cliErrors.Error{
		Code: "api_error",
		Message: subject + " was not deleted: the server returned it as declined, which normally means it " +
			"changed after the timestamp that was sent. Read it again and retry with the current timestamp.",
	}
}
