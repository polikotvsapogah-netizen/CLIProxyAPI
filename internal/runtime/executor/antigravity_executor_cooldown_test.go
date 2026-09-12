package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	logging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const antigravityCooldownTestModel = "gemini-3.6-flash-high"

func antigravityCooldownTestAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: id,
		Metadata: map[string]any{
			"access_token": "fake",
			"project_id":   "fake",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

func antigravityRateLimitedBody(retryDelay string) []byte {
	return []byte(fmt.Sprintf(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"%s"}]}}`, retryDelay))
}

func antigravitySuccessBody() []byte {
	return []byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}}`)
}

func newAntigravityCooldownExecutor() *AntigravityExecutor {
	return NewAntigravityExecutor(&config.Config{RequestRetry: 1})
}

func antigravityCooldownTestRequest(model string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatAntigravity,
	}
}

func useAntigravityCooldownFallbackOrder(t *testing.T, order func(authID string) []string) {
	t.Helper()
	previous := antigravityBaseURLFallbackOrder
	antigravityBaseURLFallbackOrder = func(auth *cliproxyauth.Auth) []string {
		id := ""
		if auth != nil {
			id = auth.ID
		}
		return order(id)
	}
	t.Cleanup(func() {
		antigravityBaseURLFallbackOrder = previous
	})
}

func newRateLimitedAntigravityEndpoint(hits *atomic.Int32, retryDelay string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write(antigravityRateLimitedBody(retryDelay))
	}))
}

func newSuccessAntigravityEndpoint(hits *atomic.Int32, body []byte) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			hits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

func drainAntigravityStream(t *testing.T, result *cliproxyexecutor.StreamResult) {
	t.Helper()
	if result == nil {
		t.Fatal("stream result is nil")
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
}

// HQ#1099: an upstream 429 on the first base URL must not leave a short cooldown
// behind when the fallback base URL serves the request successfully.
func TestAntigravityExecuteFallbackSuccessKeepsAuthAvailable(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	var throttled, served atomic.Int32
	throttle := newRateLimitedAntigravityEndpoint(&throttled, "120s")
	defer throttle.Close()
	success := newSuccessAntigravityEndpoint(&served, antigravitySuccessBody())
	defer success.Close()
	useAntigravityCooldownFallbackOrder(t, func(string) []string { return []string{throttle.URL, success.URL} })

	e := newAntigravityCooldownExecutor()
	req, opts := antigravityCooldownTestRequest(antigravityCooldownTestModel)
	if _, err := e.Execute(context.Background(), antigravityCooldownTestAuth("hq-fallback-execute"), req, opts); err != nil {
		t.Fatalf("first request: %v", err)
	}
	if throttled.Load() != 1 || served.Load() != 1 {
		t.Fatalf("unexpected endpoint counts after first request: %d/%d", throttled.Load(), served.Load())
	}
	if _, err := e.Execute(context.Background(), antigravityCooldownTestAuth("hq-fallback-execute"), req, opts); err != nil {
		t.Fatalf("second request blocked after successful fallback: %v (throttled=%d served=%d)", err, throttled.Load(), served.Load())
	}
	if throttled.Load() != 2 || served.Load() != 2 {
		t.Fatalf("unexpected endpoint counts after second request: %d/%d", throttled.Load(), served.Load())
	}
}

// HQ#1099: same guarantee for ExecuteStream.
func TestAntigravityExecuteStreamFallbackSuccessKeepsAuthAvailable(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	var throttled, served atomic.Int32
	throttle := newRateLimitedAntigravityEndpoint(&throttled, "120s")
	defer throttle.Close()
	success := newSuccessAntigravityEndpoint(&served, antigravitySuccessBody())
	defer success.Close()
	useAntigravityCooldownFallbackOrder(t, func(string) []string { return []string{throttle.URL, success.URL} })

	e := newAntigravityCooldownExecutor()
	req, opts := antigravityCooldownTestRequest(antigravityCooldownTestModel)
	auth := antigravityCooldownTestAuth("hq-fallback-stream")
	result, err := e.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("first stream request: %v", err)
	}
	drainAntigravityStream(t, result)
	if throttled.Load() != 1 || served.Load() != 1 {
		t.Fatalf("unexpected endpoint counts after first stream: %d/%d", throttled.Load(), served.Load())
	}
	result, err = e.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("second stream request blocked after successful fallback: %v (throttled=%d served=%d)", err, throttled.Load(), served.Load())
	}
	drainAntigravityStream(t, result)
	if throttled.Load() != 2 || served.Load() != 2 {
		t.Fatalf("unexpected endpoint counts after second stream: %d/%d", throttled.Load(), served.Load())
	}
}

// HQ#1099: same guarantee for the claude non-stream variant.
func TestAntigravityClaudeNonStreamFallbackSuccessKeepsAuthAvailable(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	var throttled, served atomic.Int32
	throttle := newRateLimitedAntigravityEndpoint(&throttled, "120s")
	defer throttle.Close()
	success := newSuccessAntigravityEndpoint(&served, antigravitySuccessBody())
	defer success.Close()
	useAntigravityCooldownFallbackOrder(t, func(string) []string { return []string{throttle.URL, success.URL} })

	e := newAntigravityCooldownExecutor()
	req, opts := antigravityCooldownTestRequest("claude-sonnet-4-5")
	auth := antigravityCooldownTestAuth("hq-fallback-claude")
	if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("first claude request: %v", err)
	}
	if throttled.Load() != 1 || served.Load() != 1 {
		t.Fatalf("unexpected endpoint counts after first claude request: %d/%d", throttled.Load(), served.Load())
	}
	if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("second claude request blocked after successful fallback: %v (throttled=%d served=%d)", err, throttled.Load(), served.Load())
	}
	if throttled.Load() != 2 || served.Load() != 2 {
		t.Fatalf("unexpected endpoint counts after second claude request: %d/%d", throttled.Load(), served.Load())
	}
}

// Real exhaustion: when every endpoint answers 429 the cooldown must be recorded
// and the next request must be refused locally without touching upstream.
func TestAntigravityShortCooldownRecordedWhenAllEndpointsRateLimited(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	var first, second atomic.Int32
	firstSrv := newRateLimitedAntigravityEndpoint(&first, "120s")
	defer firstSrv.Close()
	secondSrv := newRateLimitedAntigravityEndpoint(&second, "120s")
	defer secondSrv.Close()
	useAntigravityCooldownFallbackOrder(t, func(string) []string { return []string{firstSrv.URL, secondSrv.URL} })

	e := newAntigravityCooldownExecutor()
	req, opts := antigravityCooldownTestRequest(antigravityCooldownTestModel)
	auth := antigravityCooldownTestAuth("hq-exhausted")
	_, err := e.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("expected upstream failure when both endpoints answer 429")
	}
	if strings.Contains(err.Error(), "short cooldown") {
		t.Fatalf("first failure must be the upstream 429, got local refusal: %v", err)
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("unexpected endpoint counts after exhausted request: %d/%d", first.Load(), second.Load())
	}
	_, err = e.Execute(context.Background(), auth, req, opts)
	if err == nil {
		t.Fatal("expected local refusal while short cooldown is active")
	}
	if !strings.Contains(err.Error(), "short cooldown") {
		t.Fatalf("expected local short cooldown refusal, got: %v", err)
	}
	if first.Load() != 1 || second.Load() != 1 {
		t.Fatalf("local refusal must not hit upstream, counts changed to %d/%d", first.Load(), second.Load())
	}
}

// The cooldown is scoped per auth+model: other accounts and other models of the
// same account stay available.
func TestAntigravityShortCooldownScopePerAuthAndModel(t *testing.T) {
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	var throttled, throttledFallback, served atomic.Int32
	throttle := newRateLimitedAntigravityEndpoint(&throttled, "120s")
	defer throttle.Close()
	throttledFallbackSrv := newRateLimitedAntigravityEndpoint(&throttledFallback, "120s")
	defer throttledFallbackSrv.Close()
	success := newSuccessAntigravityEndpoint(&served, antigravitySuccessBody())
	defer success.Close()
	useAntigravityCooldownFallbackOrder(t, func(authID string) []string {
		if authID == "hq-scope-a" {
			return []string{throttle.URL, throttledFallbackSrv.URL}
		}
		return []string{throttle.URL, success.URL}
	})

	e := newAntigravityCooldownExecutor()
	reqA, optsA := antigravityCooldownTestRequest(antigravityCooldownTestModel)
	if _, err := e.Execute(context.Background(), antigravityCooldownTestAuth("hq-scope-a"), reqA, optsA); err == nil {
		t.Fatal("expected failure for rate limited auth+model")
	}
	// Another auth is unaffected and served by the fallback endpoint.
	reqB, optsB := antigravityCooldownTestRequest(antigravityCooldownTestModel)
	if _, err := e.Execute(context.Background(), antigravityCooldownTestAuth("hq-scope-b"), reqB, optsB); err != nil {
		t.Fatalf("different auth must not inherit the cooldown: %v", err)
	}
	// Another model of the cooled down auth is not refused locally: it reaches
	// upstream again (both test endpoints answer 429, so it fails upstream).
	before := throttled.Load()
	reqOtherModel, optsOtherModel := antigravityCooldownTestRequest("gemini-3.6-flash-low")
	_, err := e.Execute(context.Background(), antigravityCooldownTestAuth("hq-scope-a"), reqOtherModel, optsOtherModel)
	if err == nil {
		t.Fatal("expected upstream 429s for the other model call")
	}
	if strings.Contains(err.Error(), "short cooldown") {
		t.Fatalf("different model must not be refused locally: %v", err)
	}
	if throttled.Load() != before+1 {
		t.Fatalf("other model call must reach upstream, throttled %d -> %d", before, throttled.Load())
	}
	if served.Load() != 1 {
		t.Fatalf("unexpected served count: %d", served.Load())
	}
	// The original auth+model is still cooling down and must be refused locally.
	if _, err := e.Execute(context.Background(), antigravityCooldownTestAuth("hq-scope-a"), reqA, optsA); err == nil || !strings.Contains(err.Error(), "short cooldown") {
		t.Fatalf("expected local refusal for the original auth+model, got: %v", err)
	}
	if served.Load() != 1 {
		t.Fatalf("local refusal must not hit upstream, served count is %d", served.Load())
	}
}

// A late success of an older request must not erase the short cooldown that a
// newer request for the SAME auth+model recorded on its final 429: success
// mutates no cooldown state, so the next same-key request stays refused
// locally. Exercises the Execute, ExecuteStream, and Claude Execute
// entrypoints against both the in-memory store and home KV.
func TestAntigravityLateSuccessDoesNotEraseNewerCooldown(t *testing.T) {
	stores := []struct {
		name   string
		homeKV bool
	}{
		{name: "syncmap"},
		{name: "homekv", homeKV: true},
	}
	paths := []struct {
		name  string
		path  string
		model string
	}{
		{name: "execute", path: "execute", model: antigravityCooldownTestModel},
		{name: "stream", path: "stream", model: antigravityCooldownTestModel},
		{name: "claude", path: "execute", model: "claude-sonnet-4-5"},
	}
	for _, store := range stores {
		for _, p := range paths {
			t.Run(store.name+"/"+p.name, func(t *testing.T) {
				runAntigravityLateSuccessSameKeyScenario(t, p.path, p.model, store.homeKV)
			})
		}
	}
}

// antigravityCooldownKVFake is a minimal concurrency-safe home KV fake for
// scenarios that drive real executor goroutines concurrently.
type antigravityCooldownKVFake struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (c *antigravityCooldownKVFake) KVGet(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, ok := c.values[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), value...), true, nil
}

func (c *antigravityCooldownKVFake) KVSet(_ context.Context, key string, value []byte, _ homekv.KVSetOptions) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *antigravityCooldownKVFake) KVSetNX(_ context.Context, key string, value []byte, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.values[key]; ok {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *antigravityCooldownKVFake) KVDel(_ context.Context, keys ...string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var deleted int64
	for _, key := range keys {
		if _, ok := c.values[key]; ok {
			delete(c.values, key)
			deleted++
		}
	}
	return deleted, nil
}

// useAntigravityCooldownKVStore routes the executor at a threadsafe in-memory
// home KV store for the duration of the test.
func useAntigravityCooldownKVStore(t *testing.T) *antigravityCooldownKVFake {
	t.Helper()
	fake := &antigravityCooldownKVFake{values: make(map[string][]byte)}
	previous := currentAntigravityKVClient
	currentAntigravityKVClient = func() (antigravityKVClient, bool, error) {
		return fake, true, nil
	}
	t.Cleanup(func() {
		currentAntigravityKVClient = previous
	})
	return fake
}

// runAntigravityLateSuccessSameKeyScenario parks an older request on its
// successful fallback endpoint, records a fresh short cooldown from a newer
// request with the same auth+model, then releases the older success. The
// recorded cooldown must survive: the next same-key request is refused
// locally without any upstream call.
func runAntigravityLateSuccessSameKeyScenario(t *testing.T, path, model string, homeKV bool) {
	t.Helper()
	resetAntigravityCreditsRetryState()
	t.Cleanup(resetAntigravityCreditsRetryState)
	if homeKV {
		useAntigravityCooldownKVStore(t)
	}

	var lateThrottled, bFirst, bSecond, served atomic.Int32
	lateThrottle := newRateLimitedAntigravityEndpoint(&lateThrottled, "120s")
	bFirstSrv := newRateLimitedAntigravityEndpoint(&bFirst, "120s")
	bSecondSrv := newRateLimitedAntigravityEndpoint(&bSecond, "120s")
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	gated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		served.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(antigravitySuccessBody())
	}))
	// Cleanup order matters: release the parked request before closing the
	// gated server, otherwise Close waits forever on the parked handler even
	// when an assertion failed earlier.
	t.Cleanup(func() {
		lateThrottle.Close()
		bFirstSrv.Close()
		bSecondSrv.Close()
		gated.Close()
	})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
	})

	var phase atomic.Int32
	useAntigravityCooldownFallbackOrder(t, func(string) []string {
		if phase.Load() == 0 {
			return []string{lateThrottle.URL, gated.URL}
		}
		return []string{bFirstSrv.URL, bSecondSrv.URL}
	})

	e := newAntigravitySingleRetryExecutor()
	req, opts := antigravityCooldownTestRequest(model)
	auth := antigravityCooldownTestAuth("hq-late-success-same-key")

	type lateOutcome struct {
		stream *cliproxyexecutor.StreamResult
		err    error
	}
	lateDone := make(chan lateOutcome, 1)
	go func() {
		if path == "stream" {
			result, err := e.ExecuteStream(context.Background(), auth, req, opts)
			lateDone <- lateOutcome{stream: result, err: err}
			return
		}
		_, err := e.Execute(context.Background(), auth, req, opts)
		lateDone <- lateOutcome{err: err}
	}()
	select {
	case <-entered: // the older request saw a short 429 and is parked on its fallback
	case outcome := <-lateDone:
		t.Fatalf("older request finished before reaching the fallback endpoint: %v", outcome.err)
	}

	// A newer request for the same auth+model exhausts both endpoints and
	// records the cooldown on its final 429 while the older request is parked.
	phase.Store(1)
	if err := runAntigravityCooldownPath(t, path, e, auth, req, opts); err == nil {
		t.Fatal("expected the newer same-key request to fail upstream")
	} else if strings.Contains(err.Error(), "short cooldown") {
		t.Fatalf("newer request must fail with the upstream 429, got local refusal: %v", err)
	}
	if bFirst.Load() != 1 || bSecond.Load() != 1 {
		t.Fatalf("unexpected newer-request endpoint counts: %d/%d", bFirst.Load(), bSecond.Load())
	}
	inCooldown, remaining, errRead := antigravityIsInShortCooldownRequired(context.Background(), auth, model, time.Now())
	if errRead != nil {
		t.Fatalf("cooldown read after the newer refusal: %v", errRead)
	}
	if !inCooldown || remaining <= 0 || remaining > 120*time.Second {
		t.Fatalf("newer refusal must record the cooldown, inCooldown=%v remaining=%s", inCooldown, remaining)
	}

	// The parked older request now succeeds; success mutates no cooldown state.
	releaseOnce.Do(func() { close(release) })
	outcome := <-lateDone
	if outcome.err != nil {
		t.Fatalf("older request should succeed on its fallback: %v", outcome.err)
	}
	if path == "stream" {
		drainAntigravityStream(t, outcome.stream)
	}
	if lateThrottled.Load() != 1 || served.Load() != 1 {
		t.Fatalf("unexpected older-request endpoint counts: %d/%d", lateThrottled.Load(), served.Load())
	}

	// The newer same-key cooldown must have survived the older success.
	if err := runAntigravityCooldownPath(t, path, e, auth, req, opts); err == nil || !strings.Contains(err.Error(), "short cooldown") {
		t.Fatalf("expected local short-cooldown refusal after the older success, got: %v", err)
	}
	if bFirst.Load() != 1 || bSecond.Load() != 1 || lateThrottled.Load() != 1 || served.Load() != 1 {
		t.Fatalf("local refusal must not hit upstream, counts are late=%d served=%d bFirst=%d bSecond=%d", lateThrottled.Load(), served.Load(), bFirst.Load(), bSecond.Load())
	}
}

// Home KV mode: successful fallback must not write a cooldown record, and a real
// exhaustion must still record one; a broken KV store must fail closed on the
// final record.
func TestAntigravityShortCooldownHomeKVReconciliation(t *testing.T) {
	t.Run("successful fallback writes no cooldown key", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)
		kv := newFakeAntigravityKVClient()
		useFakeAntigravityKVClient(t, kv, true, nil)
		var throttled, served atomic.Int32
		throttle := newRateLimitedAntigravityEndpoint(&throttled, "120s")
		defer throttle.Close()
		success := newSuccessAntigravityEndpoint(&served, antigravitySuccessBody())
		defer success.Close()
		useAntigravityCooldownFallbackOrder(t, func(string) []string { return []string{throttle.URL, success.URL} })

		e := newAntigravityCooldownExecutor()
		req, opts := antigravityCooldownTestRequest(antigravityCooldownTestModel)
		if _, err := e.Execute(context.Background(), antigravityCooldownTestAuth("hq-kv-success"), req, opts); err != nil {
			t.Fatalf("fallback request: %v", err)
		}
		for key := range kv.values {
			if strings.HasPrefix(key, "cpa:antigravity:short-cooldown:") {
				t.Fatalf("successful fallback must not record a cooldown, found key %s", key)
			}
		}
	})

	t.Run("exhaustion records cooldown key and fails next request locally", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)
		kv := newFakeAntigravityKVClient()
		useFakeAntigravityKVClient(t, kv, true, nil)
		var first, second atomic.Int32
		firstSrv := newRateLimitedAntigravityEndpoint(&first, "120s")
		defer firstSrv.Close()
		secondSrv := newRateLimitedAntigravityEndpoint(&second, "120s")
		defer secondSrv.Close()
		useAntigravityCooldownFallbackOrder(t, func(string) []string { return []string{firstSrv.URL, secondSrv.URL} })

		e := newAntigravityCooldownExecutor()
		req, opts := antigravityCooldownTestRequest(antigravityCooldownTestModel)
		auth := antigravityCooldownTestAuth("hq-kv-exhausted")
		if _, err := e.Execute(context.Background(), auth, req, opts); err == nil {
			t.Fatal("expected upstream failure")
		}
		found := false
		for key := range kv.values {
			if strings.HasPrefix(key, "cpa:antigravity:short-cooldown:") {
				found = true
			}
		}
		if !found {
			t.Fatal("expected a recorded short cooldown key in home kv after real exhaustion")
		}
		if _, err := e.Execute(context.Background(), auth, req, opts); err == nil || !strings.Contains(err.Error(), "short cooldown") {
			t.Fatalf("expected local refusal in home kv mode, got: %v", err)
		}
		if first.Load() != 1 || second.Load() != 1 {
			t.Fatalf("local refusal must not hit upstream, counts are %d/%d", first.Load(), second.Load())
		}
	})

	t.Run("kv write failure fails closed on final record", func(t *testing.T) {
		resetAntigravityCreditsRetryState()
		t.Cleanup(resetAntigravityCreditsRetryState)
		kv := newFakeAntigravityKVClient()
		kv.setErr = errors.New("kv down")
		useFakeAntigravityKVClient(t, kv, true, nil)
		var first, second atomic.Int32
		firstSrv := newRateLimitedAntigravityEndpoint(&first, "120s")
		defer firstSrv.Close()
		secondSrv := newRateLimitedAntigravityEndpoint(&second, "120s")
		defer secondSrv.Close()
		useAntigravityCooldownFallbackOrder(t, func(string) []string { return []string{firstSrv.URL, secondSrv.URL} })

		e := newAntigravityCooldownExecutor()
		req, opts := antigravityCooldownTestRequest(antigravityCooldownTestModel)
		_, err := e.Execute(context.Background(), antigravityCooldownTestAuth("hq-kv-down"), req, opts)
		var status statusErr
		if !errors.As(err, &status) || status.code != http.StatusServiceUnavailable {
			t.Fatalf("expected home kv unavailable 503 when the final record cannot be written, got: %v", err)
		}
	})
}
func antigravitySoftRateLimitedBody() []byte {
	return []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`)
}

func antigravityQuotaExhaustedBody() []byte {
	return []byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`)
}

func newAntigravitySingleRetryExecutor() *AntigravityExecutor {
	return NewAntigravityExecutor(&config.Config{RequestRetry: 0})
}

func runAntigravityCooldownPath(t *testing.T, path string, e *AntigravityExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
	t.Helper()
	if path == "stream" {
		result, err := e.ExecuteStream(context.Background(), auth, req, opts)
		if err == nil {
			drainAntigravityStream(t, result)
		}
		return err
	}
	_, err := e.Execute(context.Background(), auth, req, opts)
	return err
}

// HQ#1099 follow-up: the pending cooldown must represent the current final
// upstream refusal only. A short-cooldown 429 observed on an earlier endpoint
// must never be committed when the request finally fails with a different 429
// class, and a later short-cooldown observation must re-anchor the deadline.
func TestAntigravityPendingCooldownTracksFinalRefusal(t *testing.T) {
	scenarios := []struct {
		name             string
		finalBody        []byte
		wantKind         antigravity429DecisionKind
		wantCooldown     bool
		wantMaxRemaining time.Duration
	}{
		{
			name:         "short 120s then instant 1s commits nothing",
			finalBody:    antigravityRateLimitedBody("1s"),
			wantKind:     antigravity429DecisionInstantRetrySameAuth,
			wantCooldown: false,
		},
		{
			name:         "short 120s then soft retry commits nothing",
			finalBody:    antigravitySoftRateLimitedBody(),
			wantKind:     antigravity429DecisionSoftRetry,
			wantCooldown: false,
		},
		{
			name:         "short 120s then full quota commits nothing",
			finalBody:    antigravityQuotaExhaustedBody(),
			wantKind:     antigravity429DecisionFullQuotaExhausted,
			wantCooldown: false,
		},
		{
			name:             "short 120s then short 10s reanchors deadline",
			finalBody:        antigravityRateLimitedBody("10s"),
			wantKind:         antigravity429DecisionShortCooldownSwitchAuth,
			wantCooldown:     true,
			wantMaxRemaining: 10 * time.Second,
		},
	}
	paths := []struct {
		name  string
		path  string
		model string
	}{
		{name: "execute", path: "execute", model: antigravityCooldownTestModel},
		{name: "stream", path: "stream", model: antigravityCooldownTestModel},
		{name: "claude", path: "execute", model: "claude-sonnet-4-5"},
	}
	for _, scenario := range scenarios {
		if got := decideAntigravity429(scenario.finalBody).kind; got != scenario.wantKind {
			t.Fatalf("%s: classifier kind = %q, want %q", scenario.name, got, scenario.wantKind)
		}
		for _, p := range paths {
			t.Run(scenario.name+"/"+p.name, func(t *testing.T) {
				resetAntigravityCreditsRetryState()
				t.Cleanup(resetAntigravityCreditsRetryState)
				var first, second atomic.Int32
				shortSrv := newRateLimitedAntigravityEndpoint(&first, "120s")
				defer shortSrv.Close()
				finalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					second.Add(1)
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write(scenario.finalBody)
				}))
				defer finalSrv.Close()
				success := newSuccessAntigravityEndpoint(nil, antigravitySuccessBody())
				defer success.Close()
				useAntigravityCooldownFallbackOrder(t, func(string) []string {
					return []string{shortSrv.URL, finalSrv.URL}
				})

				e := newAntigravitySingleRetryExecutor()
				req, opts := antigravityCooldownTestRequest(p.model)
				auth := antigravityCooldownTestAuth("hq-final-refusal")

				if err := runAntigravityCooldownPath(t, p.path, e, auth, req, opts); err == nil {
					t.Fatal("expected the final upstream refusal to fail the request")
				}
				if first.Load() != 1 || second.Load() != 1 {
					t.Fatalf("unexpected endpoint counts: %d/%d", first.Load(), second.Load())
				}

				inCooldown, remaining, errRead := antigravityIsInShortCooldownRequired(context.Background(), auth, p.model, time.Now())
				if errRead != nil {
					t.Fatalf("cooldown read: %v", errRead)
				}
				if !scenario.wantCooldown {
					if inCooldown {
						t.Fatalf("final %q refusal must not commit the earlier short cooldown, %s remaining", scenario.wantKind, remaining)
					}
					// Success controls: the auth path keeps serving right after the
					// mixed refusal, and stays serving after successful requests.
					useAntigravityCooldownFallbackOrder(t, func(string) []string {
						return []string{shortSrv.URL, success.URL}
					})
					if err := runAntigravityCooldownPath(t, p.path, e, auth, req, opts); err != nil {
						t.Fatalf("success control after mixed refusal: %v", err)
					}
					if err := runAntigravityCooldownPath(t, p.path, e, auth, req, opts); err != nil {
						t.Fatalf("second success control after mixed refusal: %v", err)
					}
					if first.Load() != 3 {
						t.Fatalf("success controls must reach upstream, first endpoint hits = %d", first.Load())
					}
					return
				}
				if !inCooldown {
					t.Fatal("expected the final short-cooldown refusal to record a cooldown")
				}
				if remaining <= 0 || remaining > scenario.wantMaxRemaining {
					t.Fatalf("cooldown remaining = %s, want 0 < remaining <= %s", remaining, scenario.wantMaxRemaining)
				}
			})
		}
	}
}

// The production log formatter only prints whitelisted fields; this pins that
// the cooldown diagnostics survive formatting into the main.log line,
// including the request_id segment sourced from the request context.
func TestAntigravityCooldownLogFieldsReachProductionFormatter(t *testing.T) {
	auth := antigravityCooldownTestAuth("hq-formatter")
	pending := &antigravityPendingShortCooldown{
		observedAt: time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC),
		retryAfter: 120 * time.Second,
		endpoint:   "cloudcode-pa.googleapis.com",
		reason:     "RATE_LIMIT_EXCEEDED",
	}
	ctx := logging.WithRequestID(context.Background(), "req-42")
	entry := helps.LogWithRequestID(ctx).WithFields(antigravityPendingShortCooldownLogFields(auth, antigravityCooldownTestModel, pending))
	entry.Time = time.Date(2026, 9, 12, 12, 0, 0, 0, time.Local)
	entry.Level = log.WarnLevel
	entry.Message = "antigravity executor: upstream 429 requests short cooldown, record deferred until final failure"

	formatted, errFormat := (&logging.LogFormatter{}).Format(entry)
	if errFormat != nil {
		t.Fatalf("Format() error = %v", errFormat)
	}
	line := string(formatted)
	for _, want := range []string{
		"[req-42]",
		"model=" + antigravityCooldownTestModel,
		"credential=",
		"endpoint=cloudcode-pa.googleapis.com",
		"reason=\"RATE_LIMIT_EXCEEDED\"",
		"retry_after_s=120",
		"observed_at=2026-09-12T05:00:00Z",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("formatted line %q missing %q", line, want)
		}
	}
	for _, forbidden := range []string{"access_token", "prompt"} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("formatted line %q leaks %q", line, forbidden)
		}
	}
}
