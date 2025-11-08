package executor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONSchemaValidator(t *testing.T) {
	dir := t.TempDir()
	schema := `{
      "$schema": "http://json-schema.org/draft-07/schema#",
      "type": "object",
      "required": ["foo"],
      "properties": {
        "foo": {"type": "string"}
      }
    }`
	schemaPath := filepath.Join(dir, "schema.json")
	require.NoError(t, os.WriteFile(schemaPath, []byte(schema), 0o600))

	validator, err := NewJSONSchemaValidator(schemaPath)
	require.NoError(t, err)
	require.NotNil(t, validator)

	require.NoError(t, validator.ValidateBytes([]byte(`{"foo":"bar"}`)))
	require.Error(t, validator.ValidateBytes([]byte(`{"bar":123}`)))
}
