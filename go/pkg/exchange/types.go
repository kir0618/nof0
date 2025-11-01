package exchange

import (
	"encoding/json"
	"fmt"
)

// Core trading domain types shared across exchange implementations.
// These structures mirror the Hyperliquid API payloads while remaining exchange-agnostic
// to keep the public interface consistent if additional venues are added later.

// OrderSide represents order direction.
type OrderSide string

const (
	// OrderSideBuy executes a buy.
	OrderSideBuy OrderSide = "buy"
	// OrderSideSell executes a sell.
	OrderSideSell OrderSide = "sell"
)

// OrderType captures optional order configuration such as limit parameters.
type OrderType struct {
	Limit   *LimitOrderType   `json:"limit,omitempty"`
	Trigger *TriggerOrderType `json:"trigger,omitempty"`
}

// LimitOrderType defines limit order specific fields.
type LimitOrderType struct {
	TIF string `json:"tif"` // Valid values: "Alo", "Ioc", "Gtc"
}

// TriggerOrderType represents a trigger order (e.g. stop loss / take profit).
// To keep the common type compact across exchanges, only generic fields are
// included here. Venue-specific nuances are handled in the provider layer.
type TriggerOrderType struct {
	// If true, executes a market-style order when triggered; otherwise limit.
	IsMarket bool `json:"isMarket"`
	// Optional semantic to distinguish take-profit vs stop-loss when supported
	// by the venue (e.g. "tp" / "sl").
	Tpsl string `json:"tpsl,omitempty"`
}

// BuilderInfo describes a routing builder and optional fee configuration.
type BuilderInfo struct {
	Name   string `json:"name"`
	FeeBps int    `json:"feeBps"`
}

// Order describes a normalized order request.
type Order struct {
	Asset      int          `json:"asset"`                // Exchange-specific asset index.
	IsBuy      bool         `json:"isBuy"`                // true for buy, false for sell.
	LimitPx    string       `json:"limitPx"`              // Limit price as string to avoid precision loss.
	Sz         string       `json:"sz"`                   // Order size as string to avoid precision loss.
	ReduceOnly bool         `json:"reduceOnly"`           // Indicates whether the order only reduces position.
	OrderType  OrderType    `json:"orderType"`            // Order execution parameters.
	Cloid      string       `json:"cloid,omitempty"`      // Optional client order identifier.
	TriggerPx  string       `json:"triggerPx,omitempty"`  // Optional trigger price for advanced orders.
	TriggerRel string       `json:"triggerRel,omitempty"` // Optional trigger relation (e.g. "lte").
	Grouping   string       `json:"grouping,omitempty"`   // Optional grouping identifier for batch actions.
	Builder    *BuilderInfo `json:"builder,omitempty"`    // Optional builder routing information.
}

// Position captures live position details.
type Position struct {
	Coin           string   `json:"coin"`
	EntryPx        *string  `json:"entryPx"`
	PositionValue  string   `json:"positionValue"`
	Szi            string   `json:"szi"`            // Signed position size.
	UnrealizedPnl  string   `json:"unrealizedPnl"`  // Unrealised profit & loss.
	ReturnOnEquity string   `json:"returnOnEquity"` // ROE in percentage string.
	Leverage       Leverage `json:"leverage"`
	LiquidationPx  *string  `json:"liquidationPx,omitempty"`
}

// Leverage contains leverage settings for an instrument.
type Leverage struct {
	Type  string `json:"type"`  // "cross" or "isolated".
	Value int    `json:"value"` // Leverage multiplier.
}

// AccountState summarizes a trading account.
type AccountState struct {
	MarginSummary      MarginSummary      `json:"marginSummary"`
	CrossMarginSummary CrossMarginSummary `json:"crossMarginSummary"`
	AssetPositions     []Position         `json:"assetPositions"`
}

// MarginSummary consolidates margin metrics.
type MarginSummary struct {
	AccountValue    string `json:"accountValue"`
	TotalMarginUsed string `json:"totalMarginUsed"`
	TotalNtlPos     string `json:"totalNtlPos"`
	TotalRawUSD     string `json:"totalRawUsd"`
}

// CrossMarginSummary mirrors margin data for cross mode.
type CrossMarginSummary struct {
	AccountValue    string `json:"accountValue"`
	TotalMarginUsed string `json:"totalMarginUsed"`
	TotalNtlPos     string `json:"totalNtlPos"`
	TotalRawUSD     string `json:"totalRawUsd"`
}

// OrderStatus conveys order lifecycle information.
type OrderStatus struct {
	Order           OrderInfo `json:"order"`
	Status          string    `json:"status"`
	StatusTimestamp int64     `json:"statusTimestamp"`
}

// OrderInfo stores metadata about an individual order.
type OrderInfo struct {
	Coin      string `json:"coin"`
	Side      string `json:"side"`
	LimitPx   string `json:"limitPx"`
	Sz        string `json:"sz"`
	Oid       int64  `json:"oid"`
	Timestamp int64  `json:"timestamp"`
	OrigSz    string `json:"origSz"`
	Cloid     string `json:"cloid,omitempty"`
}

// Fill describes a match executed against an order.
type Fill struct {
	AvgPx     string `json:"avgPx"`
	TotalSz   string `json:"totalSz"`
	LimitPx   string `json:"limitPx"`
	Sz        string `json:"sz"`
	Oid       int64  `json:"oid"`
	Crossed   bool   `json:"crossed"`
	Fee       string `json:"fee"`
	Tid       int64  `json:"tid"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

// OrderResponse captures the standard exchange response after an order submission.
type OrderResponse struct {
	Status       string            `json:"status"` // "ok" or "err".
	Response     OrderResponseData `json:"response"`
	ErrorMessage string            `json:"-"` // Populated when response is a string (typically error message)
}

// UnmarshalJSON handles both object and string payloads for the response field.
// The API sometimes returns {"status":"ok","response":"Success"} for certain operations.
func (o *OrderResponse) UnmarshalJSON(data []byte) error {
	// Try the standard format first
	type alias OrderResponse
	var temp struct {
		Status   string          `json:"status"`
		Response json.RawMessage `json:"response"`
	}

	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	o.Status = temp.Status

	// Try to unmarshal response as object first
	var respData OrderResponseData
	if err := json.Unmarshal(temp.Response, &respData); err == nil {
		o.Response = respData
		return nil
	}

	// If that fails, check if it's a string (e.g., "Success" or error message)
	var respStr string
	if err := json.Unmarshal(temp.Response, &respStr); err == nil {
		// It's a string, store it in ErrorMessage field
		o.ErrorMessage = respStr
		return nil
	}

	// Neither worked, return an error
	return fmt.Errorf("response field is neither OrderResponseData nor string")
}

// OrderResponseData wraps the response payload.
type OrderResponseData struct {
	Type string                  `json:"type"` // Typically "order".
	Data OrderResponseDataDetail `json:"data"`
}

// OrderResponseDataDetail contains the per-order statuses.
type OrderResponseDataDetail struct {
	Statuses []OrderStatusResponse `json:"statuses"`
}

// OrderStatusResponse tracks the status of an individual order request.
type OrderStatusResponse struct {
	Resting *RestingOrder `json:"resting,omitempty"`
	Filled  *FilledOrder  `json:"filled,omitempty"`
	Error   string        `json:"error,omitempty"`
}

// RestingOrder represents an order that is currently resting on the book.
type RestingOrder struct {
	Oid int64 `json:"oid"`
}

// FilledOrder represents a fully matched order.
type FilledOrder struct {
	TotalSz string `json:"totalSz"`
	AvgPx   string `json:"avgPx"`
	Oid     int64  `json:"oid"`
}
