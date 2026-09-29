package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"

type claudeUsageBucket struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

type claudeUsageLimit struct {
	Group    string   `json:"group"`
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resets_at"`
	Scope    struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type claudeUsage struct {
	FiveHour claudeUsageBucket  `json:"five_hour"`
	SevenDay claudeUsageBucket  `json:"seven_day"`
	Limits   []claudeUsageLimit `json:"limits"`
}

func claudeUsageSignals(usage claudeUsage) http.Header {
	headers := make(http.Header)
	add := func(bucket string, used *float64, resetText string) {
		if used == nil || math.IsNaN(*used) || math.IsInf(*used, 0) || *used < 0 {
			return
		}
		prefix := "Anthropic-Ratelimit-Unified-" + bucket
		headers.Set(prefix+"-Utilization", strconv.FormatFloat(*used/100, 'f', -1, 64))
		// An unused Claude window reports utilization=0 with resets_at=null.
		// The percentage is still a reading; only its reset time is absent.
		reset, err := time.Parse(time.RFC3339Nano, resetText)
		if err == nil && !reset.IsZero() {
			headers.Set(prefix+"-Reset", strconv.FormatInt(reset.Unix(), 10))
		}
		if *used >= 100 && (reset.IsZero() || reset.After(time.Now())) {
			headers.Set(prefix+"-Status", "rejected")
		} else {
			headers.Set(prefix+"-Status", "allowed")
		}
	}
	add("5h", usage.FiveHour.Utilization, usage.FiveHour.ResetsAt)
	add("7d", usage.SevenDay.Utilization, usage.SevenDay.ResetsAt)
	for _, limit := range usage.Limits {
		if limit.Group == "weekly" && strings.Contains(strings.ToLower(limit.Scope.Model.DisplayName), "fable") {
			add("7d_oi", limit.Percent, limit.ResetsAt)
			break
		}
	}
	return headers
}

func claudeOAuthAuth(auth *coreauth.Auth) bool {
	if auth == nil || !strings.EqualFold(auth.Provider, "claude") || auth.Disabled || auth.Metadata == nil {
		return false
	}
	access, _ := auth.Metadata["access_token"].(string)
	refresh, _ := auth.Metadata["refresh_token"].(string)
	return strings.TrimSpace(access) != "" && strings.TrimSpace(refresh) != ""
}

func (h *Handler) refreshClaudeQuota(ctx context.Context, auth *coreauth.Auth) error {
	if !claudeOAuthAuth(auth) {
		return fmt.Errorf("auth is not an enabled Claude OAuth credential")
	}
	requestUsage := func(auth *coreauth.Auth) (*http.Response, error) {
		token, _ := auth.Metadata["access_token"].(string)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeUsageURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
		req.Header.Set("Accept", "application/json")
		return (&http.Client{Transport: h.apiCallTransport(auth, "")}).Do(req)
	}
	resp, err := requestUsage(auth)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		refreshed, errRefresh := h.authManager.ForceRefreshAuth(ctx, auth.ID)
		if errRefresh != nil || refreshed == nil {
			return fmt.Errorf("OAuth refresh failed")
		}
		resp, err = requestUsage(refreshed)
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("usage endpoint returned HTTP %d", resp.StatusCode)
	}
	var usage claudeUsage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&usage); err != nil {
		return fmt.Errorf("decode usage: %w", err)
	}
	signals := claudeUsageSignals(usage)
	if len(signals) == 0 {
		return fmt.Errorf("usage endpoint returned no supported quota windows")
	}
	return h.authManager.ObserveQuotaHeaders(auth.ID, signals, time.Now())
}

// RefreshOAuthQuotas refreshes one credential, or every enabled Claude and
// Codex OAuth credential when auth_index is omitted. It does not send model requests.
func (h *Handler) RefreshOAuthQuotas(c *gin.Context) {
	var body credentialQuotaRequest
	if err := c.ShouldBindJSON(&body); err != nil && err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	index := body.resolveAuthIndex()
	if index != "" && h.authByIndex(index) == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	result := make(map[string]string)
	for _, auth := range h.authManager.List() {
		if auth == nil || (index != "" && auth.Index != index) || !refreshableOAuthAuth(auth) {
			continue
		}
		if err := h.refreshOAuthQuota(c.Request.Context(), auth); err != nil {
			result[auth.Index] = err.Error()
		} else {
			result[auth.Index] = "ok"
		}
	}
	c.JSON(http.StatusOK, gin.H{"results": result})
}

func refreshableOAuthAuth(auth *coreauth.Auth) bool {
	return claudeOAuthAuth(auth) || codexOAuthAuth(auth)
}

func (h *Handler) refreshOAuthQuota(ctx context.Context, auth *coreauth.Auth) error {
	if claudeOAuthAuth(auth) {
		return h.refreshClaudeQuota(ctx, auth)
	}
	if codexOAuthAuth(auth) {
		return h.refreshCodexQuota(ctx, auth)
	}
	return fmt.Errorf("credential has no supported quota refresh")
}

func (h *Handler) startOAuthQuotaRefresh() {
	go func() {
		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		<-timer.C
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			h.mu.Lock()
			cfg := h.cfg
			manager := h.authManager
			h.mu.Unlock()
			if cfg != nil && strings.EqualFold(cfg.Routing.Strategy, "quota-aware") && manager != nil {
				for _, auth := range manager.List() {
					if !refreshableOAuthAuth(auth) {
						continue
					}
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					err := h.refreshOAuthQuota(ctx, auth)
					cancel()
					if err != nil {
						log.WithError(err).Warnf("OAuth quota refresh failed for %s", auth.Index)
					}
				}
			}
			<-ticker.C
		}
	}()
}
