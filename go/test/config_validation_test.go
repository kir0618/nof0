package test

import (
	"math"
	"testing"

	"nof0-api/pkg/confkit"
	managerpkg "nof0-api/pkg/manager"
)

// TestManagerConfigAllocationBudget ensures the committed manager.yaml always satisfies
// the total-allocation constraint (<= 100 - reserve_equity_pct). This test is run in CI
// so misconfigured YAML files fail fast before deployment.
func TestManagerConfigAllocationBudget(t *testing.T) {
	path := confkit.MustProjectPath("etc/manager.yaml")
	cfg, err := managerpkg.LoadConfig(path)
	if err != nil {
		t.Fatalf("load manager config: %v", err)
	}
	var total float64
	for _, trader := range cfg.Traders {
		total += trader.AllocationPct
	}
	maxAllowed := 100 - cfg.Manager.ReserveEquityPct
	if total > maxAllowed+1e-6 {
		t.Fatalf("trader allocation %.2f exceeds manager budget %.2f (reserve=%.2f)", total, maxAllowed, cfg.Manager.ReserveEquityPct)
	}
	if total > 100+1e-6 {
		t.Fatalf("trader allocation %.2f exceeds hard limit 100%%", total)
	}
	if math.Abs(total+cfg.Manager.ReserveEquityPct-100) > 20 {
		t.Logf("allocation %.2f + reserve %.2f leaves %.2f%% idle", total, cfg.Manager.ReserveEquityPct, 100-total-cfg.Manager.ReserveEquityPct)
	}
}
