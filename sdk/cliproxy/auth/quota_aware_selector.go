package auth

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// QuotaAwareSelector spends Claude subscription quota that resets soonest while
// retaining the usual availability, priority, and retry filtering.
type QuotaAwareSelector struct{}

const quotaAwareSwitchThreshold = 0.98

func (s *QuotaAwareSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	_ = opts
	now := time.Now()
	available, errAvailable := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	if !strings.EqualFold(provider, "claude") {
		return available[0], nil
	}
	var picked *Auth
	for _, candidate := range available {
		if quotaAwareBetter(candidate, picked, model, now) {
			picked = candidate
		}
	}
	return picked, nil
}

type quotaAwareRank struct {
	underThreshold bool
	hasWeeklyReset bool
	weeklyReset    time.Time
	weeklyUsed     float64
	sessionUsed    float64
}

func quotaAwareBetter(candidate, current *Auth, model string, now time.Time) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	a, b := quotaAwareRankFor(candidate, model, now), quotaAwareRankFor(current, model, now)
	if a.underThreshold != b.underThreshold {
		return a.underThreshold
	}
	if a.hasWeeklyReset != b.hasWeeklyReset {
		return a.hasWeeklyReset
	}
	if a.hasWeeklyReset && !a.weeklyReset.Equal(b.weeklyReset) {
		return a.weeklyReset.Before(b.weeklyReset)
	}
	if a.weeklyUsed != b.weeklyUsed {
		return a.weeklyUsed > b.weeklyUsed
	}
	if a.sessionUsed != b.sessionUsed {
		return a.sessionUsed > b.sessionUsed
	}
	return candidate.ID < current.ID
}

func quotaAwareRankFor(auth *Auth, model string, now time.Time) quotaAwareRank {
	rank := quotaAwareRank{underThreshold: true}
	if auth == nil || !strings.EqualFold(auth.Provider, "claude") {
		return rank
	}
	signals := auth.Quota.Signals
	weekly := "7d"
	if strings.Contains(strings.ToLower(model), "fable") &&
		(quotaSignal(signals, "7d_oi-utilization") != "" || quotaSignal(signals, "7d_oi-status") != "") {
		weekly = "7d_oi"
	}
	if used, valid := quotaUtilization(signals, "5h", now); valid {
		rank.sessionUsed = used
		if used >= quotaAwareSwitchThreshold {
			rank.underThreshold = false
		}
	}
	if quotaRejected(signals, "5h", now) || quotaRejected(signals, "7d", now) {
		rank.underThreshold = false
	}
	if sharedUsed, valid := quotaUtilization(signals, "7d", now); valid && sharedUsed >= quotaAwareSwitchThreshold {
		rank.underThreshold = false
	}
	if used, valid := quotaUtilization(signals, weekly, now); valid {
		rank.weeklyUsed = used
		if used >= quotaAwareSwitchThreshold {
			rank.underThreshold = false
		}
	}
	if weekly != "7d" && quotaRejected(signals, weekly, now) {
		rank.underThreshold = false
	}
	if reset, valid := quotaReset(signals, weekly); valid && reset.After(now) {
		rank.hasWeeklyReset = true
		rank.weeklyReset = reset
	}
	return rank
}

func quotaSignal(signals map[string]string, suffix string) string {
	name := "anthropic-ratelimit-unified-" + suffix
	for key, value := range signals {
		if strings.EqualFold(key, name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func quotaReset(signals map[string]string, bucket string) (time.Time, bool) {
	value := quotaSignal(signals, bucket+"-reset")
	seconds, errParse := strconv.ParseInt(value, 10, 64)
	if errParse != nil || seconds <= 0 {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

func quotaUtilization(signals map[string]string, bucket string, now time.Time) (float64, bool) {
	value := quotaSignal(signals, bucket+"-utilization")
	used, errParse := strconv.ParseFloat(value, 64)
	if errParse != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 {
		return 0, false
	}
	if reset, valid := quotaReset(signals, bucket); valid && !reset.After(now) {
		return 0, false
	}
	return used, true
}

func quotaRejected(signals map[string]string, bucket string, now time.Time) bool {
	if reset, valid := quotaReset(signals, bucket); valid && !reset.After(now) {
		return false
	}
	return strings.EqualFold(quotaSignal(signals, bucket+"-status"), "rejected")
}
