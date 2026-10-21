// ABOUTME: Bounded fuzz/property targets for response envelopes and persisted sessions.
// ABOUTME: They guard false-success classification, exact payloads, cookies, and redaction.
package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func FuzzEnvelopeDecoding(f *testing.F) {
	for _, seed := range []struct {
		body   string
		status uint16
	}{
		{`{"data":{"id":"1"}}`, http.StatusOK},
		{`{"data":null}`, http.StatusOK},
		{`{}`, http.StatusOK},
		{`null`, http.StatusOK},
		{`{"error":"denied","code":"CAL-0001","categories":"USER_INPUT"}`, http.StatusOK},
		{`{"error":{"message":"wrong shape"},"data":{}}`, http.StatusOK},
		{`{"data":{"id":"1"},"timestamp":9007199254740993}`, http.StatusOK},
		{`{"data":{},"timestamp":0}`, http.StatusOK},
		{`{"data":{}}`, http.StatusInternalServerError},
		{`not json`, http.StatusOK},
	} {
		f.Add([]byte(seed.body), seed.status)
	}

	f.Fuzz(func(t *testing.T, body []byte, statusSeed uint16) {
		if len(body) > 64<<10 {
			t.Skip()
		}
		status := int(statusSeed)
		if status < 100 || status > 599 {
			status = 200 + status%400
		}

		envelopeErr := responseEnvelopeError(body, status)
		statusErr := httpStatusError(status)
		payload, dataErr := decodeDataEnvelope(body, status)

		var object map[string]json.RawMessage
		objectErr := json.Unmarshal(body, &object)
		if objectErr == nil && object != nil {
			if rawError, ok := object["error"]; ok {
				var message string
				if json.Unmarshal(rawError, &message) == nil && message != "" && envelopeErr == nil {
					t.Fatalf("non-empty provider error was not classified: %s", body)
				}
			}
		}

		if envelopeErr == nil && statusErr == nil && dataErr == nil {
			if objectErr != nil || object == nil {
				t.Fatalf("malformed or null response became success: %s", body)
			}
			rawData, present := object["data"]
			if !present {
				if len(payload) != 0 {
					t.Fatalf("missing data produced payload %q", payload)
				}
			} else if !bytes.Equal(bytes.TrimSpace(payload), bytes.TrimSpace(rawData)) {
				t.Fatalf("payload changed: got %q want %q", payload, rawData)
			}
		}

		timestampedPayload, timestamp, timestampErr := decodeTimestampedDataEnvelope(body, status)
		if timestampErr == nil {
			if objectErr != nil || object == nil {
				t.Fatalf("timestamped decoder accepted non-object: %s", body)
			}
			rawData, dataPresent := object["data"]
			rawTimestamp, timestampPresent := object["timestamp"]
			var decodedTimestamp int64
			if !dataPresent || !timestampPresent || json.Unmarshal(rawTimestamp, &decodedTimestamp) != nil || decodedTimestamp <= 0 {
				t.Fatalf("timestamped decoder accepted missing/invalid contract: %s", body)
			}
			if timestamp != decodedTimestamp || !bytes.Equal(bytes.TrimSpace(timestampedPayload), bytes.TrimSpace(rawData)) {
				t.Fatalf("timestamped payload changed: payload=%q timestamp=%d", timestampedPayload, timestamp)
			}
		}
	})
}

func FuzzSessionStoreLoad(f *testing.F) {
	for _, seed := range []string{
		`{"id":"sess-1","cookies":[{"name":"JSESSIONID","value":"secret"}]}`,
		`{"id":"sess-1","cookies":[]}`,
		`{"id":"","cookies":[{"name":"active","value":"v"}]}`,
		`{"id":"sess-1","cookies":[{"name":"","value":"secret"}]}`,
		`{"id":"sess-1","cookies":[],"unknown":true}`,
		`{"id":"sess-1","cookies":[]} {"id":"second","cookies":[]}`,
		`null`,
		`not json`,
	} {
		f.Add([]byte(seed))
	}

	root := f.TempDir()
	path := filepath.Join(root, "session.json")
	roundTripPath := filepath.Join(root, "roundtrip.json")
	f.Fuzz(func(t *testing.T, document []byte) {
		if len(document) > 128<<10 {
			t.Skip()
		}
		if err := os.WriteFile(path, document, 0o600); err != nil {
			t.Fatal(err)
		}

		store := &SessionStore{Path: path}
		session, err := store.Load()
		if err != nil {
			return
		}

		wantString := "Session{empty}"
		if session.ID != "" {
			wantString = "Session{ID: [REDACTED], Cookies: [REDACTED]}"
		}
		if got := session.String(); got != wantString {
			t.Fatalf("Session.String() = %q, want fixed redacted form %q", got, wantString)
		}
		now := time.Now()
		for _, cookie := range session.Cookies {
			if cookie == nil {
				t.Fatal("successful session load returned a nil cookie")
			}
			if err := cookie.Valid(); err != nil {
				t.Fatalf("successful session load returned invalid cookie: %v", err)
			}
			if cookieExpiredAt(cookie, now) {
				t.Fatal("successful session load retained an expired cookie")
			}
		}

		roundTrip := &SessionStore{Path: roundTripPath}
		if err := roundTrip.Save(session); err != nil {
			t.Fatalf("saving a successfully loaded session failed: %v", err)
		}
		loadedAgain, err := roundTrip.Load()
		if err != nil {
			t.Fatalf("loading a saved valid session failed: %v", err)
		}
		if loadedAgain.ID != session.ID {
			t.Fatalf("session id changed across save/load: got %q want %q", loadedAgain.ID, session.ID)
		}
	})
}
