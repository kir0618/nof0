package manager

import (
	"encoding/json"
	"fmt"
	"strings"

	"nof0-api/internal/model"
	"nof0-api/pkg/repo"
)

// TraderConfigsToRecords converts trader configs into repository payloads.
func TraderConfigsToRecords(cfgs []TraderConfig, changedBy, reason string) ([]repo.TraderConfigRecord, error) {
	records := make([]repo.TraderConfigRecord, 0, len(cfgs))
	for i := range cfgs {
		rec, err := TraderConfigToRecord(cfgs[i], changedBy, reason)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
}

// TraderConfigToRecord serializes a TraderConfig into the format expected by the
// repository layer. Durations and guard structs are preserved as-is so the
// payload remains replayable.
func TraderConfigToRecord(cfg TraderConfig, changedBy, reason string) (repo.TraderConfigRecord, error) {
	if strings.TrimSpace(cfg.ID) == "" {
		return repo.TraderConfigRecord{}, fmt.Errorf("trader config missing id")
	}
	detail, err := json.Marshal(cfg)
	if err != nil {
		return repo.TraderConfigRecord{}, fmt.Errorf("marshal trader config %s: %w", cfg.ID, err)
	}
	return repo.TraderConfigRecord{
		ID:               cfg.ID,
		ExchangeProvider: cfg.ExchangeProvider,
		MarketProvider:   cfg.MarketProvider,
		AllocationPct:    cfg.AllocationPct,
		Detail:           detail,
		CreatedBy:        changedBy,
		ChangeReason:     reason,
	}, nil
}

// TraderConfigFromModel hydrates a TraderConfig from the persisted detail JSON
// and authoritative column values.
func TraderConfigFromModel(row *model.TraderConfig) (*TraderConfig, error) {
	if row == nil {
		return nil, fmt.Errorf("nil trader config row")
	}
	var cfg TraderConfig
	if err := json.Unmarshal([]byte(row.Detail), &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal trader config %s detail: %w", row.Id, err)
	}
	cfg.ID = row.Id
	cfg.ExchangeProvider = row.ExchangeProvider
	cfg.MarketProvider = row.MarketProvider
	cfg.AllocationPct = row.AllocationPct
	cfg.Version = row.Version
	return &cfg, nil
}
