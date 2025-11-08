package llm

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBudgetGuardRecordUsage(t *testing.T) {
	cfg := &BudgetConfig{
		DailyTokenLimit:   1000,
		AlertThresholdPct: 80,
		StrictEnforcement: true,
		CostPerMillionTokens: map[string]float64{
			"gpt-5": 20,
		},
	}
	guard := NewBudgetGuard(cfg)
	require.NotNil(t, guard)

	start := time.Date(2025, 11, 8, 0, 0, 0, 0, time.UTC)
	guard.now = func() time.Time { return start }

	require.NoError(t, guard.AllowAttempt())

	snapshot, err := guard.RecordUsage("gpt-5", 600)
	require.NoError(t, err)
	require.False(t, snapshot.AlertTriggered)

	snapshot, err = guard.RecordUsage("gpt-5", 300)
	require.NoError(t, err)
	require.True(t, snapshot.AlertTriggered)
	require.InDelta(t, 90.0, snapshot.UsagePct, 0.001)
	require.Greater(t, snapshot.UsedCostUSD, 0.0)

	snapshot, err = guard.RecordUsage("gpt-5", 200)
	require.ErrorIs(t, err, ErrBudgetExceeded)
	require.EqualValues(t, 1100, snapshot.UsedTokens)

	require.ErrorIs(t, guard.AllowAttempt(), ErrBudgetExceeded)

	// roll clock forward to next day, should reset counters
	guard.now = func() time.Time { return start.Add(24 * time.Hour) }
	require.NoError(t, guard.AllowAttempt())
	snapshot, err = guard.RecordUsage("gpt-5", 100)
	require.NoError(t, err)
	require.EqualValues(t, 100, snapshot.UsedTokens)
}

func TestBudgetGuardDisabled(t *testing.T) {
	cfg := &BudgetConfig{DailyTokenLimit: 0}
	guard := NewBudgetGuard(cfg)
	require.Nil(t, guard)
}
