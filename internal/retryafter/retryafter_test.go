// ABOUTME: Tests Retry-After parsing, the MaxDelay cap and millisecond rounding.
// ABOUTME: Both header forms (seconds and HTTP date) are covered, including huge values.
package retryafter

import (
	"net/http"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, time.August, 13, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"seconds", " 5 ", 5 * time.Second},
		{"http date", now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second},
		{"past date", now.Add(-time.Second).Format(http.TimeFormat), 0},
		{"invalid", "later", 0},
		{"negative", "-5", 0},
		{"empty", "", 0},
		{"cap exactly", "3600", MaxDelay},
		{"huge seconds", "3153600000", MaxDelay},
		{"overflowing seconds", "99999999999999999999", MaxDelay},
		{"far date", now.AddDate(200, 0, 0).Format(http.TimeFormat), MaxDelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Parse(tc.value, now); got != tc.want {
				t.Errorf("Parse(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestMilliseconds(t *testing.T) {
	for delay, want := range map[time.Duration]int{
		-time.Second:            0,
		0:                       0,
		time.Microsecond:        1,
		1500 * time.Microsecond: 2,
		3 * time.Second:         3000,
		MaxDelay:                3_600_000,
	} {
		if got := Milliseconds(delay); got != want {
			t.Errorf("Milliseconds(%v) = %d, want %d", delay, got, want)
		}
	}
}
