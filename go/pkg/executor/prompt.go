package executor

import (
	"fmt"

	"nof0-api/pkg/llm"
)

// PromptInputs contains dynamic data injected into the executor prompt template.
type PromptInputs struct {
	CurrentTime     string
	RuntimeMinutes  int
	SharpeRatio     float64
	AccountOverview string
	OpenPositions   string
	RiskBudget      string
	PerformanceView string
	CandidateCoins  string
	MarketSnapshots string
}

// PromptRenderer renders the executor system prompt from a template file.
type PromptRenderer struct {
	cfg             *Config
	tpl             *llm.PromptTemplate
	templateVersion string
}

// NewPromptRenderer constructs a renderer using the supplied template path.
func NewPromptRenderer(cfg *Config, templatePath string) (*PromptRenderer, error) {
	if cfg == nil {
		return nil, fmt.Errorf("executor prompt renderer requires config")
	}
	guard := llm.TemplateVersionGuard{
		Component:            "executor.prompt",
		ExpectedVersion:      cfg.PromptSchemaVersion,
		RequireVersionHeader: cfg.PromptValidation.RequireVersionHeader,
		StrictMode:           cfg.PromptValidation.StrictMode,
	}
	version, err := guard.Enforce(templatePath)
	if err != nil {
		return nil, err
	}
	tpl, err := llm.NewPromptTemplate(templatePath, nil)
	if err != nil {
		return nil, err
	}
	return &PromptRenderer{
		cfg:             cfg,
		tpl:             tpl,
		templateVersion: version,
	}, nil
}

// Render generates the final prompt string populated with inputs.
func (r *PromptRenderer) Render(inputs PromptInputs) (string, error) {
	if r == nil || r.tpl == nil {
		return "", fmt.Errorf("executor prompt renderer not initialised")
	}

	payload := struct {
		Config *Config
		PromptInputs
	}{
		Config:       r.cfg,
		PromptInputs: inputs,
	}

	return r.tpl.Render(payload)
}

// Digest returns the underlying template digest for observability.
func (r *PromptRenderer) Digest() string {
	if r == nil || r.tpl == nil {
		return ""
	}
	return r.tpl.Digest()
}

// TemplateVersion returns the parsed Version header from the template, if present.
func (r *PromptRenderer) TemplateVersion() string {
	if r == nil {
		return ""
	}
	return r.templateVersion
}
