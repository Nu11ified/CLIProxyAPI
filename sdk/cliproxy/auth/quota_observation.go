package auth

import (
	"errors"
	"net/http"
	"time"
)

// ObserveQuotaHeaders records a read-only provider quota check without changing
// request counts, cooldowns, credentials, or persisted auth files.
func (m *Manager) ObserveQuotaHeaders(authID string, headers http.Header, observedAt time.Time) error {
	if m == nil || authID == "" {
		return errors.New("auth not found")
	}
	m.mu.Lock()
	auth := m.auths[authID]
	if auth == nil {
		m.mu.Unlock()
		return errors.New("auth not found")
	}
	if !auth.Quota.ObserveResponseHeadersForProvider(auth.Provider, headers, observedAt) {
		m.mu.Unlock()
		return errors.New("no quota signals")
	}
	auth.Generation++
	snapshot := auth.Clone()
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(snapshot)
	}
	return nil
}
