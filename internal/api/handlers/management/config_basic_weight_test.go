package management

import "testing"

func TestNormalizeRoutingStrategyWeightedRoundRobin(t *testing.T) {
	for _, input := range []string{"weighted-round-robin", "weightedroundrobin", "wrr"} {
		got, ok := normalizeRoutingStrategy(input)
		if !ok || got != "weighted-round-robin" {
			t.Fatalf("normalizeRoutingStrategy(%q) = %q, %v; want weighted-round-robin, true", input, got, ok)
		}
	}
}

func TestNormalizeRoutingStrategyQuotaAware(t *testing.T) {
	got, ok := normalizeRoutingStrategy("quota-aware")
	if !ok || got != "quota-aware" {
		t.Fatalf("normalizeRoutingStrategy(quota-aware) = %q, %v", got, ok)
	}
}
