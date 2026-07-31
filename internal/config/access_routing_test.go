package config

import "testing"

func TestRouteAccessForAPIKeyBuildsAllowedAuthMap(t *testing.T) {
	cfg := &SDKConfig{
		AccountPools: []AccountPool{
			{ID: "codex-main", Provider: "codex", AuthIDs: []string{"auth-1", "auth-2"}},
			{ID: "codex-reserve", Provider: "codex", AuthIDs: []string{"auth-3"}},
			{ID: "disabled", AuthIDs: []string{"auth-4"}, Disabled: true},
		},
		ClientAccess: []ClientAccess{
			{
				ID:           "openclaw",
				Label:        "OpenClaw",
				APIKeys:      []string{"sk-openclaw"},
				AllowedPools: []string{"codex-main", "disabled"},
			},
		},
	}
	cfg.SanitizeAccessRouting()

	access := cfg.RouteAccessForAPIKey("sk-openclaw")
	if !access.Restricted {
		t.Fatal("Restricted = false, want true")
	}
	if access.ClientID != "openclaw" {
		t.Fatalf("ClientID = %q, want openclaw", access.ClientID)
	}
	if got := access.AuthIDToPool["auth-1"]; got != "codex-main" {
		t.Fatalf("auth-1 pool = %q, want codex-main", got)
	}
	if _, ok := access.AuthIDToPool["auth-3"]; ok {
		t.Fatal("auth-3 unexpectedly allowed")
	}
	if _, ok := access.AuthIDToPool["auth-4"]; ok {
		t.Fatal("disabled pool auth unexpectedly allowed")
	}
}

func TestGenericSecretForClientRequiresAllowedClient(t *testing.T) {
	cfg := &SDKConfig{
		GenericSecrets: []GenericSecret{
			{ID: "elevenlabs-main", Value: "secret", AllowedClients: []string{"ogod"}},
		},
	}
	cfg.SanitizeAccessRouting()

	if _, ok := cfg.GenericSecretForClient("openclaw", "elevenlabs-main"); ok {
		t.Fatal("openclaw unexpectedly received secret")
	}
	if secret, ok := cfg.GenericSecretForClient("ogod", "elevenlabs-main"); !ok || secret.ID != "elevenlabs-main" {
		t.Fatalf("ogod secret = %#v, %v", secret, ok)
	}
}
