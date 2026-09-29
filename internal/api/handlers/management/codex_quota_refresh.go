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

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const codexUsageURL = "https://chatgpt.com/backend-api/wham/usage"

type codexUsageWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds int64    `json:"limit_window_seconds"`
	ResetAt            int64    `json:"reset_at"`
}

type codexUsageRateLimit struct {
	PrimaryWindow   *codexUsageWindow `json:"primary_window"`
	SecondaryWindow *codexUsageWindow `json:"secondary_window"`
}

type codexUsage struct {
	PlanType  string              `json:"plan_type"`
	RateLimit codexUsageRateLimit `json:"rate_limit"`
}

func codexUsageSignals(usage codexUsage) http.Header {
	headers := make(http.Header)
	add := func(name string, window *codexUsageWindow) {
		if window == nil || window.UsedPercent == nil || math.IsNaN(*window.UsedPercent) ||
			math.IsInf(*window.UsedPercent, 0) || *window.UsedPercent < 0 ||
			window.LimitWindowSeconds <= 0 || window.ResetAt <= 0 {
			return
		}
		prefix := "X-Codex-" + name + "-"
		headers.Set(prefix+"Used-Percent", strconv.FormatFloat(*window.UsedPercent, 'f', -1, 64))
		headers.Set(prefix+"Window-Minutes", strconv.FormatInt(window.LimitWindowSeconds/60, 10))
		headers.Set(prefix+"Reset-At", strconv.FormatInt(window.ResetAt, 10))
	}
	add("Primary", usage.RateLimit.PrimaryWindow)
	add("Secondary", usage.RateLimit.SecondaryWindow)
	if len(headers) > 0 && usage.PlanType != "" {
		headers.Set("X-Codex-Plan-Type", usage.PlanType)
	}
	return headers
}

func codexOAuthAuth(auth *coreauth.Auth) bool {
	if auth == nil || !strings.EqualFold(auth.Provider, "codex") || auth.Disabled || auth.Metadata == nil {
		return false
	}
	for _, name := range []string{"access_token", "refresh_token", "account_id"} {
		value, _ := auth.Metadata[name].(string)
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func (h *Handler) refreshCodexQuota(ctx context.Context, auth *coreauth.Auth) error {
	if !codexOAuthAuth(auth) {
		return fmt.Errorf("auth is not an enabled Codex OAuth credential")
	}
	requestUsage := func(auth *coreauth.Auth) (*http.Response, error) {
		token, _ := auth.Metadata["access_token"].(string)
		accountID, _ := auth.Metadata["account_id"].(string)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, codexUsageURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("ChatGPT-Account-Id", accountID)
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
		if errRefresh != nil || refreshed == nil || !codexOAuthAuth(refreshed) {
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
	var usage codexUsage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&usage); err != nil {
		return fmt.Errorf("decode usage: %w", err)
	}
	signals := codexUsageSignals(usage)
	if len(signals) == 0 {
		return fmt.Errorf("usage endpoint returned no supported quota windows")
	}
	return h.authManager.ObserveQuotaHeaders(auth.ID, signals, time.Now())
}
