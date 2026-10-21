// ABOUTME: Tests for mapping OX middleware error envelopes into the CLI's structured error contract.
// ABOUTME: Covers code/message/http_status/retryable/retry_after_ms/request_id derivation and JSON shape.
package errors

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestEnvelope_UnmarshalsNumericErrorParams(t *testing.T) {
	raw := []byte(`{"error":"You do not have the appropriate permission to create objects in the folder \"6\".",
		"error_params":[6,2044270,3], "categories":"PERMISSION_DENIED", "category":3,
		"code":"CON-0112", "error_id":"req-4", "error_desc":"Permission denied."}`)

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("Unmarshal failed for numeric error_params: %v", err)
	}
	if len(env.ErrorParams) != 3 {
		t.Errorf("ErrorParams = %+v, want 3 elements", env.ErrorParams)
	}

	cliErr := FromEnvelope(env, 200)
	if cliErr.Code != "CON-0112" {
		t.Errorf("Code = %q, want CON-0112", cliErr.Code)
	}
	if !strings.Contains(cliErr.Message, "permission") {
		t.Errorf("Message = %q, want the permission error text", cliErr.Message)
	}
}

func TestEnvelope_UnmarshalsArrayCategories(t *testing.T) {
	raw := []byte(`{"error":"retry later","code":"SVL-0011","categories":["SERVICE_DOWN","TRY_AGAIN"]}`)

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("Unmarshal failed for array categories: %v", err)
	}
	err := FromEnvelope(env, 503)
	if err.Code != "SVL-0011" {
		t.Errorf("Code = %q, want SVL-0011", err.Code)
	}
	if !err.Retryable {
		t.Error("Retryable = false, want true when categories include TRY_AGAIN")
	}
}

func TestFromEnvelope_MapsFields(t *testing.T) {
	env := Envelope{
		Error:       "The user name or password is incorrect.",
		ErrorParams: []json.RawMessage{},
		Categories:  Categories{"USER_INPUT"},
		Category:    1,
		Code:        "LGI-0006",
		ErrorID:     "1234567890.abcdef",
		ErrorDesc:   "Invalid credentials.",
	}

	err := FromEnvelope(env, 200)

	if err.Code != "LGI-0006" {
		t.Errorf("Code = %q, want %q", err.Code, "LGI-0006")
	}
	if err.Message != "The user name or password is incorrect." {
		t.Errorf("Message = %q, want the error text", err.Message)
	}
	if err.HTTPStatus != 200 {
		t.Errorf("HTTPStatus = %d, want 200", err.HTTPStatus)
	}
	if err.RequestID != "1234567890.abcdef" {
		t.Errorf("RequestID = %q, want error_id", err.RequestID)
	}
}

func TestFromEnvelope_UsesStableCodeWhenProviderOmitsOne(t *testing.T) {
	err := FromEnvelope(Envelope{Error: "provider failed"}, 200)

	if err.Code != "api_error" {
		t.Fatalf("Code = %q, want api_error fallback", err.Code)
	}
	if err.Message != "provider failed" {
		t.Fatalf("Message = %q, want provider message preserved", err.Message)
	}
}

func TestFromEnvelope_TryAgainCategoryIsRetryable(t *testing.T) {
	env := Envelope{
		Error:      "Your session expired. Please login again.",
		Categories: Categories{"TRY_AGAIN"},
		Category:   4,
		Code:       "SES-0203",
		ErrorID:    "req-1",
	}

	err := FromEnvelope(env, 200)

	if !err.Retryable {
		t.Errorf("Retryable = false, want true for TRY_AGAIN category")
	}
}

func TestFromEnvelope_LoginRateLimitCodesAreRetryable(t *testing.T) {
	for _, code := range []string{"LGI-0026", "LGI-0027"} {
		err := FromEnvelope(Envelope{Error: "too many login attempts", Code: code}, 200)
		if !err.Retryable {
			t.Errorf("code %s Retryable = false, want true", code)
		}
	}
}

func TestFromEnvelope_RetryableHTTPStatusRemainsRetryable(t *testing.T) {
	for _, status := range []int{408, 429, 500, 503} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			err := FromEnvelope(Envelope{
				Error:      "temporarily unavailable",
				Categories: Categories{"ERROR"},
				Code:       "SVL-0011",
			}, status)

			if !err.Retryable {
				t.Fatalf("Retryable = false for HTTP %d provider error, want true", status)
			}
		})
	}
}

func TestFromEnvelope_UserInputCategoryNotRetryable(t *testing.T) {
	env := Envelope{
		Categories: Categories{"USER_INPUT"},
		Category:   1,
		Code:       "LGI-0006",
	}

	err := FromEnvelope(env, 200)

	if err.Retryable {
		t.Errorf("Retryable = true, want false for USER_INPUT category")
	}
}

func TestError_ErrorMethod(t *testing.T) {
	err := &Error{Code: "LGI-0006", Message: "bad credentials"}
	if err.Error() != "bad credentials" {
		t.Errorf("Error() = %q, want %q", err.Error(), "bad credentials")
	}
}

func TestError_MarshalJSON_LLMEnvelope(t *testing.T) {
	err := &Error{
		Code:       "not_found",
		Message:    "user usr_123 was not found",
		HTTPStatus: 404,
		Retryable:  false,
		RequestID:  "req_abc",
	}

	b, jsonErr := json.Marshal(err)
	if jsonErr != nil {
		t.Fatalf("Marshal failed: %v", jsonErr)
	}

	var decoded struct {
		Code       string `json:"code"`
		Message    string `json:"message"`
		HTTPStatus int    `json:"http_status"`
		Retryable  bool   `json:"retryable"`
		RequestID  string `json:"request_id"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if decoded.Code != "not_found" || decoded.HTTPStatus != 404 {
		t.Errorf("decoded = %+v, want code=not_found http_status=404", decoded)
	}
}

func TestError_MarshalJSON_OmitsRetryAfterWhenZero(t *testing.T) {
	err := &Error{Code: "x", Message: "y"}
	b, _ := json.Marshal(err)
	if string(b) == "" {
		t.Fatal("empty json")
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if _, ok := m["retry_after_ms"]; ok {
		t.Errorf("retry_after_ms should be omitted when zero, got %v", m["retry_after_ms"])
	}
	if _, ok := m["request_id"]; ok {
		t.Errorf("request_id should be omitted when empty, got %v", m["request_id"])
	}
}

func TestIsErrorEnvelope(t *testing.T) {
	raw := []byte(`{"error":"bad","code":"X"}`)
	if !IsErrorEnvelope(raw) {
		t.Errorf("IsErrorEnvelope = false, want true when error key present")
	}
	raw2 := []byte(`{"data":{"foo":"bar"}}`)
	if IsErrorEnvelope(raw2) {
		t.Errorf("IsErrorEnvelope = true, want false when no error key")
	}
}

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		err  *Error
		want int
	}{
		{&Error{Code: "auth_failed"}, 2},
		{&Error{Code: "LGI-0006"}, 2},
		{&Error{Code: "SES-0203"}, 2},
		{&Error{Code: "http_error", HTTPStatus: 401}, 2},
		{&Error{Code: "not_found"}, 3},
		{&Error{Code: "http_error", HTTPStatus: 404}, 3},
		{&Error{Code: "invalid_argument"}, 4},
		{&Error{Code: "unknown"}, 1},
	}
	for _, c := range cases {
		if got := ExitCodeFor(c.err); got != c.want {
			t.Errorf("ExitCodeFor(%q) = %d, want %d", c.err.Code, got, c.want)
		}
	}
}

func TestIsCertainErrorEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want bool
	}{
		{"full envelope", `{"error":"not found","code":"IFO-0300","categories":"ERROR"}`, true},
		{"code only", `{"error":"boom","code":"IFO-0300"}`, true},
		{"categories only", `{"error":"boom","categories":"TRY_AGAIN"}`, true},
		{"category array", `{"error":"boom","categories":["SERVICE_DOWN","TRY_AGAIN"]}`, true},
		{"numeric category only", `{"error":"boom","category":8}`, true},
		{"error id only", `{"error":"boom","error_id":"1234-5678"}`, true},
		{"stored document with null category", `{"error":"disk full","category":null}`, false},
		{"stored document with an error field", `{"error":"disk full","retries":3}`, false},
		{"stored document with only an error field", `{"error":"disk full"}`, false},
		{"success payload", `{"data":{"id":"7"}}`, false},
		{"not an object", `["error"]`, false},
		{"not json", `PK\x03\x04binary`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCertainErrorEnvelope([]byte(tc.raw)); got != tc.want {
				t.Errorf("IsCertainErrorEnvelope(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestIsSessionRejection(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"unauthorized", &Error{Code: "http_error", HTTPStatus: 401}, true},
		{"session code", &Error{Code: "SES-0203"}, true},
		{"login code", &Error{Code: "LGI-0006"}, true},
		{"wrapped session code", fmt.Errorf("autologin: %w", &Error{Code: "SES-0205"}), true},
		{"login rate limit 26", &Error{Code: "LGI-0026", Retryable: true}, false},
		{"login rate limit 27", &Error{Code: "LGI-0027", Retryable: true}, false},
		{"wrapped login rate limit", fmt.Errorf("autologin: %w", &Error{Code: "LGI-0027"}), false},
		{"invalid cookie", &Error{Code: "SVL-0003", HTTPStatus: 200, Retryable: true}, true},
		{"server error", &Error{Code: "SVL-0011", HTTPStatus: 200}, false},
		{"too many requests", &Error{Code: "http_error", HTTPStatus: 429}, false},
		{"plain error", fmt.Errorf("network down"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSessionRejection(tc.err); got != tc.want {
				t.Errorf("IsSessionRejection(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// mailbox.org answers an expired stored cookie with SVL-0003 and the TRY_AGAIN category,
// but only a new login helps.
func TestInvalidCookieCode_IsAnAuthenticationFailure(t *testing.T) {
	err := FromEnvelope(Envelope{
		Error: "The provided cookie is invalid. Please login.", Code: "SVL-0003", Categories: Categories{"TRY_AGAIN"},
	}, 200)
	if err.Retryable {
		t.Errorf("SVL-0003 retryable = true, want false")
	}
	if code := ExitCodeFor(err); code != 2 {
		t.Errorf("ExitCodeFor(SVL-0003) = %d, want 2", code)
	}
}
