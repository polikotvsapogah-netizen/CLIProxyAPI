package management

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func (h *Handler) GetAccountPools(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"account-pools": h.cfg.AccountPools})
}

func (h *Handler) PutAccountPools(c *gin.Context) {
	var items []config.AccountPool
	if !bindItems(c, &items) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.AccountPools = items
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) PatchAccountPool(c *gin.Context) {
	var item config.AccountPool
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	replaced := false
	for idx := range h.cfg.AccountPools {
		if h.cfg.AccountPools[idx].ID == item.ID {
			h.cfg.AccountPools[idx] = item
			replaced = true
			break
		}
	}
	if !replaced {
		h.cfg.AccountPools = append(h.cfg.AccountPools, item)
	}
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) DeleteAccountPool(c *gin.Context) {
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.AccountPools = deleteAccountPoolByID(h.cfg.AccountPools, id)
	for idx := range h.cfg.ClientAccess {
		h.cfg.ClientAccess[idx].AllowedPools = removeString(h.cfg.ClientAccess[idx].AllowedPools, id)
	}
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) GetClientAccess(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"client-access": redactedClients(h.cfg.ClientAccess)})
}

func (h *Handler) PutClientAccess(c *gin.Context) {
	var items []config.ClientAccess
	if !bindItems(c, &items) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.ClientAccess = items
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) PatchClientAccess(c *gin.Context) {
	var item config.ClientAccess
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	replaced := false
	for idx := range h.cfg.ClientAccess {
		if h.cfg.ClientAccess[idx].ID == item.ID {
			if len(item.APIKeys) == 0 {
				item.APIKeys = h.cfg.ClientAccess[idx].APIKeys
			}
			h.cfg.ClientAccess[idx] = item
			replaced = true
			break
		}
	}
	if !replaced {
		h.cfg.ClientAccess = append(h.cfg.ClientAccess, item)
	}
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) DeleteClientAccess(c *gin.Context) {
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.ClientAccess = deleteClientByID(h.cfg.ClientAccess, id)
	for idx := range h.cfg.GenericSecrets {
		h.cfg.GenericSecrets[idx].AllowedClients = removeString(h.cfg.GenericSecrets[idx].AllowedClients, id)
	}
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) RotateClientAccessKey(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	key, err := generateClientKey()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "key_generation_failed", "message": err.Error()})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for idx := range h.cfg.ClientAccess {
		if h.cfg.ClientAccess[idx].ID == id {
			h.cfg.ClientAccess[idx].APIKeys = []string{key}
			h.cfg.SanitizeAccessRouting()
			if !h.persistLocked(c) {
				return
			}
			c.JSON(http.StatusOK, gin.H{"id": id, "api_key": key})
			return
		}
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
}

func (h *Handler) GetGenericSecrets(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"generic-secrets": redactedSecrets(h.cfg.GenericSecrets)})
}

func (h *Handler) PutGenericSecrets(c *gin.Context) {
	var items []config.GenericSecret
	if !bindItems(c, &items) {
		return
	}
	if err := encryptPlainSecretValues(items); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "encryption_failed", "message": err.Error()})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.GenericSecrets = items
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) PatchGenericSecret(c *gin.Context) {
	var item config.GenericSecret
	if err := c.ShouldBindJSON(&item); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	items := []config.GenericSecret{item}
	if err := encryptPlainSecretValues(items); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "encryption_failed", "message": err.Error()})
		return
	}
	item = items[0]
	h.mu.Lock()
	defer h.mu.Unlock()
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	replaced := false
	for idx := range h.cfg.GenericSecrets {
		if h.cfg.GenericSecrets[idx].ID == item.ID {
			if item.Value == "" && item.ValueEncrypted == "" {
				item.Value = h.cfg.GenericSecrets[idx].Value
				item.ValueEncrypted = h.cfg.GenericSecrets[idx].ValueEncrypted
			}
			h.cfg.GenericSecrets[idx] = item
			replaced = true
			break
		}
	}
	if !replaced {
		h.cfg.GenericSecrets = append(h.cfg.GenericSecrets, item)
	}
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) DeleteGenericSecret(c *gin.Context) {
	id := strings.TrimSpace(c.Query("id"))
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing id"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cfg.GenericSecrets = deleteSecretByID(h.cfg.GenericSecrets, id)
	for idx := range h.cfg.ClientAccess {
		h.cfg.ClientAccess[idx].AllowedSecrets = removeString(h.cfg.ClientAccess[idx].AllowedSecrets, id)
	}
	h.cfg.SanitizeAccessRouting()
	h.persistLocked(c)
}

func (h *Handler) GetAccountMatrix(c *gin.Context) {
	auths := []*coreauth.Auth{}
	if h.authManager != nil {
		auths = h.authManager.List()
	}
	accounts := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if entry := h.buildAuthFileEntry(auth); entry != nil {
			accounts = append(accounts, entry)
		}
	}
	sort.Slice(accounts, func(i, j int) bool {
		left, _ := accounts[i]["name"].(string)
		right, _ := accounts[j]["name"].(string)
		return strings.ToLower(left) < strings.ToLower(right)
	})
	c.JSON(http.StatusOK, gin.H{
		"accounts":        accounts,
		"account-pools":   h.cfg.AccountPools,
		"client-access":   redactedClients(h.cfg.ClientAccess),
		"generic-secrets": redactedSecrets(h.cfg.GenericSecrets),
	})
}

func (h *Handler) poolsForAuth(authID string) []gin.H {
	authID = strings.TrimSpace(authID)
	if h == nil || h.cfg == nil || authID == "" {
		return nil
	}
	out := make([]gin.H, 0)
	for _, pool := range h.cfg.AccountPools {
		if pool.Disabled {
			continue
		}
		for _, candidate := range pool.AuthIDs {
			if strings.TrimSpace(candidate) == authID {
				out = append(out, gin.H{"id": pool.ID, "label": pool.Label, "provider": pool.Provider})
				break
			}
		}
	}
	return out
}

func bindItems[T any](c *gin.Context, target *[]T) bool {
	data, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to read body"})
		return false
	}
	if err = json.Unmarshal(data, target); err == nil {
		return true
	}
	var obj struct {
		Items []T `json:"items"`
	}
	if err = json.Unmarshal(data, &obj); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return false
	}
	*target = obj.Items
	return true
}

func redactedClients(items []config.ClientAccess) []gin.H {
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		keys := make([]string, 0, len(item.APIKeys))
		for _, key := range item.APIKeys {
			keys = append(keys, redactKey(key))
		}
		out = append(out, gin.H{
			"id":              item.ID,
			"label":           item.Label,
			"api_keys":        keys,
			"allowed_pools":   item.AllowedPools,
			"allowed_secrets": item.AllowedSecrets,
			"disabled":        item.Disabled,
		})
	}
	return out
}

func redactedSecrets(items []config.GenericSecret) []gin.H {
	out := make([]gin.H, 0, len(items))
	for _, item := range items {
		out = append(out, gin.H{
			"id":              item.ID,
			"label":           item.Label,
			"service":         item.Service,
			"has_value":       strings.TrimSpace(item.Value) != "" || strings.TrimSpace(item.ValueEncrypted) != "",
			"encrypted":       strings.TrimSpace(item.ValueEncrypted) != "",
			"allowed_clients": item.AllowedClients,
			"metadata":        item.Metadata,
			"disabled":        item.Disabled,
		})
	}
	return out
}

func redactKey(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 8 {
		if key == "" {
			return ""
		}
		return "****"
	}
	return key[:4] + "..." + key[len(key)-4:]
}

func generateClientKey() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "sk-client-" + hex.EncodeToString(buf), nil
}

func encryptPlainSecretValues(items []config.GenericSecret) error {
	for idx := range items {
		if strings.TrimSpace(items[idx].Value) == "" || strings.TrimSpace(items[idx].ValueEncrypted) != "" {
			continue
		}
		encrypted, err := config.EncryptGenericSecretValue(items[idx].Value)
		if err != nil {
			return err
		}
		items[idx].Value = ""
		items[idx].ValueEncrypted = encrypted
	}
	return nil
}

func deleteAccountPoolByID(items []config.AccountPool, id string) []config.AccountPool {
	out := make([]config.AccountPool, 0, len(items))
	for _, item := range items {
		if item.ID != id {
			out = append(out, item)
		}
	}
	return out
}

func deleteClientByID(items []config.ClientAccess, id string) []config.ClientAccess {
	out := make([]config.ClientAccess, 0, len(items))
	for _, item := range items {
		if item.ID != id {
			out = append(out, item)
		}
	}
	return out
}

func deleteSecretByID(items []config.GenericSecret, id string) []config.GenericSecret {
	out := make([]config.GenericSecret, 0, len(items))
	for _, item := range items {
		if item.ID != id {
			out = append(out, item)
		}
	}
	return out
}

func removeString(items []string, value string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item) != value {
			out = append(out, item)
		}
	}
	return out
}
