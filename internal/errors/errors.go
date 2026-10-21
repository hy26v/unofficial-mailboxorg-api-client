// ABOUTME: Structured error contract for the CLI, and the mapping from the OX middleware's
// ABOUTME: error/error_params/categories/category/code/error_id/error_desc envelope into it.
package errors

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Envelope is the OX middleware's error response shape.
type Envelope struct {
	Error string `json:"error"`
	// ErrorParams elements are not guaranteed to be strings (e.g. folder/context/user ids
	// come back as numbers), so this decodes as raw JSON rather than []string.
	ErrorParams []json.RawMessage `json:"error_params"`
	Categories  Categories        `json:"categories"`
	Category    int               `json:"category"`
	Code        string            `json:"code"`
	ErrorID     string            `json:"error_id"`
	ErrorDesc   string            `json:"error_desc"`
}

// Categories accepts both shapes used by the OX error contract: older responses carry
// one category string, while newer responses may carry an array of category strings.
type Categories []string

func (c *Categories) UnmarshalJSON(data []byte) error {
	if strings.TrimSpace(string(data)) == "null" {
		*c = nil
		return nil
	}
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*c = Categories{single}
		return nil
	}
	var multiple []string
	if err := json.Unmarshal(data, &multiple); err != nil {
		return err
	}
	*c = multiple
	return nil
}

func (c Categories) contains(want string) bool {
	for _, category := range c {
		if category == want {
			return true
		}
	}
	return false
}

func (c Categories) hasValue() bool {
	for _, category := range c {
		if strings.TrimSpace(category) != "" {
			return true
		}
	}
	return false
}

// Error is the CLI's stable, structured error contract.
type Error struct {
	Code          string
	Message       string
	HTTPStatus    int
	Retryable     bool
	RetryAfterMs  int
	RequestID     string
	AllowedFields []string
	Suggestion    string
}

func (e *Error) Error() string {
	return e.Message
}

func (e *Error) MarshalJSON() ([]byte, error) {
	out := struct {
		Code          string   `json:"code"`
		Message       string   `json:"message"`
		HTTPStatus    int      `json:"http_status"`
		Retryable     bool     `json:"retryable"`
		RetryAfterMs  int      `json:"retry_after_ms,omitempty"`
		RequestID     string   `json:"request_id,omitempty"`
		AllowedFields []string `json:"allowed_fields,omitempty"`
		Suggestion    string   `json:"suggestion,omitempty"`
	}{
		Code:          e.Code,
		Message:       e.Message,
		HTTPStatus:    e.HTTPStatus,
		Retryable:     e.Retryable,
		RetryAfterMs:  e.RetryAfterMs,
		RequestID:     e.RequestID,
		AllowedFields: e.AllowedFields,
		Suggestion:    e.Suggestion,
	}
	return json.Marshal(out)
}

// FromEnvelope maps an OX error envelope into the CLI's structured error contract.
func FromEnvelope(env Envelope, httpStatus int) *Error {
	code := env.Code
	if code == "" {
		code = "api_error"
	}
	return &Error{
		Code:       code,
		Message:    env.Error,
		HTTPStatus: httpStatus,
		Retryable: !IsInvalidCookieCode(code) && (env.Categories.contains("TRY_AGAIN") ||
			IsLoginRateLimitCode(code) ||
			httpStatus == http.StatusRequestTimeout ||
			httpStatus == http.StatusTooManyRequests ||
			(httpStatus >= http.StatusInternalServerError && httpStatus < 600)),
		RequestID: env.ErrorID,
	}
}

// IsLoginRateLimitCode reports whether code is one of the OX login rate-limit codes. They
// share the LGI- prefix with credential rejections but mean "try again later", not "this
// login or session is invalid".
func IsLoginRateLimitCode(code string) bool {
	return code == "LGI-0026" || code == "LGI-0027"
}

// IsInvalidCookieCode reports whether code is SVL-0003, "The provided cookie is invalid.
// Please login.", which mailbox.org returns for an expired stored session. The provider
// marks it TRY_AGAIN, but a retry cannot help: only a new login does.
func IsInvalidCookieCode(code string) bool {
	return code == "SVL-0003"
}

// IsSessionRejection reports whether err is the provider refusing a session or its
// cookies: HTTP 401, an SES- code, SVL-0003, or an LGI- code other than the login
// rate-limit codes.
// Callers use it to decide that a stored session is no longer usable. Transport failures,
// server errors and rate limits are not rejections: they say nothing about the session,
// and reacting to them with a credential login would add load during a rate limit.
func IsSessionRejection(err error) bool {
	var cerr *Error
	if !errors.As(err, &cerr) || cerr == nil {
		return false
	}
	if IsLoginRateLimitCode(cerr.Code) {
		return false
	}
	return cerr.HTTPStatus == http.StatusUnauthorized || IsInvalidCookieCode(cerr.Code) ||
		strings.HasPrefix(cerr.Code, "SES-") || strings.HasPrefix(cerr.Code, "LGI-")
}

// IsErrorEnvelope reports whether raw JSON contains a non-empty top-level "error" key,
// which is the OX middleware's failure signal (independent of HTTP status).
func IsErrorEnvelope(raw []byte) bool {
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Error != ""
}

// IsCertainErrorEnvelope reports whether raw JSON is an OX failure response, judged
// strictly enough to apply to a body that may instead be arbitrary user content. Beyond a
// non-empty "error" it requires one of the fields the middleware always sets to identify a
// failure, none of which an ordinary stored document would carry together with "error".
// Callers that unwrap a JSON envelope should use IsErrorEnvelope; this is for callers that
// return a raw document body, where a stored file whose contents merely happen to include
// an "error" key must still be handed back to the user.
func IsCertainErrorEnvelope(raw []byte) bool {
	if !IsErrorEnvelope(raw) {
		return false
	}
	var probe struct {
		Categories Categories `json:"categories"`
		Category   *int       `json:"category"`
		Code       string     `json:"code"`
		ErrorID    string     `json:"error_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return probe.Categories.hasValue() || probe.Category != nil || probe.Code != "" || probe.ErrorID != ""
}

// ExitCodeFor maps a structured error's code to a broad, documented process exit code.
func ExitCodeFor(err *Error) int {
	switch {
	case err.Code == "auth_failed", strings.HasPrefix(err.Code, "LGI-"), strings.HasPrefix(err.Code, "SES-"),
		IsInvalidCookieCode(err.Code), err.HTTPStatus == http.StatusUnauthorized:
		return 2
	case err.Code == "not_found", err.HTTPStatus == http.StatusNotFound:
		return 3
	case err.Code == "invalid_argument":
		return 4
	default:
		return 1
	}
}
