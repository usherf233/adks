package manifest

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// Decode parses YAML strictly (unknown fields are an error, not a silent typo),
// applies defaults and returns the project without validating it.
func Decode(raw []byte) (*Project, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var p Project
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	// A second YAML document used to be silently dropped, so appending "---"
	// could hide configuration from validation while the editor showed only the
	// first document.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("parse manifest: trailing document: %w", err)
		}
		return nil, fmt.Errorf("parse manifest: multiple YAML documents are not allowed")
	}
	ApplyDefaults(&p)
	return &p, nil
}

// Encode renders the project as YAML with two-space indentation.
func Encode(p *Project) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(p); err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode manifest: %w", err)
	}
	return buf.Bytes(), nil
}

// Parse decodes, applies defaults and validates in one step. A returned project
// is non-nil even when the error list contains errors, so a UI can edit it.
func Parse(raw []byte) (*Project, []Issue, error) {
	p, err := Decode(raw)
	if err != nil {
		return nil, nil, err
	}
	return p, Validate(p), nil
}
