package kit

import (
	"sort"
	"strconv"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/usherf233/adks/manifest"
)

// schemaFromParams builds the tool's input schema from the manifest parameters.
//
// PropertyOrder is set explicitly and sorted. Schema.Properties is a map, and
// without an order the rendered JSON key order would change when the manifest
// reorders its parameters; the declaration is part of the prompt prefix, so an
// unstable order would silently drop provider cache hits.
func schemaFromParams(params []manifest.ParamSpec) *jsonschema.Schema {
	if len(params) == 0 {
		return &jsonschema.Schema{Type: "object"}
	}
	props := make(map[string]*jsonschema.Schema, len(params))
	required := make([]string, 0, len(params))
	order := make([]string, 0, len(params))

	for _, p := range params {
		s := &jsonschema.Schema{Description: p.Description}
		switch p.Type {
		case "integer":
			s.Type = "integer"
		case "number":
			s.Type = "number"
		case "boolean":
			s.Type = "boolean"
		case "array_of_string":
			s.Type = "array"
			s.Items = &jsonschema.Schema{Type: "string"}
		default:
			s.Type = "string"
		}
		if len(p.Enum) > 0 {
			enum := make([]any, len(p.Enum))
			for i, e := range p.Enum {
				// Enum values are declared as strings, but the schema type may be
				// integer/number/boolean. Leaving them as strings makes the schema
				// permanently unsatisfiable: 1 fails the enum, "1" fails the type.
				if v, ok := coerceEnum(p.Type, e); ok {
					enum[i] = v
				} else {
					enum[i] = e
				}
			}
			s.Enum = enum
		}
		props[p.Name] = s
		order = append(order, p.Name)
		if p.Required {
			required = append(required, p.Name)
		}
	}

	// Both lists are sorted so a reordered manifest produces identical prompt
	// bytes; PropertyOrder only needs to be explicit and deterministic.
	sort.Strings(order)
	sort.Strings(required)
	return &jsonschema.Schema{
		Type:          "object",
		Properties:    props,
		Required:      required,
		PropertyOrder: order,
	}
}

func coerceEnum(typ, raw string) (any, bool) {
	switch typ {
	case "integer":
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return n, true
		}
	case "number":
		if f, err := strconv.ParseFloat(raw, 64); err == nil {
			return f, true
		}
	case "boolean":
		if b, err := strconv.ParseBool(raw); err == nil {
			return b, true
		}
	default:
		return raw, true
	}
	return nil, false
}

// SchemaForParams is exported for the builder's preview panel, which renders the
// exact schema the model will see.
func SchemaForParams(params []manifest.ParamSpec) *jsonschema.Schema { return schemaFromParams(params) }
