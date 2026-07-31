package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// codexUsageURL is the ChatGPT backend endpoint that returns the live
// rate-limit window state for the Codex CLI account behind the access token.
// We use /wham/usage (same payload as /codex/usage) to match the URL the
// built-in /quota dashboard page already calls — that way both views agree.
const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

// codexRateLimitsTimeout caps the time spent fetching usage info per account.
const codexRateLimitsTimeout = 15 * time.Second

// codexRateLimitsRequestSpacing controls the per-request delay range. ChatGPT's
// edge (Cloudflare) issues a 403 challenge when many requests arrive close
// together with the same source-IP / User-Agent, so the handler walks accounts
// sequentially with a randomised 8.0–12.0 second pause between calls.
const (
	codexRateLimitsSpacingMin = 8 * time.Second
	codexRateLimitsSpacingMax = 12 * time.Second
)

// codexRateLimitsCacheTTL is how long a successful aggregate response stays
// cached in memory. Press-the-button-again within this window returns the same
// snapshot rather than hitting OpenAI again.
const codexRateLimitsCacheTTL = 60 * time.Second

// codexCLIVersions is a small pool of plausible Codex CLI version strings we
// rotate through on the User-Agent header to dodge the basic
// "same UA in a tight burst" CF fingerprint.
var codexCLIVersions = []string{
	"0.20.0", "0.20.1", "0.20.2", "0.21.0", "0.21.1", "0.22.0",
}

// nextCodexUserAgent returns a randomised but well-formed Codex CLI UA.
func nextCodexUserAgent() string {
	v := codexCLIVersions[rand.Intn(len(codexCLIVersions))]
	return fmt.Sprintf("codex_cli_rs/%s", v)
}

// nextCodexSpacing picks a random pause within the configured range.
func nextCodexSpacing() time.Duration {
	d := codexRateLimitsSpacingMax - codexRateLimitsSpacingMin
	if d <= 0 {
		return codexRateLimitsSpacingMin
	}
	return codexRateLimitsSpacingMin + time.Duration(rand.Int63n(int64(d)))
}

type codexRateLimitsCacheEntry struct {
	at      time.Time
	payload gin.H
}

var (
	codexRateLimitsCacheMu sync.Mutex
	codexRateLimitsCache   *codexRateLimitsCacheEntry
)

type codexRateLimitsAccount struct {
	ID            string         `json:"id"`
	AuthIndex     string         `json:"auth_index"`
	Email         string         `json:"email,omitempty"`
	Label         string         `json:"label,omitempty"`
	Provider      string         `json:"provider,omitempty"`
	JWTPlanType   string         `json:"jwt_plan_type,omitempty"`
	APIPlanType   string         `json:"api_plan_type,omitempty"`
	ProxyStatus   string         `json:"proxy_status,omitempty"`
	UpstreamCode  int            `json:"upstream_status_code,omitempty"`
	Allowed       *bool          `json:"allowed,omitempty"`
	LimitReached  *bool          `json:"limit_reached,omitempty"`
	ReachedType   string         `json:"rate_limit_reached_type,omitempty"`
	PrimaryWindow *codexWindow   `json:"primary_window,omitempty"`
	SecondaryWnd  *codexWindow   `json:"secondary_window,omitempty"`
	Credits       map[string]any `json:"credits,omitempty"`
	SpendControl  map[string]any `json:"spend_control,omitempty"`
	Pools         []codexPoolRef `json:"pools,omitempty"`
	Error         string         `json:"error,omitempty"`
	FetchedAt     string         `json:"fetched_at"`
}

type codexWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at,omitempty"`
}

type codexPoolRef struct {
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
}

// CodexRateLimits aggregates the live rate-limit window state for every
// codex account known to the proxy. It calls the ChatGPT usage endpoint on
// behalf of each account using its current access token and returns the raw
// window data plus per-account error status.
//
// Endpoint:
//
//	GET /v0/management/codex-rate-limits
//
// Authentication: same management key as other /v0/management/* endpoints.
//
// Response JSON shape:
//
//	{
//	  "fetched_at": "2026-05-01T12:34:56Z",
//	  "accounts": [
//	    {
//	      "id": "codex-foo.json",
//	      "auth_index": "abc123...",
//	      "email": "...",
//	      "jwt_plan_type": "plus",
//	      "api_plan_type": "free",
//	      "proxy_status": "ok",        // or "token_missing" / "http_error" / "non_json"
//	      "upstream_status_code": 200,
//	      "allowed": true,
//	      "limit_reached": false,
//	      "primary_window":   { "used_percent": 76, "limit_window_seconds": 604800, ... },
//	      "secondary_window": null,
//	      "credits": {...},
//	      "spend_control": {...},
//	      "pools": [{"id":"codex-main","label":"Codex Main"}, ...]
//	    },
//	    ...
//	  ]
//	}
func (h *Handler) CodexRateLimits(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager not initialised"})
		return
	}

	force := strings.EqualFold(strings.TrimSpace(c.Query("force")), "1") ||
		strings.EqualFold(strings.TrimSpace(c.Query("force")), "true")

	if !force {
		codexRateLimitsCacheMu.Lock()
		if codexRateLimitsCache != nil && time.Since(codexRateLimitsCache.at) < codexRateLimitsCacheTTL {
			cached := codexRateLimitsCache.payload
			codexRateLimitsCacheMu.Unlock()
			c.Header("X-Codex-Rate-Limits-Source", "cache")
			c.JSON(http.StatusOK, cached)
			return
		}
		codexRateLimitsCacheMu.Unlock()
	}

	auths := h.authManager.List()
	codexAuths := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
			continue
		}
		auth.EnsureIndex()
		codexAuths = append(codexAuths, auth)
	}

	results := make([]codexRateLimitsAccount, len(codexAuths))
	ctx := c.Request.Context()
	for i, auth := range codexAuths {
		if i > 0 {
			select {
			case <-ctx.Done():
				results[i] = codexRateLimitsAccount{
					ID:          auth.ID,
					AuthIndex:   auth.Index,
					Provider:    strings.TrimSpace(auth.Provider),
					Label:       strings.TrimSpace(auth.Label),
					Email:       authEmail(auth),
					ProxyStatus: "request_cancelled",
					Error:       ctx.Err().Error(),
					FetchedAt:   time.Now().UTC().Format(time.RFC3339),
				}
				continue
			case <-time.After(nextCodexSpacing()):
			}
		}
		results[i] = h.fetchCodexRateLimit(ctx, auth)
	}

	payload := gin.H{
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
		"accounts":   results,
	}
	codexRateLimitsCacheMu.Lock()
	codexRateLimitsCache = &codexRateLimitsCacheEntry{at: time.Now(), payload: payload}
	codexRateLimitsCacheMu.Unlock()
	c.Header("X-Codex-Rate-Limits-Source", "live")
	c.JSON(http.StatusOK, payload)
}

func (h *Handler) fetchCodexRateLimit(ctx context.Context, auth *coreauth.Auth) codexRateLimitsAccount {
	out := codexRateLimitsAccount{
		ID:        auth.ID,
		AuthIndex: auth.Index,
		Provider:  strings.TrimSpace(auth.Provider),
		Label:     strings.TrimSpace(auth.Label),
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if email := authEmail(auth); email != "" {
		out.Email = email
	}

	// Capture pools the account is currently a member of so the UI can show
	// the impact at a glance without doing another lookup.
	if h.cfg != nil {
		for _, pool := range h.cfg.AccountPools {
			for _, authID := range pool.AuthIDs {
				if strings.EqualFold(strings.TrimSpace(authID), strings.TrimSpace(auth.ID)) {
					out.Pools = append(out.Pools, codexPoolRef{ID: pool.ID, Label: pool.Label})
					break
				}
			}
		}
	}

	// JWT plan_type and chatgpt_account_id from id_token. The account_id
	// must be sent to OpenAI as the Chatgpt-Account-Id header — without it
	// the upstream returns usage for the user's *default* context, which for
	// org/team members is not the same subscription as the access token was
	// issued against. The built-in /quota page already does this; we mirror
	// it here so numbers match.
	chatgptAccountID := ""
	if claims := extractCodexIDTokenClaims(auth); claims != nil {
		if v, ok := claims["plan_type"].(string); ok && strings.TrimSpace(v) != "" {
			out.JWTPlanType = strings.TrimSpace(v)
		}
		if v, ok := claims["chatgpt_account_id"].(string); ok && strings.TrimSpace(v) != "" {
			chatgptAccountID = strings.TrimSpace(v)
		}
	}

	token, err := h.resolveTokenForAuth(ctx, auth)
	if err != nil || strings.TrimSpace(token) == "" {
		out.ProxyStatus = "token_missing"
		if err != nil {
			out.Error = err.Error()
		} else {
			out.Error = "access token not available"
		}
		return out
	}

	reqCtx, cancel := context.WithTimeout(ctx, codexRateLimitsTimeout)
	defer cancel()
	req, errReq := http.NewRequestWithContext(reqCtx, http.MethodGet, codexUsageURL, nil)
	if errReq != nil {
		out.ProxyStatus = "request_build_failed"
		out.Error = errReq.Error()
		return out
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", nextCodexUserAgent())
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Accept", "application/json")
	if chatgptAccountID != "" {
		req.Header.Set("Chatgpt-Account-Id", chatgptAccountID)
	}
	// Force a fresh TCP/TLS connection per upstream call. Reusing keep-alive
	// connections across many sequential requests causes ChatGPT's Cloudflare
	// edge to flag the burst as bot traffic and respond with 403 challenges.
	req.Close = true
	req.Header.Set("Connection", "close")

	transport := h.apiCallTransport(auth)
	if t, ok := transport.(*http.Transport); ok && t != nil {
		t = t.Clone()
		t.DisableKeepAlives = true
		// Keep HTTP/2 — chatgpt.com/backend-api uses h2 and reverting to HTTP/1.1
		// causes "malformed HTTP response" because the server still talks h2.
		transport = t
	}
	httpClient := &http.Client{
		Timeout:   codexRateLimitsTimeout,
		Transport: transport,
	}
	resp, errDo := httpClient.Do(req)
	if errDo != nil {
		out.ProxyStatus = "http_error"
		out.Error = errDo.Error()
		return out
	}
	defer resp.Body.Close()

	out.UpstreamCode = resp.StatusCode
	bodyBytes, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil {
		out.ProxyStatus = "read_error"
		out.Error = errRead.Error()
		return out
	}

	if resp.StatusCode != http.StatusOK {
		out.ProxyStatus = "upstream_error"
		out.Error = strings.TrimSpace(string(bodyBytes))
		return out
	}

	var payload struct {
		PlanType  string `json:"plan_type"`
		RateLimit struct {
			Allowed         *bool        `json:"allowed"`
			LimitReached    *bool        `json:"limit_reached"`
			PrimaryWindow   *codexWindow `json:"primary_window"`
			SecondaryWindow *codexWindow `json:"secondary_window"`
		} `json:"rate_limit"`
		ReachedType  any            `json:"rate_limit_reached_type"`
		Credits      map[string]any `json:"credits"`
		SpendControl map[string]any `json:"spend_control"`
	}
	if errParse := json.Unmarshal(bodyBytes, &payload); errParse != nil {
		out.ProxyStatus = "non_json"
		out.Error = errParse.Error()
		return out
	}

	out.ProxyStatus = "ok"
	out.APIPlanType = strings.TrimSpace(payload.PlanType)
	out.Allowed = payload.RateLimit.Allowed
	out.LimitReached = payload.RateLimit.LimitReached
	out.PrimaryWindow = payload.RateLimit.PrimaryWindow
	out.SecondaryWnd = payload.RateLimit.SecondaryWindow
	out.Credits = payload.Credits
	out.SpendControl = payload.SpendControl
	out.ReachedType = stringifyReachedType(payload.ReachedType)
	return out
}

// stringifyReachedType normalises rate_limit_reached_type, which OpenAI returns
// either as a string or as an object like {"type":"...","details":...}.
func stringifyReachedType(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if t, ok := v["type"].(string); ok && strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
		if encoded, err := json.Marshal(v); err == nil {
			return string(encoded)
		}
	}
	if encoded, err := json.Marshal(raw); err == nil {
		return strings.Trim(string(encoded), "\"")
	}
	return ""
}
