package executor

import (
	"fmt"
	"os"
	"strings"

	"github.com/xeipuuv/gojsonschema"
)

// JSONSchemaValidator validates executor outputs against a JSON schema.
type JSONSchemaValidator struct {
	schema *gojsonschema.Schema
}

func NewJSONSchemaValidator(path string) (*JSONSchemaValidator, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("executor: schema path cannot be empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("executor: read schema %q: %w", path, err)
	}
	loader := gojsonschema.NewBytesLoader(data)
	compiled, err := gojsonschema.NewSchema(loader)
	if err != nil {
		return nil, fmt.Errorf("executor: parse schema %q: %w", path, err)
	}
	return &JSONSchemaValidator{schema: compiled}, nil
}

func (v *JSONSchemaValidator) ValidateBytes(raw []byte) error {
	if v == nil || v.schema == nil || len(raw) == 0 {
		return nil
	}
	loader := gojsonschema.NewBytesLoader(raw)
	result, err := v.schema.Validate(loader)
	if err != nil {
		return fmt.Errorf("executor: schema validation error: %w", err)
	}
	if result.Valid() {
		return nil
	}
	if len(result.Errors()) == 0 {
		return fmt.Errorf("executor: schema validation failed")
	}
	first := result.Errors()[0]
	return fmt.Errorf("executor: schema validation failed: %s", first)
}
