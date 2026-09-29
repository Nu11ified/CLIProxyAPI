package management

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// GetRoutingModels lists models registered to credentials for a provider.
// The dashboard uses this list so its preview tracks model catalog updates.
func (h *Handler) GetRoutingModels(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}
	provider := strings.ToLower(strings.TrimSpace(c.Query("provider")))
	if provider != "claude" && provider != "codex" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported provider"})
		return
	}
	type modelEntry struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	}
	byID := make(map[string]modelEntry)
	reg := registry.GetGlobalRegistry()
	for _, auth := range h.authManager.List() {
		if auth == nil || auth.Provider != provider || auth.Disabled {
			continue
		}
		for _, model := range reg.GetModelsForClient(auth.ID) {
			if model == nil || model.ID == "" {
				continue
			}
			name := model.DisplayName
			if name == "" {
				name = model.ID
			}
			byID[model.ID] = modelEntry{ID: model.ID, DisplayName: name}
		}
	}
	models := make([]modelEntry, 0, len(byID))
	for _, model := range byID {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	c.JSON(http.StatusOK, gin.H{"models": models})
}

// GetRoutingPreview asks the live selector which credential a new request would
// use. Existing sessions may remain pinned to another credential.
func (h *Handler) GetRoutingPreview(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}
	model := strings.TrimSpace(c.DefaultQuery("model", "claude-opus-5-5"))
	provider := strings.ToLower(strings.TrimSpace(c.DefaultQuery("provider", "claude")))
	if provider != "claude" && provider != "codex" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported provider"})
		return
	}
	if model == "" || len(model) > 128 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid model"})
		return
	}
	selected, err := h.authManager.SelectAuth(c.Request.Context(), provider, model, cliproxyexecutor.Options{})
	strategy := ""
	if h.cfg != nil {
		strategy = h.cfg.Routing.Strategy
	}
	response := gin.H{
		"model":       model,
		"provider":    provider,
		"observed_at": time.Now().UTC(),
		"strategy":    strategy,
	}
	if err != nil || selected == nil {
		response["error"] = "no credential is currently available for this model"
		c.JSON(http.StatusOK, response)
		return
	}
	selected.EnsureIndex()
	response["auth_index"] = selected.Index
	c.JSON(http.StatusOK, response)
}
