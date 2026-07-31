package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func (s *Server) registerPolikotPublicRoutes() {
	secrets := s.engine.Group("/v0/secrets")
	secrets.Use(AuthMiddleware(s.accessManager))
	secrets.GET("/:id", s.getGenericSecret)
}

func (s *Server) getGenericSecret(c *gin.Context) {
	if s == nil || s.cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "server_not_ready"})
		return
	}

	rawAPIKey, exists := c.Get("userApiKey")
	if !exists {
		c.JSON(http.StatusForbidden, gin.H{"error": "client_access_required"})
		return
	}
	client, ok := s.cfg.ClientForAPIKey(fmt.Sprint(rawAPIKey))
	if !ok || client.Disabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "client_access_required"})
		return
	}

	secretID := strings.TrimSpace(c.Param("id"))
	secret, ok := s.cfg.GenericSecretByID(secretID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "secret_not_found"})
		return
	}
	if !clientAllowsSecret(client, secretID) && !secretAllowsClient(secret, client.ID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "secret_access_denied"})
		return
	}
	value, err := config.DecryptGenericSecretValue(secret)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "secret_decrypt_failed", "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"id":      secret.ID,
		"label":   secret.Label,
		"service": secret.Service,
		"value":   value,
	})
}

func clientAllowsSecret(client config.ClientAccess, secretID string) bool {
	secretID = strings.TrimSpace(secretID)
	for _, allowed := range client.AllowedSecrets {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || allowed == secretID {
			return true
		}
	}
	return false
}

func secretAllowsClient(secret config.GenericSecret, clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	for _, allowed := range secret.AllowedClients {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || allowed == clientID {
			return true
		}
	}
	return false
}
