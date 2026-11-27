package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// operation is one HTTP endpoint from the OpenAPI spec, turned into
// everything we need to expose it as an MCP tool and to call it.
type operation struct {
	Name        string         `json:"name"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	Description string         `json:"description"`
	Params      []param        `json:"params"`
	HasBody     bool           `json:"hasBody"`
	InputSchema map[string]any `json:"inputSchema"`
}

type param struct {
	Name string `json:"name"`
	In   string `json:"in"` // path, query or header
}

// bodyArg is the tool argument that carries the JSON request body.
const bodyArg = "body"

// Minimal subset of OpenAPI 3.x that we need.
type openAPIDoc struct {
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

type openAPIOperation struct {
	OperationID string         `json:"operationId"`
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	Parameters  []openAPIParam `json:"parameters"`
	RequestBody *struct {
		Description string `json:"description"`
		Required    bool   `json:"required"`
		Content     map[string]struct {
			Schema map[string]any `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
}

type openAPIParam struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Description string         `json:"description"`
	Required    bool           `json:"required"`
	Schema      map[string]any `json:"schema"`
}

var httpMethods = []string{"get", "post", "put", "patch", "delete"}

var invalidToolNameChars = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// parseOperations turns an OpenAPI document into a list of operations,
// sorted by tool name. Limitations (fine for a playground): no $ref
// resolution, only JSON request bodies, no cookie parameters.
func parseOperations(specJSON []byte) ([]operation, error) {
	var doc openAPIDoc
	if err := json.Unmarshal(specJSON, &doc); err != nil {
		return nil, fmt.Errorf("parse OpenAPI spec: %w", err)
	}

	var ops []operation
	seen := map[string]bool{}
	for path, item := range doc.Paths {
		// Parameters declared on the path apply to all its operations.
		var shared []openAPIParam
		if raw, ok := item["parameters"]; ok {
			if err := json.Unmarshal(raw, &shared); err != nil {
				return nil, fmt.Errorf("%s: parameters: %w", path, err)
			}
		}

		for _, method := range httpMethods {
			raw, ok := item[method]
			if !ok {
				continue
			}
			var o openAPIOperation
			if err := json.Unmarshal(raw, &o); err != nil {
				return nil, fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			op := buildOperation(method, path, o, shared)
			if seen[op.Name] {
				return nil, fmt.Errorf("duplicate tool name %q", op.Name)
			}
			seen[op.Name] = true
			ops = append(ops, op)
		}
	}

	sort.Slice(ops, func(i, j int) bool { return ops[i].Name < ops[j].Name })
	return ops, nil
}

func buildOperation(method, path string, o openAPIOperation, shared []openAPIParam) operation {
	name := o.OperationID
	if name == "" {
		name = method + "_" + path
	}
	name = strings.Trim(invalidToolNameChars.ReplaceAllString(name, "_"), "_")

	op := operation{
		Name:   name,
		Method: strings.ToUpper(method),
		Path:   path,
	}

	// Description shown to the LLM.
	var desc []string
	if o.Summary != "" {
		desc = append(desc, o.Summary)
	}
	if o.Description != "" && o.Description != o.Summary {
		desc = append(desc, o.Description)
	}
	desc = append(desc, fmt.Sprintf("HTTP: %s %s", op.Method, path))
	op.Description = strings.Join(desc, "\n\n")

	// Operation-level parameters override path-level ones with the same name+location.
	merged := map[string]openAPIParam{}
	var order []string
	for _, p := range append(append([]openAPIParam{}, shared...), o.Parameters...) {
		if p.In != "path" && p.In != "query" && p.In != "header" {
			continue
		}
		key := p.In + ":" + p.Name
		if _, exists := merged[key]; !exists {
			order = append(order, key)
		}
		merged[key] = p
	}

	properties := map[string]any{}
	required := []string{}
	for _, key := range order {
		p := merged[key]
		schema := copyMap(p.Schema)
		if schema == nil {
			schema = map[string]any{"type": "string"}
		}
		if p.Description != "" {
			schema["description"] = p.Description
		}
		properties[p.Name] = schema
		if p.Required || p.In == "path" {
			required = append(required, p.Name)
		}
		op.Params = append(op.Params, param{Name: p.Name, In: p.In})
	}

	if rb := o.RequestBody; rb != nil {
		if c, ok := rb.Content["application/json"]; ok {
			schema := copyMap(c.Schema)
			if schema == nil {
				schema = map[string]any{"type": "object"}
			}
			if rb.Description != "" {
				schema["description"] = rb.Description
			}
			properties[bodyArg] = schema
			if rb.Required {
				required = append(required, bodyArg)
			}
			op.HasBody = true
		}
	}

	op.InputSchema = map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		sort.Strings(required)
		op.InputSchema["required"] = required
	}
	return op
}

func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
