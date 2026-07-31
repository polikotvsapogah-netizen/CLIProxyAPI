package auth

import (
	"context"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestPickNext_SkipsAuthPausedUntilFuture(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(schedulerTestExecutor{})
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "paused-auth",
		Provider: "test",
		Metadata: map[string]any{
			"paused_until": future,
		},
	}); err != nil {
		t.Fatalf("register paused-auth: %v", err)
	}
	if _, err := manager.Register(context.Background(), &Auth{ID: "ready-auth", Provider: "test"}); err != nil {
		t.Fatalf("register ready-auth: %v", err)
	}

	auth, _, err := manager.pickNext(context.Background(), "test", "", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatalf("pickNext() error = %v", err)
	}
	if auth == nil || auth.ID != "ready-auth" {
		t.Fatalf("pickNext() auth = %v, want ready-auth", auth)
	}
}

func TestPickNext_AllowsAuthAfterPauseExpires(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(schedulerTestExecutor{})
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)

	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "expired-pause-auth",
		Provider: "test",
		Metadata: map[string]any{
			"paused_until": past,
		},
	}); err != nil {
		t.Fatalf("register expired-pause-auth: %v", err)
	}

	auth, _, err := manager.pickNext(context.Background(), "test", "", cliproxyexecutor.Options{}, nil)
	if err != nil {
		t.Fatalf("pickNext() error = %v", err)
	}
	if auth == nil || auth.ID != "expired-pause-auth" {
		t.Fatalf("pickNext() auth = %v, want expired-pause-auth", auth)
	}
}
