package auth

import (
	"context"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const antigravityGenericExhaustedTestBody = `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota)","status":"RESOURCE_EXHAUSTED"}}`

func antigravityGenericExhaustedResult(authID, model string) Result {
	return Result{
		AuthID:   authID,
		Provider: "antigravity",
		Model:    model,
		Success:  false,
		Error: &Error{
			Code:       "rate_limit",
			Message:    antigravityGenericExhaustedTestBody,
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	}
}

// HQ#1104: a generic RESOURCE_EXHAUSTED 429 (no ErrorInfo reason, no retryDelay)
// must cool the auth+model for at least 60s, and a repeat failure after the
// window expires must keep climbing the backoff ladder instead of restarting
// from the 1s base.
func TestMarkResultAntigravityGenericExhaustedCooldownFloor(t *testing.T) {
	withQuotaCooldownEnabled(t)

	const model = "antigravity-opus-4.6"

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-antigravity-generic",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	before := time.Now()
	manager.MarkResult(context.Background(), antigravityGenericExhaustedResult(auth.ID, model))
	first, ok := manager.GetByID(auth.ID)
	if !ok || first == nil || first.ModelStates[model] == nil {
		t.Fatalf("expected model state after generic exhausted 429")
	}
	if remaining := first.ModelStates[model].Quota.NextRecoverAt.Sub(before); remaining < 60*time.Second {
		t.Fatalf("generic exhausted cooldown = %v, want >= 60s", remaining)
	}

	// Expire the window and fail again on a ladder level 1: the level must
	// grow to 2 (not restart), and the cooldown floor still applies.
	expired := time.Now().Add(-time.Second)
	retry := NewManager(nil, nil, nil)
	retryAuth := &Auth{
		ID:       "auth-antigravity-generic-repeat",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: expired,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired, BackoffLevel: 1},
			},
		},
	}
	if _, errRegister := retry.Register(WithSkipPersist(context.Background()), retryAuth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	beforeRepeat := time.Now()
	retry.MarkResult(context.Background(), antigravityGenericExhaustedResult(retryAuth.ID, model))
	updated, ok := retry.GetByID(retryAuth.ID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatalf("expected model state after repeat generic exhausted 429")
	}
	state := updated.ModelStates[model]
	if state.Quota.BackoffLevel != 2 {
		t.Fatalf("expected BackoffLevel to grow to 2 after window expiry, got %d", state.Quota.BackoffLevel)
	}
	if remaining := state.Quota.NextRecoverAt.Sub(beforeRepeat); remaining < 60*time.Second {
		t.Fatalf("repeat generic exhausted cooldown = %v, want >= 60s", remaining)
	}
}

// HQ#1104 guard: the 60s floor applies only to Antigravity generic exhausted
// refusals. The same 429 body from another provider keeps the plain 1s ladder.
func TestMarkResultNonAntigravityGenericBodyKeepsLadder(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "auth-codex-generic-body",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	result := antigravityGenericExhaustedResult(auth.ID, "gpt-5")
	result.Provider = "codex"
	manager.MarkResult(context.Background(), result)
	state, ok := manager.GetByID(auth.ID)
	if !ok || state == nil || state.ModelStates["gpt-5"] == nil {
		t.Fatalf("expected model state after non-antigravity 429")
	}
	if remaining := time.Until(state.ModelStates["gpt-5"].Quota.NextRecoverAt); remaining >= 60*time.Second {
		t.Fatalf("non-antigravity cooldown = %v, want below the 60s floor", remaining)
	}
}

// HQ#1104 review R2: an auth-level result carrying a retry hint must keep the
// short hint cooldown; the generic exhausted floor must not be applied on top
// of a recognized RetryAfter. The hint-less generic body pins the per-auth
// result.RetryAfter guard itself: the classifier sees a generic refusal there,
// so only the guard keeps the floor off.
func TestMarkResultAuthRetryHintKeepsShortCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)

	hint := 2 * time.Second
	for name, body := range map[string]string{
		"retry_info_body":              `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"2s"}]}}`,
		"hint_without_retry_info_body": antigravityGenericExhaustedTestBody,
	} {
		t.Run(name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			auth := &Auth{ID: "hq1104-auth-hint-" + name, Provider: "antigravity"}
			if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
				t.Fatalf("Register returned error: %v", errRegister)
			}

			result := antigravityGenericExhaustedResult(auth.ID, "")
			result.Error.Message = body
			result.RetryAfter = &hint

			before := time.Now()
			manager.MarkResult(context.Background(), result)
			after := time.Now()

			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated == nil {
				t.Fatalf("expected auth state after retry-hinted 429")
			}
			if updated.Quota.NextRecoverAt.Before(before.Add(hint)) || updated.Quota.NextRecoverAt.After(after.Add(hint)) {
				t.Fatalf("auth retry-hint cooldown = %v, want %v", updated.Quota.NextRecoverAt.Sub(before), hint)
			}
			blocked, _, next := isAuthBlockedForModel(updated, "", before.Add(hint-time.Second))
			if !blocked {
				t.Fatal("selector did not block before the retry-hint deadline")
			}
			blocked, _, _ = isAuthBlockedForModel(updated, "", next.Add(time.Nanosecond))
			if blocked {
				t.Fatal("selector blocked after the retry-hint deadline")
			}
		})
	}
}

// HQ#1104 review R1: a request arriving inside the tail of a generic exhausted
// cooldown must be refused immediately, not parked in the external retry loop
// until the 60s floor expires. Synctest advances the virtual clock 40s into
// the window, leaving ~20s of cooldown — under the old policy the client wait.
func TestAntigravityGenericCooldownTailDoesNotWait(t *testing.T) {
	const tailModel = "hq1104-tail-model"
	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withQuotaCooldownEnabled(t)
				manager := NewManager(nil, nil, nil)
				manager.SetRetryConfig(2, 30*time.Second, 0)
				e := &authFallbackExecutor{id: "antigravity", executeErrors: map[string]error{}, streamFirstErrors: map[string]error{}}
				manager.RegisterExecutor(e)
				for _, id := range []string{"hq1104-tail-a", "hq1104-tail-b"} {
					registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: tailModel, Object: "model", OwnedBy: "antigravity"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
					if _, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: id, Provider: "antigravity"}); err != nil {
						t.Fatal(err)
					}
					err := &Error{Message: antigravityGenericExhaustedTestBody, HTTPStatus: http.StatusTooManyRequests}
					e.executeErrors[id] = err
					e.streamFirstErrors[id] = err
				}
				req := cliproxyexecutor.Request{Model: tailModel}
				invoke := func() error {
					if mode == "stream" {
						result, err := manager.ExecuteStream(context.Background(), []string{"antigravity"}, req, cliproxyexecutor.Options{})
						if err != nil {
							return err
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								return chunk.Err
							}
						}
						return nil
					}
					_, err := manager.Execute(context.Background(), []string{"antigravity"}, req, cliproxyexecutor.Options{})
					return err
				}
				if err := invoke(); err == nil {
					t.Fatal("expected initial generic refusal")
				}
				// Advance into the final ~20 seconds of the 60s cooldown.
				time.Sleep(40 * time.Second)
				started := time.Now()
				err := invoke()
				elapsed := time.Since(started)
				calls := len(e.ExecuteCalls())
				if mode == "stream" {
					calls = len(e.StreamCalls())
				}
				if err == nil {
					t.Fatal("expected the second request to be refused during the cooldown tail")
				}
				if elapsed > 3*time.Second {
					t.Fatalf("artificial cooldown wait=%s; want refusal <=3s", elapsed)
				}
				if calls != 2 {
					t.Fatalf("total executor calls=%d, want 2 (first request only; second refused locally)", calls)
				}
			})
		})
	}
}
