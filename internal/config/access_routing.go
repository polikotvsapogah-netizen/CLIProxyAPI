package config

import "strings"

// AccountPool groups OAuth/file-backed auth entries by stable auth ID.
type AccountPool struct {
	ID       string   `yaml:"id" json:"id"`
	Label    string   `yaml:"label,omitempty" json:"label,omitempty"`
	Provider string   `yaml:"provider,omitempty" json:"provider,omitempty"`
	AuthIDs  []string `yaml:"auth_ids,omitempty" json:"auth_ids,omitempty"`
	Disabled bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// ClientAccess describes a downstream client and the pools/secrets it may use.
type ClientAccess struct {
	ID             string   `yaml:"id" json:"id"`
	Label          string   `yaml:"label,omitempty" json:"label,omitempty"`
	APIKeys        []string `yaml:"api_keys,omitempty" json:"api_keys,omitempty"`
	AllowedPools   []string `yaml:"allowed_pools,omitempty" json:"allowed_pools,omitempty"`
	AllowedSecrets []string `yaml:"allowed_secrets,omitempty" json:"allowed_secrets,omitempty"`
	Disabled       bool     `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// GenericSecret stores a non-LLM service secret. ValueEncrypted is preferred
// when written by the management API; Value is accepted for hand-written configs.
type GenericSecret struct {
	ID             string            `yaml:"id" json:"id"`
	Label          string            `yaml:"label,omitempty" json:"label,omitempty"`
	Service        string            `yaml:"service,omitempty" json:"service,omitempty"`
	Value          string            `yaml:"value,omitempty" json:"value,omitempty"`
	ValueEncrypted string            `yaml:"value_encrypted,omitempty" json:"value_encrypted,omitempty"`
	AllowedClients []string          `yaml:"allowed_clients,omitempty" json:"allowed_clients,omitempty"`
	Metadata       map[string]string `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Disabled       bool              `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// RouteAccessSnapshot is the per-request authorization view passed into routing.
type RouteAccessSnapshot struct {
	ClientID     string
	ClientLabel  string
	AllowedPools []string
	AuthIDToPool map[string]string
	Restricted   bool
}

// SanitizeAccessRouting normalizes account pools, client access entries, and generic secrets.
func (cfg *SDKConfig) SanitizeAccessRouting() {
	if cfg == nil {
		return
	}
	cfg.AccountPools = sanitizeAccountPools(cfg.AccountPools)
	cfg.ClientAccess = sanitizeClientAccess(cfg.ClientAccess)
	cfg.GenericSecrets = sanitizeGenericSecrets(cfg.GenericSecrets)
}

// ClientAPIKeyMetadata returns client API key metadata keyed by raw client key.
func (cfg *SDKConfig) ClientAPIKeyMetadata() map[string]ClientAccess {
	if cfg == nil || len(cfg.ClientAccess) == 0 {
		return nil
	}
	out := make(map[string]ClientAccess)
	for _, client := range cfg.ClientAccess {
		if client.Disabled || client.ID == "" {
			continue
		}
		for _, key := range client.APIKeys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if _, exists := out[key]; !exists {
				out[key] = client
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// AllProxyAPIKeys returns legacy global API keys plus scoped client keys.
func (cfg *SDKConfig) AllProxyAPIKeys() []string {
	if cfg == nil {
		return nil
	}
	keys := make([]string, 0, len(cfg.APIKeys))
	seen := make(map[string]struct{})
	add := func(key string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for _, key := range cfg.APIKeys {
		add(key)
	}
	for _, client := range cfg.ClientAccess {
		if client.Disabled {
			continue
		}
		for _, key := range client.APIKeys {
			add(key)
		}
	}
	return keys
}

// ClientForAPIKey returns the enabled client matched by raw API key.
func (cfg *SDKConfig) ClientForAPIKey(apiKey string) (ClientAccess, bool) {
	apiKey = strings.TrimSpace(apiKey)
	if cfg == nil || apiKey == "" {
		return ClientAccess{}, false
	}
	for _, client := range cfg.ClientAccess {
		if client.Disabled || client.ID == "" {
			continue
		}
		for _, key := range client.APIKeys {
			if strings.TrimSpace(key) == apiKey {
				return client, true
			}
		}
	}
	return ClientAccess{}, false
}

// RouteAccessForAPIKey resolves the account pools available to a client key.
func (cfg *SDKConfig) RouteAccessForAPIKey(apiKey string) RouteAccessSnapshot {
	client, ok := cfg.ClientForAPIKey(apiKey)
	if !ok {
		return RouteAccessSnapshot{}
	}
	snapshot := RouteAccessSnapshot{
		ClientID:     client.ID,
		ClientLabel:  client.Label,
		AllowedPools: append([]string(nil), client.AllowedPools...),
		AuthIDToPool: make(map[string]string),
		Restricted:   true,
	}
	if len(client.AllowedPools) == 0 || len(cfg.AccountPools) == 0 {
		return snapshot
	}
	allowedPoolSet := make(map[string]struct{}, len(client.AllowedPools))
	allowAllPools := false
	for _, poolID := range client.AllowedPools {
		poolID = strings.TrimSpace(poolID)
		if poolID == "" {
			continue
		}
		if poolID == "*" {
			allowAllPools = true
			continue
		}
		allowedPoolSet[poolID] = struct{}{}
	}
	for _, pool := range cfg.AccountPools {
		if pool.Disabled || pool.ID == "" {
			continue
		}
		if !allowAllPools {
			if _, okPool := allowedPoolSet[pool.ID]; !okPool {
				continue
			}
		}
		for _, authID := range pool.AuthIDs {
			authID = strings.TrimSpace(authID)
			if authID == "" {
				continue
			}
			if _, exists := snapshot.AuthIDToPool[authID]; !exists {
				snapshot.AuthIDToPool[authID] = pool.ID
			}
		}
	}
	return snapshot
}

// GenericSecretForClient resolves a readable secret for a scoped client.
func (cfg *SDKConfig) GenericSecretForClient(clientID, secretID string) (GenericSecret, bool) {
	clientID = strings.TrimSpace(clientID)
	secretID = strings.TrimSpace(secretID)
	if cfg == nil || clientID == "" || secretID == "" {
		return GenericSecret{}, false
	}
	for _, secret := range cfg.GenericSecrets {
		if secret.Disabled || secret.ID != secretID {
			continue
		}
		for _, allowed := range secret.AllowedClients {
			allowed = strings.TrimSpace(allowed)
			if allowed == "*" || allowed == clientID {
				return secret, true
			}
		}
	}
	return GenericSecret{}, false
}

// GenericSecretByID returns an enabled generic secret by ID.
func (cfg *SDKConfig) GenericSecretByID(secretID string) (GenericSecret, bool) {
	secretID = strings.TrimSpace(secretID)
	if cfg == nil || secretID == "" {
		return GenericSecret{}, false
	}
	for _, secret := range cfg.GenericSecrets {
		if !secret.Disabled && secret.ID == secretID {
			return secret, true
		}
	}
	return GenericSecret{}, false
}

func sanitizeAccountPools(in []AccountPool) []AccountPool {
	if len(in) == 0 {
		return nil
	}
	out := make([]AccountPool, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, pool := range in {
		pool.ID = strings.TrimSpace(pool.ID)
		if pool.ID == "" {
			continue
		}
		if _, exists := seen[pool.ID]; exists {
			continue
		}
		seen[pool.ID] = struct{}{}
		pool.Label = strings.TrimSpace(pool.Label)
		pool.Provider = strings.ToLower(strings.TrimSpace(pool.Provider))
		pool.AuthIDs = cleanStringList(pool.AuthIDs, false)
		out = append(out, pool)
	}
	return out
}

func sanitizeClientAccess(in []ClientAccess) []ClientAccess {
	if len(in) == 0 {
		return nil
	}
	out := make([]ClientAccess, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, client := range in {
		client.ID = strings.TrimSpace(client.ID)
		if client.ID == "" {
			continue
		}
		if _, exists := seen[client.ID]; exists {
			continue
		}
		seen[client.ID] = struct{}{}
		client.Label = strings.TrimSpace(client.Label)
		client.APIKeys = cleanStringList(client.APIKeys, false)
		client.AllowedPools = cleanStringList(client.AllowedPools, true)
		client.AllowedSecrets = cleanStringList(client.AllowedSecrets, true)
		out = append(out, client)
	}
	return out
}

func sanitizeGenericSecrets(in []GenericSecret) []GenericSecret {
	if len(in) == 0 {
		return nil
	}
	out := make([]GenericSecret, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, secret := range in {
		secret.ID = strings.TrimSpace(secret.ID)
		if secret.ID == "" {
			continue
		}
		if _, exists := seen[secret.ID]; exists {
			continue
		}
		seen[secret.ID] = struct{}{}
		secret.Label = strings.TrimSpace(secret.Label)
		secret.Service = strings.ToLower(strings.TrimSpace(secret.Service))
		secret.Value = strings.TrimSpace(secret.Value)
		secret.ValueEncrypted = strings.TrimSpace(secret.ValueEncrypted)
		secret.AllowedClients = cleanStringList(secret.AllowedClients, true)
		out = append(out, secret)
	}
	return out
}

func cleanStringList(values []string, keepWildcard bool) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if value == "*" && !keepWildcard {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
