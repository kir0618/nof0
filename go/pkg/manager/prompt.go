package manager

import (
	"fmt"
	"strings"

	"nof0-api/pkg/llm"
)

// ManagerPromptInputs encapsulates the data required to render a manager prompt.
type ManagerPromptInputs struct {
	Trader      *TraderConfig
	ContextJSON string
}

// PromptRenderer renders manager prompt templates for a specific trader.
type PromptRenderer struct {
	template        *llm.PromptTemplate
	templateVersion string
}

// NewPromptRenderer parses the template at the provided path applying the optional version guard.
func NewPromptRenderer(path string, guard *llm.TemplateVersionGuard) (*PromptRenderer, error) {
	var version string
	if guard != nil {
		g := *guard
		if strings.TrimSpace(g.Component) == "" {
			g.Component = "manager.prompt"
		}
		var err error
		version, err = g.Enforce(path)
		if err != nil {
			return nil, err
		}
	}
	tpl, err := llm.NewPromptTemplate(path, nil)
	if err != nil {
		return nil, err
	}
	return &PromptRenderer{template: tpl, templateVersion: version}, nil
}

// Render executes the template using the supplied inputs.
func (r *PromptRenderer) Render(inputs ManagerPromptInputs) (string, error) {
	if r == nil || r.template == nil {
		return "", fmt.Errorf("manager prompt renderer not initialised")
	}
	if inputs.Trader == nil {
		return "", fmt.Errorf("manager prompt renderer requires trader data")
	}
	return r.template.Render(inputs)
}

// Digest exposes the template digest for version tracking.
func (r *PromptRenderer) Digest() string {
	if r == nil || r.template == nil {
		return ""
	}
	return r.template.Digest()
}

// TemplateVersion exposes the parsed version header, if available.
func (r *PromptRenderer) TemplateVersion() string {
	if r == nil {
		return ""
	}
	return r.templateVersion
}
