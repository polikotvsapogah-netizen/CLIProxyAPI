package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
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
