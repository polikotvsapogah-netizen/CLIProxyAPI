package management

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (h *Handler) findAuthRecordByNameOrID(name string) (*coreauth.Auth, bool) {
	name = strings.TrimSpace(name)
	if name == "" || h == nil || h.authManager == nil {
		return nil, false
	}
	if auth, ok := h.authManager.GetByID(name); ok {
		return auth, true
	}
	for _, auth := range h.authManager.List() {
		if auth == nil {
			continue
		}
		if auth.FileName == name || auth.ID == name {
			return auth, true
		}
	}
	return nil, false
}

func activeAlternativeAuthCount(auths []*coreauth.Auth, target *coreauth.Auth, now time.Time) int {
	if target == nil {
		return 0
	}
	targetID := strings.TrimSpace(target.ID)
	targetProvider := strings.ToLower(strings.TrimSpace(target.Provider))
	if targetProvider == "" {
		return 0
	}
	count := 0
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.ID) == "" || strings.TrimSpace(auth.ID) == targetID {
			continue
		}
		if strings.ToLower(strings.TrimSpace(auth.Provider)) != targetProvider {
			continue
		}
		if auth.Disabled || auth.Status == coreauth.StatusDisabled || auth.Unavailable {
			continue
		}
		if coreauth.AuthPausedAt(auth, now) {
			continue
		}
		count++
	}
	return count
}

// PatchAuthFilePause sets or clears a temporary operator pause for an auth file.
// Paused auths remain logged in and visible, but selectors exclude them until paused_until.
func (h *Handler) PatchAuthFilePause(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		Name            string  `json:"name"`
		PausedUntil     *string `json:"paused_until"`
		PauseUntil      *string `json:"pause_until"`
		DurationSeconds *int64  `json:"duration_seconds"`
		Resume          bool    `json:"resume"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}

	targetAuth, ok := h.findAuthRecordByNameOrID(name)
	if !ok || targetAuth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth file not found"})
		return
	}

	now := time.Now().UTC()
	pause := false
	pausedUntil := time.Time{}
	if req.Resume {
		pause = false
	} else if req.DurationSeconds != nil && *req.DurationSeconds > 0 {
		pause = true
		pausedUntil = now.Add(time.Duration(*req.DurationSeconds) * time.Second).UTC().Truncate(time.Second)
	} else {
		rawUntil := req.PausedUntil
		if rawUntil == nil {
			rawUntil = req.PauseUntil
		}
		if rawUntil == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "paused_until or resume is required"})
			return
		}
		text := strings.TrimSpace(*rawUntil)
		if text != "" {
			parsed, err := time.Parse(time.RFC3339Nano, text)
			if err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": "paused_until must be RFC3339"})
				return
			}
			pause = true
			pausedUntil = parsed.UTC().Truncate(time.Second)
		}
	}

	alternatives := activeAlternativeAuthCount(h.authManager.List(), targetAuth, now)
	if pause {
		if !pausedUntil.After(now) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "paused_until must be in the future"})
			return
		}
		if alternatives <= 0 {
			c.JSON(http.StatusConflict, gin.H{
				"error":         fmt.Sprintf("cannot pause last active %s auth", strings.TrimSpace(targetAuth.Provider)),
				"provider":      strings.TrimSpace(targetAuth.Provider),
				"auth_id":       targetAuth.ID,
				"active_others": alternatives,
			})
			return
		}
	}

	if targetAuth.Metadata == nil {
		targetAuth.Metadata = make(map[string]any)
	}
	if pause {
		targetAuth.Metadata[coreauth.PauseUntilMetadataKey] = pausedUntil.Format(time.RFC3339)
		targetAuth.Metadata["paused_at"] = now.Format(time.RFC3339)
	} else {
		delete(targetAuth.Metadata, coreauth.PauseUntilMetadataKey)
		delete(targetAuth.Metadata, "pause_until")
		delete(targetAuth.Metadata, "pausedUntil")
		delete(targetAuth.Metadata, "paused_at")
	}
	targetAuth.UpdatedAt = now

	if _, err := h.authManager.Update(c.Request.Context(), targetAuth); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update auth: %v", err)})
		return
	}

	resp := gin.H{
		"status":              "ok",
		"paused":              pause,
		"active_alternatives": alternatives,
	}
	if pause {
		resp["paused_until"] = pausedUntil.Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, resp)
}
