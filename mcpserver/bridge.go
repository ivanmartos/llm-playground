package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxResponseBytes = 1 << 20

// bridge keeps the MCP server's tool list in sync with the API's OpenAPI spec
// and forwards tool calls to the API.
type bridge struct {
	server  *mcp.Server
	apiBase string // e.g. http://localhost:8080
	specURL string // e.g. http://localhost:8080/openapi.json
	client  *http.Client
	token   string // fallback bearer token for operations that require one

	// forwardAuth: in HTTP mode, use the bearer token from the incoming MCP
	// request's Authorization header (falls back to token if absent).
	// Note: the MCP spec forbids this "token passthrough" in production.
	forwardAuth bool

	mu    sync.Mutex
	tools map[string]string    // tool name -> fingerprint of its operation
	ops   map[string]operation // tool name -> operation, for the operation resources
	spec  []byte               // last fetched spec, served as a resource
}

func newBridge(server *mcp.Server, apiBase, specURL, token string, client *http.Client) *bridge {
	return &bridge{
		server:  server,
		apiBase: strings.TrimRight(apiBase, "/"),
		specURL: specURL,
		client:  client,
		token:   token,
		tools:   map[string]string{},
	}
}

// sync fetches the spec and adds, replaces or removes tools so they match it.
// Every change makes the SDK send notifications/tools/list_changed to clients.
// If the spec can't be fetched, the current tools are kept.
func (b *bridge) sync(ctx context.Context) error {
	spec, err := b.fetchSpec(ctx)
	if err != nil {
		return err
	}
	ops, err := parseOperations(spec)
	if err != nil {
		return err
	}

	specChanged, err := b.apply(spec, ops)
	if err != nil {
		return err
	}
	if specChanged {
		// Tell clients that subscribed to the spec resource (resources/subscribe).
		if err := b.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: specURI}); err != nil {
			log.Printf("notify resource updated: %v", err)
		}
	}
	return nil
}

// apply updates tools and stored spec to match ops. It reports whether the
// spec changed compared to an earlier sync.
func (b *bridge) apply(spec []byte, ops []operation) (specChanged bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	specChanged = b.spec != nil && !bytes.Equal(b.spec, spec)
	b.spec = spec
	b.ops = make(map[string]operation, len(ops))

	wanted := map[string]bool{}
	for _, op := range ops {
		wanted[op.Name] = true
		b.ops[op.Name] = op
		fp, err := json.Marshal(op)
		if err != nil {
			return false, err
		}
		if b.tools[op.Name] == string(fp) {
			continue // unchanged
		}
		_, existed := b.tools[op.Name]
		b.server.AddTool(&mcp.Tool{
			Name:        op.Name,
			Description: op.Description,
			InputSchema: op.InputSchema,
		}, b.handler(op))
		b.tools[op.Name] = string(fp)
		if existed {
			log.Printf("tool updated: %s (%s %s)%s", op.Name, op.Method, op.Path, b.authNote(op))
		} else {
			log.Printf("tool added:   %s (%s %s)%s", op.Name, op.Method, op.Path, b.authNote(op))
		}
	}

	var removed []string
	for name := range b.tools {
		if !wanted[name] {
			removed = append(removed, name)
			delete(b.tools, name)
			log.Printf("tool removed: %s", name)
		}
	}
	if len(removed) > 0 {
		b.server.RemoveTools(removed...)
	}
	return specChanged, nil
}

func (b *bridge) fetchSpec(ctx context.Context) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.specURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch spec: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch spec: %s returned %s", b.specURL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
}

// handler returns the MCP tool handler that calls the API for op.
func (b *bridge) handler(op operation) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			dec := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
			dec.UseNumber() // keep numbers as written (no 1e+06)
			if err := dec.Decode(&args); err != nil {
				return errorResult("invalid arguments: %v", err), nil
			}
		}

		httpReq, err := b.buildRequest(ctx, op, args, b.tokenFor(req))
		if err != nil {
			return errorResult("%v", err), nil
		}
		resp, err := b.client.Do(httpReq)
		if err != nil {
			return errorResult("calling API: %v", err), nil
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if err != nil {
			return errorResult("reading API response: %v", err), nil
		}

		// The response is passed through as-is, so changes to the API's output
		// schema reach the LLM without touching this server.
		if resp.StatusCode >= 400 {
			return errorResult("API returned %s: %s", resp.Status, body), nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
		}, nil
	}
}

// tokenFor picks the bearer token for one tool call: the one the MCP client
// sent (HTTP transport with forwarding on), else the configured API_TOKEN.
func (b *bridge) tokenFor(req *mcp.CallToolRequest) string {
	if b.forwardAuth && req.Extra != nil && req.Extra.Header != nil {
		if tok, ok := strings.CutPrefix(req.Extra.Header.Get("Authorization"), "Bearer "); ok && tok != "" {
			return tok
		}
	}
	return b.token
}

func (b *bridge) buildRequest(ctx context.Context, op operation, args map[string]any, token string) (*http.Request, error) {
	path := op.Path
	query := url.Values{}
	headers := http.Header{}

	for _, p := range op.Params {
		v, ok := args[p.Name]
		if !ok || v == nil {
			if p.In == "path" {
				return nil, fmt.Errorf("missing required path parameter %q", p.Name)
			}
			continue
		}
		switch p.In {
		case "path":
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(fmt.Sprint(v)))
		case "query":
			if list, ok := v.([]any); ok {
				for _, item := range list {
					query.Add(p.Name, fmt.Sprint(item))
				}
			} else {
				query.Set(p.Name, fmt.Sprint(v))
			}
		case "header":
			headers.Set(p.Name, fmt.Sprint(v))
		}
	}

	u := b.apiBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var body io.Reader
	if op.HasBody {
		if v, ok := args[bodyArg]; ok {
			data, err := json.Marshal(v)
			if err != nil {
				return nil, fmt.Errorf("encoding body: %w", err)
			}
			body = bytes.NewReader(data)
			headers.Set("Content-Type", "application/json")
		}
	}

	req, err := http.NewRequestWithContext(ctx, op.Method, u, body)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		req.Header[k] = vs
	}
	req.Header.Set("Accept", "application/json")
	// Only send the token where the spec asks for it, so it never leaks to
	// public endpoints.
	if op.Bearer && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func (b *bridge) authNote(op operation) string {
	switch {
	case !op.Bearer:
		return ""
	case b.forwardAuth && b.token == "":
		return " [bearer auth: forwarded from MCP client]"
	case b.forwardAuth:
		return " [bearer auth: forwarded from MCP client, else API_TOKEN]"
	case b.token == "":
		return " [needs bearer token, but API_TOKEN is not set: calls will fail]"
	default:
		return " [bearer auth]"
	}
}

func errorResult(format string, a ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, a...)}},
	}
}
