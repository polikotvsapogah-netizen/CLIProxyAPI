package auth

import (
	"fmt"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type routeAccessPolicy struct {
	restricted   bool
	authIDToPool map[string]string
	clientID     string
	clientLabel  string
}

func routeAccessPolicyFromMetadata(meta map[string]any) routeAccessPolicy {
	policy := routeAccessPolicy{}
	if len(meta) == 0 {
		return policy
	}
	if clientID, ok := meta[cliproxyexecutor.ClientIDMetadataKey].(string); ok {
		policy.clientID = strings.TrimSpace(clientID)
	}
	if clientLabel, ok := meta[cliproxyexecutor.ClientLabelMetadataKey].(string); ok {
		policy.clientLabel = strings.TrimSpace(clientLabel)
	}
	raw, ok := meta[cliproxyexecutor.AllowedAuthPoolsMetadataKey]
	if !ok {
		return policy
	}
	policy.restricted = true
	policy.authIDToPool = make(map[string]string)
	switch typed := raw.(type) {
	case map[string]string:
		for authID, poolID := range typed {
			authID = strings.TrimSpace(authID)
			if authID != "" {
				policy.authIDToPool[authID] = strings.TrimSpace(poolID)
			}
		}
	case map[string]any:
		for authID, poolID := range typed {
			authID = strings.TrimSpace(authID)
			if authID != "" {
				policy.authIDToPool[authID] = strings.TrimSpace(toString(poolID))
			}
		}
	case []string:
		for _, authID := range typed {
			authID = strings.TrimSpace(authID)
			if authID != "" {
				policy.authIDToPool[authID] = ""
			}
		}
	case []any:
		for _, authID := range typed {
			authIDText := strings.TrimSpace(toString(authID))
			if authIDText != "" {
				policy.authIDToPool[authIDText] = ""
			}
		}
	}
	return policy
}

func (p routeAccessPolicy) allows(auth *Auth) bool {
	if !p.restricted {
		return true
	}
	if auth == nil {
		return false
	}
	_, ok := p.authIDToPool[strings.TrimSpace(auth.ID)]
	return ok
}

func (p routeAccessPolicy) selectedPoolID(authID string) string {
	if !p.restricted {
		return ""
	}
	return strings.TrimSpace(p.authIDToPool[strings.TrimSpace(authID)])
}

func (p routeAccessPolicy) annotate(auth *Auth) {
	if auth == nil {
		return
	}
	if !p.restricted && p.clientID == "" {
		return
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if p.clientID != "" {
		auth.Attributes[cliproxyexecutor.ClientIDMetadataKey] = p.clientID
	}
	if p.clientLabel != "" {
		auth.Attributes[cliproxyexecutor.ClientLabelMetadataKey] = p.clientLabel
	}
	if poolID := p.selectedPoolID(auth.ID); poolID != "" {
		auth.Attributes[cliproxyexecutor.SelectedPoolMetadataKey] = poolID
	}
}

func authAccessRestricted(meta map[string]any) bool {
	return routeAccessPolicyFromMetadata(meta).restricted
}

func poolAccessDeniedError() *Error {
	return &Error{
		Code:       "pool_access_denied",
		Message:    "client has no accessible account in its allowed pools",
		HTTPStatus: http.StatusForbidden,
	}
}

func toString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		if v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
