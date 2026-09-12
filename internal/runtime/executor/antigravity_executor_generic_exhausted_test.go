package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// antigravityGenericExhaustedBody mirrors the live upstream refusal observed in
// HQ#1104: quota status without ErrorInfo details and without a retryDelay.
func antigravityGenericExhaustedBody() []byte {
	return []byte(`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota)","status":"RESOURCE_EXHAUSTED"}}`)
}

// HQ#1104: a generic RESOURCE_EXHAUSTED 429 without retryDelay must not trigger
// same-auth retries. Each fallback base URL is tried once and the auth refuses,
// so one request costs exactly one upstream call per base URL regardless of the
// configured attempt count.
func TestAntigravityGenericExhausted429DoesNotRetrySameAuth(t *testing.T) {
	paths := []struct {
		name  string
		path  string
		model string
	}{
		{name: "execute", path: "execute", model: antigravityCooldownTestModel},
		{name: "stream", path: "stream", model: antigravityCooldownTestModel},
		{name: "claude", path: "execute", model: "claude-sonnet-4-5"},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			resetAntigravityCreditsRetryState()
			t.Cleanup(resetAntigravityCreditsRetryState)
			var hits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write(antigravityGenericExhaustedBody())
			}))
			defer srv.Close()
			useAntigravityCooldownFallbackOrder(t, func(string) []string {
				return []string{srv.URL, srv.URL}
			})

			e := NewAntigravityExecutor(&config.Config{RequestRetry: 2})
			req, opts := antigravityCooldownTestRequest(p.model)
			err := runAntigravityCooldownPath(t, p.path, e, antigravityCooldownTestAuth("hq1104-generic"), req, opts)
			if err == nil {
				t.Fatal("expected the auth to refuse with the upstream 429")
			}
			var se statusErr
			if !errors.As(err, &se) || se.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("expected a 429 status error, got %v", err)
			}
			if got := hits.Load(); got != 2 {
				t.Fatalf("generic exhausted 429 upstream calls = %d, want 2 (one per base url, no same-auth retry)", got)
			}
		})
	}
}

// HQ#1104 guard: classified 429 responses with a retryDelay keep the HQ#1099
// behavior. An instant-retry refusal continues the attempt loop on the primary
// base URL and falls back to the second URL only on the final attempt, so
// attempts=3 costs four upstream calls.
func TestAntigravityInstantRetry429BehaviorUnchanged(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(antigravityRateLimitedBody("1s"))
	}))
	defer srv.Close()
	useAntigravityCooldownFallbackOrder(t, func(string) []string {
		return []string{srv.URL, srv.URL}
	})

	e := NewAntigravityExecutor(&config.Config{RequestRetry: 2})
	req, opts := antigravityCooldownTestRequest(antigravityCooldownTestModel)
	err := runAntigravityCooldownPath(t, "execute", e, antigravityCooldownTestAuth("hq1104-instant"), req, opts)
	if err == nil {
		t.Fatal("expected the auth to refuse with the upstream 429")
	}
	if got := hits.Load(); got != 4 {
		t.Fatalf("instant retry 429 upstream calls = %d, want 4 (3 primary attempts + final fallback)", got)
	}
}

// HQ#1104 review R2: a RESOURCE_EXHAUSTED refusal carrying a RetryInfo
// retryDelay but no ErrorInfo reason keeps the pre-HQ#1104 soft-retry
// recovery instead of being classified as a drained quota.
func TestAntigravityRetryHintWithoutReasonKeepsSoftRetry(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	body := []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"0.01s"}]}}`)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write(body)
			return
		}
		_, _ = w.Write(antigravitySuccessBody())
	}))
	defer srv.Close()
	useAntigravityCooldownFallbackOrder(t, func(string) []string {
		return []string{srv.URL}
	})

	req, opts := antigravityCooldownTestRequest(antigravityCooldownTestModel)
	_, err := NewAntigravityExecutor(&config.Config{RequestRetry: 1}).Execute(context.Background(), antigravityCooldownTestAuth("hq1104-retry-hint"), req, opts)
	decision := decideAntigravity429(body)
	if err != nil {
		t.Fatalf("retry-hinted 429 lost existing recovery: kind=%s retryAfter=%v upstream calls=%d err=%v; want success on second call", decision.kind, *decision.retryAfter, hits.Load(), err)
	}
}
