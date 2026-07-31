package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPatchAuthFilePause_RejectsLastActiveProviderAuth(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(&memoryAuthStore{}, nil, nil)
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-one.json",
		FileName: "codex-one.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"type": "codex",
		},
	}); err != nil {
		t.Fatalf("register codex-one: %v", err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := `{"name":"codex-one.json","paused_until":"` + time.Now().UTC().Add(72*time.Hour).Format(time.RFC3339) + `"}`
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFilePause(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusConflict, rec.Code, rec.Body.String())
	}
	updated, _ := manager.GetByID("codex-one.json")
	if updated == nil || updated.Metadata["paused_until"] != nil {
		t.Fatalf("expected last active auth to remain unpaused, metadata=%#v", updated.Metadata)
	}
}

func TestPatchAuthFilePause_PersistsFuturePauseAndListMarksPaused(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	store := &memoryAuthStore{}
	manager := coreauth.NewManager(store, nil, nil)
	for _, id := range []string{"codex-one.json", "codex-two.json"} {
		if _, err := manager.Register(context.Background(), &coreauth.Auth{
			ID:       id,
			FileName: id,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"path": "/tmp/" + id,
			},
			Metadata: map[string]any{
				"type": "codex",
			},
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	pausedUntil := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	body := `{"name":"codex-one.json","paused_until":"` + pausedUntil.Format(time.RFC3339) + `"}`
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFilePause(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	updated, _ := manager.GetByID("codex-one.json")
	if updated == nil {
		t.Fatalf("expected paused auth to exist")
	}
	if got, _ := updated.Metadata["paused_until"].(string); got != pausedUntil.Format(time.RFC3339) {
		t.Fatalf("metadata.paused_until = %q, want %q", got, pausedUntil.Format(time.RFC3339))
	}

	listRec := httptest.NewRecorder()
	listCtx, _ := gin.CreateTestContext(listRec)
	listCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(listCtx)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	var paused map[string]any
	for _, item := range payload.Files {
		if item["name"] == "codex-one.json" {
			paused = item
			break
		}
	}
	if paused == nil {
		t.Fatalf("paused auth not found in list: %#v", payload.Files)
	}
	if got, _ := paused["paused"].(bool); !got {
		t.Fatalf("list paused = %#v, want true; item=%#v", paused["paused"], paused)
	}
	if got, _ := paused["paused_until"].(string); got == "" {
		t.Fatalf("list paused_until missing; item=%#v", paused)
	}
}
