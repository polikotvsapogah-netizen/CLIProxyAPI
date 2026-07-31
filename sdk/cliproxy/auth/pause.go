package auth

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// PauseUntilMetadataKey stores an operator-controlled pause deadline.
	// While this timestamp is in the future, selectors exclude the auth from routing.
	PauseUntilMetadataKey = "paused_until"
)

// AuthPauseUntil returns the configured pause deadline, if present and parseable.
func AuthPauseUntil(auth *Auth) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	if until, ok := parsePauseUntilValue(auth.Metadata[PauseUntilMetadataKey]); ok {
		return until, true
	}
	if until, ok := parsePauseUntilValue(auth.Metadata["pause_until"]); ok {
		return until, true
	}
	if until, ok := parsePauseUntilValue(auth.Metadata["pausedUntil"]); ok {
		return until, true
	}
	if auth.Attributes != nil {
		if until, ok := parsePauseUntilValue(auth.Attributes[PauseUntilMetadataKey]); ok {
			return until, true
		}
	}
	return time.Time{}, false
}

// AuthPausedAt reports whether the auth should be excluded from routing at now.
func AuthPausedAt(auth *Auth, now time.Time) bool {
	until, ok := AuthPauseUntil(auth)
	if !ok {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	return until.After(now)
}

func parsePauseUntilValue(value any) (time.Time, bool) {
	switch v := value.(type) {
	case nil:
		return time.Time{}, false
	case time.Time:
		if v.IsZero() {
			return time.Time{}, false
		}
		return v.UTC(), true
	case string:
		return parsePauseUntilString(v)
	case fmt.Stringer:
		return parsePauseUntilString(v.String())
	case int64:
		return pauseUnix(v)
	case int:
		return pauseUnix(int64(v))
	case float64:
		return pauseUnix(int64(v))
	case json.Number:
		if n, err := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64); err == nil {
			return pauseUnix(n)
		}
	}
	return time.Time{}, false
}

func parsePauseUntilString(value string) (time.Time, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}, false
	}
	if parsed, err := time.Parse(time.RFC3339Nano, text); err == nil {
		return parsed.UTC(), true
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		return pauseUnix(n)
	}
	return time.Time{}, false
}

func pauseUnix(value int64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	// Accept either seconds or milliseconds from frontend/native clients.
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value).UTC(), true
	}
	return time.Unix(value, 0).UTC(), true
}
