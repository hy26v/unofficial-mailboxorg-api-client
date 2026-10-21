// ABOUTME: Shared parsing of the HTTP Retry-After header for the client and request guard.
// ABOUTME: Accepts seconds or an HTTP date and caps every delay at MaxDelay.
package retryafter

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxDelay caps how long one provider Retry-After value can ask mbx to wait. The cap is
// far above every provider delay seen or documented for mailbox.org: the measured
// failed-login Retry-After of 600 seconds and the roughly 15-minute firewall block in its
// guidance. A larger value is almost certainly a provider or proxy error, and without the
// cap one bad header could block every command sharing MBX_HOME for years. If the
// provider still limits the account after the cap, the next request receives a fresh 429
// and a new delay, so the cost of the cap is at most one extra request per hour.
const MaxDelay = time.Hour

// Parse returns the delay that a Retry-After value asks for, limited to MaxDelay. It
// accepts whole seconds or an HTTP date. An empty, invalid or past value gives zero.
func Parse(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseUint(value, 10, 64)
	if err == nil {
		if seconds > uint64(MaxDelay/time.Second) {
			return MaxDelay
		}
		return time.Duration(seconds) * time.Second
	}
	if errors.Is(err, strconv.ErrRange) {
		// All digits, but too large for uint64: still a seconds value, just a huge one.
		return MaxDelay
	}
	retryAt, err := http.ParseTime(value)
	if err != nil || !retryAt.After(now) {
		return 0
	}
	if delay := retryAt.Sub(now); delay < MaxDelay {
		return delay
	}
	return MaxDelay
}

// Milliseconds converts a delay to whole milliseconds, rounding up so a caller that waits
// the reported time never retries early. Negative delays give zero.
func Milliseconds(delay time.Duration) int {
	if delay <= 0 {
		return 0
	}
	millis := delay / time.Millisecond
	if delay%time.Millisecond != 0 {
		millis++
	}
	if maxInt := int64(^uint(0) >> 1); int64(millis) > maxInt {
		return int(maxInt)
	}
	return int(millis)
}
