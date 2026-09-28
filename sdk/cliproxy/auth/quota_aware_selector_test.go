package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func quotaAwareTestAuth(id string, fiveHourUsed, weekUsed string, weekReset time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": fiveHourUsed,
			"Anthropic-Ratelimit-Unified-7d-Utilization": weekUsed,
			"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(weekReset.Unix(), 10),
		}},
	}
}

func TestQuotaAwareRanksUsableWeeklyCreditByReset(t *testing.T) {
	now := time.Now()
	soon := quotaAwareTestAuth("soon", "0.4", "0.7", now.Add(2*time.Hour))
	later := quotaAwareTestAuth("later", "0.1", "0.1", now.Add(5*24*time.Hour))
	if !quotaAwareBetter(soon, later, "claude-opus-5-5", now) {
		t.Fatal("weekly credit resetting soon should be spent first")
	}

	soon.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"] = "0.99"
	if !quotaAwareBetter(later, soon, "claude-opus-5-5", now) {
		t.Fatal("a near-exhausted 5-hour bucket should rotate to a usable account")
	}
	soon.Quota.Signals["Anthropic-Ratelimit-Unified-5h-Utilization"] = "0.4"
	soon.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] = "1.0"
	if !quotaAwareBetter(later, soon, "claude-opus-5-5", now) {
		t.Fatal("a spent weekly bucket should rotate to a usable account")
	}
}

func TestQuotaAwareHandlesResetAndModelScopedQuota(t *testing.T) {
	now := time.Now()
	soon := quotaAwareTestAuth("soon", "0.5", "0.5", now.Add(time.Hour))
	later := quotaAwareTestAuth("later", "0.2", "0.2", now.Add(4*24*time.Hour))
	soon.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Utilization"] = "1.02"
	soon.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Reset"] = strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	if !quotaAwareBetter(later, soon, "claude-fable-5", now) {
		t.Fatal("Fable-specific weekly exhaustion should not select the account")
	}
	if !quotaAwareBetter(soon, later, "claude-opus-5-5", now) {
		t.Fatal("Fable-specific exhaustion should not block Opus")
	}

	soon.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"] = strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)
	soon.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] = "1.0"
	if !quotaAwareRankFor(soon, "claude-opus-5-5", now).underThreshold {
		t.Fatal("utilization from a reset window should be treated as stale")
	}
}

func TestQuotaAwareManagerSelectionUsesObservedQuota(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, &QuotaAwareSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{provider: "claude"}
	for _, auth := range []*Auth{
		quotaAwareTestAuth("later", "0.1", "0.1", now.Add(5*24*time.Hour)),
		quotaAwareTestAuth("soon", "0.4", "0.6", now.Add(2*time.Hour)),
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
	}
	selected, errPick := manager.SelectAuth(context.Background(), "claude", "", cliproxyexecutor.Options{})
	if errPick != nil || selected == nil || selected.ID != "soon" {
		t.Fatalf("SelectAuth() = %v, %v; want soon", selected, errPick)
	}
}

func TestQuotaAwareSelectionUpdatesAfterResponseHeaders(t *testing.T) {
	now := time.Now()
	manager := NewManager(nil, &QuotaAwareSelector{}, nil)
	manager.executors["claude"] = schedulerTestExecutor{provider: "claude"}
	for _, id := range []string{"later", "soon"} {
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: "claude"}); errRegister != nil {
			t.Fatalf("Register(%s): %v", id, errRegister)
		}
	}
	for _, observation := range []struct {
		id    string
		reset time.Time
	}{
		{"later", now.Add(5 * 24 * time.Hour)},
		{"soon", now.Add(2 * time.Hour)},
	} {
		ctx := internallogging.WithResponseHeadersHolder(context.Background())
		internallogging.SetResponseHeaders(ctx, http.Header{
			"Anthropic-Ratelimit-Unified-5h-Utilization": []string{"0.3"},
			"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.5"},
			"Anthropic-Ratelimit-Unified-7d-Reset":       []string{strconv.FormatInt(observation.reset.Unix(), 10)},
		})
		manager.MarkResult(ctx, Result{AuthID: observation.id, Provider: "claude", Success: true})
	}
	selected, errPick := manager.SelectAuth(context.Background(), "claude", "", cliproxyexecutor.Options{})
	if errPick != nil || selected == nil || selected.ID != "soon" {
		t.Fatalf("SelectAuth() after quota observations = %v, %v; want soon", selected, errPick)
	}
}

func TestQuotaAwareQuotaObservationReachesOtherModelShards(t *testing.T) {
	old := QuotaState{Signals: map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.1"}}
	sonnet := &scheduledAuth{auth: &Auth{ID: "account", Provider: "claude", Quota: old.Clone()}}
	opus := &scheduledAuth{auth: &Auth{ID: "account", Provider: "claude", Quota: old.Clone()}}
	opus.auth.ModelStates = map[string]*ModelState{"opus": {Unavailable: true}}
	provider := &providerScheduler{modelShards: map[string]*modelScheduler{
		"sonnet": {entries: map[string]*scheduledAuth{"account": sonnet}},
		"opus":   {entries: map[string]*scheduledAuth{"account": opus}},
	}}
	fresh := QuotaState{
		ObservedAt: time.Now(),
		Signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.9"},
	}
	provider.refreshQuotaObservationLocked("account", fresh)
	for _, entry := range []*scheduledAuth{sonnet, opus} {
		if got := entry.auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"]; got != "0.9" {
			t.Fatalf("quota observation = %q, want 0.9", got)
		}
	}
	if !opus.auth.ModelStates["opus"].Unavailable {
		t.Fatal("credential quota refresh changed model-specific availability")
	}
}
