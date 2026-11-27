package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Resources are read-only context the client (or user) can pull in, as opposed
// to tools, which the model calls to do something.
const (
	specURI              = "hello-api://openapi.json"
	operationURIPrefix   = "hello-api://operations/"
	operationURITemplate = operationURIPrefix + "{name}"
)

// registerResources adds:
//   - hello-api://openapi.json        the API's current OpenAPI spec
//   - hello-api://operations/{name}   a readable description of one endpoint
func (b *bridge) registerResources() {
	b.server.AddResource(&mcp.Resource{
		URI:         specURI,
		Name:        "openapi-spec",
		Title:       "Hello API OpenAPI spec",
		Description: "The current OpenAPI spec of the Hello API, as last fetched by this server. Subscribe to get notified when it changes.",
		MIMEType:    "application/json",
	}, b.readSpec)

	b.server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: operationURITemplate,
		Name:        "operation",
		Title:       "Hello API operation",
		Description: "Documentation of one API operation; {name} is the tool name, e.g. helloV2.",
		MIMEType:    "text/markdown",
	}, b.readOperation)
}

func (b *bridge) readSpec(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	b.mu.Lock()
	spec := b.spec
	b.mu.Unlock()
	if spec == nil {
		return nil, fmt.Errorf("spec not loaded yet: is the API reachable?")
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
		{URI: req.Params.URI, MIMEType: "application/json", Text: string(spec)},
	}}, nil
}

func (b *bridge) readOperation(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	name := strings.TrimPrefix(req.Params.URI, operationURIPrefix)
	b.mu.Lock()
	op, ok := b.ops[name]
	b.mu.Unlock()
	if !ok {
		return nil, mcp.ResourceNotFoundError(req.Params.URI)
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{
		{URI: req.Params.URI, MIMEType: "text/markdown", Text: operationMarkdown(op)},
	}}, nil
}

func operationMarkdown(op operation) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n\n`%s %s`\n\n%s\n\n", op.Name, op.Method, op.Path, op.Description)
	if op.Bearer {
		sb.WriteString("**Auth:** bearer token required\n\n")
	} else {
		sb.WriteString("**Auth:** none (public)\n\n")
	}
	schema, _ := json.MarshalIndent(op.InputSchema, "", "  ")
	fmt.Fprintf(&sb, "## Input schema\n\n```json\n%s\n```\n", schema)
	return sb.String()
}
