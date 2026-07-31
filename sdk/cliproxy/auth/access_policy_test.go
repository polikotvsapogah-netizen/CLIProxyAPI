package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestPickNext_RestrictsAuthsToAllowedPools(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(schedulerTestExecutor{})
	if _, err := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "test"}); err != nil {
		t.Fatalf("register codex-a: %v", err)
	}
	if _, err := manager.Register(context.Background(), &Auth{ID: "codex-b", Provider: "test"}); err != nil {
		t.Fatalf("register codex-b: %v", err)
	}

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ClientIDMetadataKey:         "openclaw-foxy",
		cliproxyexecutor.AllowedAuthPoolsMetadataKey: map[string]string{"codex-b": "codex-foxy"},
	}}

	auth, _, err := manager.pickNext(context.Background(), "test", "", opts, nil)
	if err != nil {
		t.Fatalf("pickNext() error = %v", err)
	}
	if auth == nil || auth.ID != "codex-b" {
		t.Fatalf("pickNext() auth = %v, want codex-b", auth)
	}
	if got := auth.Attributes[cliproxyexecutor.ClientIDMetadataKey]; got != "openclaw-foxy" {
		t.Fatalf("client attribute = %q, want openclaw-foxy", got)
	}
	if got := auth.Attributes[cliproxyexecutor.SelectedPoolMetadataKey]; got != "codex-foxy" {
		t.Fatalf("pool attribute = %q, want codex-foxy", got)
	}
}

func TestPickNext_ReturnsPoolAccessDeniedWhenNoAllowedAuth(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(schedulerTestExecutor{})
	if _, err := manager.Register(context.Background(), &Auth{ID: "codex-a", Provider: "test"}); err != nil {
		t.Fatalf("register codex-a: %v", err)
	}

	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ClientIDMetadataKey:         "openclaw-foxy",
		cliproxyexecutor.AllowedAuthPoolsMetadataKey: map[string]string{},
	}}

	_, _, err := manager.pickNext(context.Background(), "test", "", opts, nil)
	authErr, ok := err.(*Error)
	if !ok || authErr == nil {
		t.Fatalf("pickNext() error = %T %v, want *Error", err, err)
	}
	if authErr.Code != "pool_access_denied" {
		t.Fatalf("error code = %q, want pool_access_denied", authErr.Code)
	}
	if authErr.StatusCode() != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", authErr.StatusCode(), http.StatusForbidden)
	}
}
