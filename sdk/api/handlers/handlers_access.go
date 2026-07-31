package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func (h *BaseAPIHandler) requestExecutionMetadata(ctx context.Context) map[string]any {
	meta := requestExecutionMetadata(ctx)
	if h == nil || h.Cfg == nil {
		return meta
	}

	apiKey := ""
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
			if value, exists := ginCtx.Get("userApiKey"); exists {
				apiKey = strings.TrimSpace(fmt.Sprint(value))
			}
		}
	}
	access := h.Cfg.RouteAccessForAPIKey(apiKey)
	if !access.Restricted {
		return meta
	}

	meta[coreexecutor.ClientIDMetadataKey] = access.ClientID
	if access.ClientLabel != "" {
		meta[coreexecutor.ClientLabelMetadataKey] = access.ClientLabel
	}
	meta[coreexecutor.AllowedAuthPoolsMetadataKey] = access.AuthIDToPool
	return meta
}
