package configaccess

import (
	"context"
	"net/http"
	"testing"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestProviderAuthenticatesClientAccessKeyWithMetadata(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		ClientAccess: []sdkconfig.ClientAccess{
			{ID: "ogod", Label: "oGod", APIKeys: []string{"sk-ogod"}},
		},
	}
	cfg.SanitizeAccessRouting()

	provider := newProvider("test", cfg.AllProxyAPIKeys(), cfg.ClientAPIKeyMetadata())
	req, err := http.NewRequest(http.MethodGet, "http://example.test/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-ogod")

	result, authErr := provider.Authenticate(context.Background(), req)
	if authErr != nil {
		t.Fatalf("Authenticate() error = %v", authErr)
	}
	if result.Principal != "sk-ogod" {
		t.Fatalf("Principal = %q, want sk-ogod", result.Principal)
	}
	if got := result.Metadata["client_id"]; got != "ogod" {
		t.Fatalf("client_id metadata = %q, want ogod", got)
	}
	if got := result.Metadata["client_label"]; got != "oGod" {
		t.Fatalf("client_label metadata = %q, want oGod", got)
	}
}
