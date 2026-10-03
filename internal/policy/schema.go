package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// ValidateYAML checks a YAML document against a JSON schema and returns every violation, so a
// hand-edited file gets all its problems at once.
func ValidateYAML(schemaJSON []byte, schemaURL string, doc []byte) error {
	sch, err := compile(schemaJSON, schemaURL)
	if err != nil {
		return err
	}
	var v any
	if err := yaml.Unmarshal(doc, &v); err != nil {
		return fmt.Errorf("yaml: %w", err)
	}
	// Round-trip through JSON so numbers and maps are the shapes the validator expects.
	j, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("yaml is not representable as JSON: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(j))
	if err != nil {
		return err
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(err.Error()))
	}
	return nil
}

func compile(schemaJSON []byte, url string) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", url, err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}
