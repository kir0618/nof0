package manager

import (
	"testing"

	"github.com/stretchr/testify/require"

	"nof0-api/pkg/exchange"
	executorpkg "nof0-api/pkg/executor"
)

func TestManagerAssignReleaseVirtualPosition(t *testing.T) {
	m := &Manager{
		traders:        make(map[string]*VirtualTrader),
		positionOwners: make(map[string]string),
	}
	trader := &VirtualTrader{ID: "t1", VirtualPositions: make(map[string]VirtualPosition)}
	m.traders[trader.ID] = trader

	err := m.assignVirtualPosition(trader, VirtualPosition{Symbol: "BTC", Side: "long", Quantity: 1, EntryPrice: 100})
	require.NoError(t, err)
	require.Equal(t, "t1", m.getPositionOwner("BTC"))

	m.releaseVirtualPosition(trader.ID, "BTC")
	require.Equal(t, "", m.getPositionOwner("BTC"))
	trader.mu.RLock()
	_, exists := trader.VirtualPositions[normalizeSymbol("BTC")]
	trader.mu.RUnlock()
	require.False(t, exists)
}

func TestManagerEnforceSecondaryRisk(t *testing.T) {
	m := &Manager{}
	trader := &VirtualTrader{
		ID: "t-risk",
		RiskParams: RiskParameters{
			MaxPositionSizeUSD: 500,
			MaxMarginUsagePct:  30,
		},
		VirtualPositions: make(map[string]VirtualPosition),
	}
	trader.ResourceAlloc = ResourceAllocation{
		CurrentEquityUSD: 1000,
		MarginUsedUSD:    200,
	}

	decision := &executorpkg.Decision{Symbol: "BTC", PositionSizeUSD: 600}
	err := m.enforceSecondaryRisk(trader, decision, 3)
	require.Error(t, err, "should block oversize position")

	decision.PositionSizeUSD = 300
	err = m.enforceSecondaryRisk(trader, decision, 2) // adds 150 margin => 35%
	require.Error(t, err, "should block excessive margin usage")

	decision.PositionSizeUSD = 200
	err = m.enforceSecondaryRisk(trader, decision, 4)
	require.NoError(t, err, "should allow within caps")
}

func TestFilterPositionsForTrader(t *testing.T) {
	m := &Manager{
		positionOwners: map[string]string{"BTC": "t1"},
	}
	positions := []exchange.Position{
		{Coin: "BTC"},
		{Coin: "ETH"},
	}

	mine := m.filterPositionsForTrader("t1", positions)
	require.Len(t, mine, 2, "owner should see assigned and unowned positions")

	other := m.filterPositionsForTrader("t2", positions)
	require.Len(t, other, 1, "other trader should not see BTC")
	require.Equal(t, "ETH", other[0].Coin)
}
