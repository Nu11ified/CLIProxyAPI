package management

import (
	"encoding/json"
	"testing"
)

func TestCodexUsageSignalsKeepsWindowDuration(t *testing.T) {
	var usage codexUsage
	data := []byte(`{"plan_type":"pro","rate_limit":{"primary_window":{"used_percent":8,"limit_window_seconds":604800,"reset_at":1893632400},"secondary_window":null}}`)
	if err := json.Unmarshal(data, &usage); err != nil {
		t.Fatal(err)
	}
	signals := codexUsageSignals(usage)
	for name, want := range map[string]string{
		"X-Codex-Primary-Used-Percent":   "8",
		"X-Codex-Primary-Window-Minutes": "10080",
		"X-Codex-Primary-Reset-At":       "1893632400",
		"X-Codex-Plan-Type":              "pro",
	} {
		if got := signals.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}
