package management

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"
	"time"
)

func TestClaudeUsageSignalsConvertsPercentAndScopedWeekly(t *testing.T) {
	var usage claudeUsage
	data := []byte(`{
		"five_hour":{"utilization":16,"resets_at":"2030-01-01T01:00:00Z"},
		"seven_day":{"utilization":4,"resets_at":"2030-01-02T01:00:00Z"},
		"limits":[{"group":"weekly","percent":91,"resets_at":"2030-01-03T01:00:00Z","scope":{"model":{"display_name":"Fable"}}}]
	}`)
	if err := json.Unmarshal(data, &usage); err != nil {
		t.Fatal(err)
	}
	signals := claudeUsageSignals(usage)
	for name, want := range map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization":    "0.16",
		"Anthropic-Ratelimit-Unified-7d-Utilization":    "0.04",
		"Anthropic-Ratelimit-Unified-7d_oi-Utilization": "0.91",
	} {
		if got := signals.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	reset, err := time.Parse(time.RFC3339, "2030-01-03T01:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if got := signals.Get("Anthropic-Ratelimit-Unified-7d_oi-Reset"); got != strconv.FormatInt(reset.Unix(), 10) {
		t.Errorf("Fable reset = %q", got)
	}
}

func TestClaudeUsageSignalsRejectsInvalidWindows(t *testing.T) {
	invalid := math.NaN()
	usage := claudeUsage{FiveHour: claudeUsageBucket{Utilization: &invalid, ResetsAt: "2030-01-01T00:00:00Z"}}
	if got := claudeUsageSignals(usage); len(got) != 0 {
		t.Fatalf("invalid usage produced signals: %#v", got)
	}
}

func TestClaudeUsageSignalsPreservesUnusedWindowWithoutReset(t *testing.T) {
	var usage claudeUsage
	if err := json.Unmarshal([]byte(`{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":100,"resets_at":"2030-01-02T01:00:00Z"}}`), &usage); err != nil {
		t.Fatal(err)
	}
	signals := claudeUsageSignals(usage)
	if got := signals.Get("Anthropic-Ratelimit-Unified-5h-Utilization"); got != "0" {
		t.Fatalf("unused window utilization = %q, want 0", got)
	}
	if got := signals.Get("Anthropic-Ratelimit-Unified-5h-Reset"); got != "" {
		t.Fatalf("unused window invented a reset: %q", got)
	}
	if got := signals.Get("Anthropic-Ratelimit-Unified-5h-Status"); got != "allowed" {
		t.Fatalf("unused window status = %q, want allowed", got)
	}
	if got := signals.Get("Anthropic-Ratelimit-Unified-7d-Status"); got != "rejected" {
		t.Fatalf("exhausted weekly window status = %q, want rejected", got)
	}
}

func TestClaudeUsageSignalsDoesNotInventAbsentUtilization(t *testing.T) {
	var usage claudeUsage
	if err := json.Unmarshal([]byte(`{"five_hour":{"utilization":null,"resets_at":null}}`), &usage); err != nil {
		t.Fatal(err)
	}
	if got := claudeUsageSignals(usage); len(got) != 0 {
		t.Fatalf("absent utilization produced signals: %#v", got)
	}
}
