package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const specV1 = `{
  "openapi": "3.0.3",
  "paths": {
    "/v1/hello": {
      "get": {
        "operationId": "helloV1",
        "summary": "Greet someone",
        "parameters": [{"name": "name", "in": "query", "schema": {"type": "string"}}]
      }
    }
  }
}`

// specV2 adds a bearer-protected POST endpoint with a path parameter and a JSON body.
const specV2 = `{
  "openapi": "3.0.3",
  "paths": {
    "/v1/hello": {
      "get": {
        "operationId": "helloV1",
        "summary": "Greet someone",
        "parameters": [{"name": "name", "in": "query", "schema": {"type": "string"}}]
      }
    },
    "/v2/greet/{lang}": {
      "post": {
        "operationId": "greetV2",
        "summary": "Greet in a language",
        "security": [{"bearerAuth": []}],
        "parameters": [{"name": "lang", "in": "path", "required": true, "schema": {"type": "string"}}],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"type": "object", "properties": {"name": {"type": "string"}}}}}
        }
      }
    }
  },
  "components": {"securitySchemes": {"bearerAuth": {"type": "http", "scheme": "bearer"}}}
}`

const testToken = "test-token"

// fakeAPI serves a swappable spec plus the endpoints it describes.
func fakeAPI(t *testing.T, spec *atomic.Value) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(spec.Load().(string)))
	})
	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("token was sent to a public endpoint")
		}
		json.NewEncoder(w).Encode(map[string]string{"message": "Hello, " + r.URL.Query().Get("name") + "!"})
	})
	mux.HandleFunc("POST /v2/greet/{lang}", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		var in struct{ Name string }
		json.NewDecoder(r.Body).Decode(&in)
		json.NewEncoder(w).Encode(map[string]string{"lang": r.PathValue("lang"), "greeting": "Hallo, " + in.Name + "!"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestBridgeEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var spec atomic.Value
	spec.Store(specV1)
	api := fakeAPI(t, &spec)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, &mcp.ServerOptions{
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
	})
	b := newBridge(server, api.URL, api.URL+"/openapi.json", testToken, api.Client())
	if err := b.sync(ctx); err != nil {
		t.Fatal(err)
	}

	// Connect an MCP client over in-memory transports.
	changed := make(chan struct{}, 10)
	client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	assertTools(t, ctx, cs, "helloV1")
	if got := callText(t, ctx, cs, "helloV1", map[string]any{"name": "Ivan"}); got != `{"message":"Hello, Ivan!"}` {
		t.Errorf("helloV1 = %s", got)
	}

	// The API gains a new endpoint: the client is notified and sees the new tool.
	spec.Store(specV2)
	if err := b.sync(ctx); err != nil {
		t.Fatal(err)
	}
	waitForNotification(t, changed)
	assertTools(t, ctx, cs, "greetV2", "helloV1")
	got := callText(t, ctx, cs, "greetV2", map[string]any{"lang": "de", "body": map[string]any{"name": "Ivan"}})
	if got != `{"greeting":"Hallo, Ivan!","lang":"de"}` {
		t.Errorf("greetV2 = %s", got)
	}

	// The endpoint is removed again: the tool disappears.
	spec.Store(specV1)
	if err := b.sync(ctx); err != nil {
		t.Fatal(err)
	}
	waitForNotification(t, changed)
	assertTools(t, ctx, cs, "helloV1")
}

func TestMissingPathParamIsToolError(t *testing.T) {
	b := newBridge(nil, "http://api", "", "", http.DefaultClient)
	ops, err := parseOperations([]byte(specV2))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Name == "greetV2" {
			if _, err := b.buildRequest(context.Background(), op, map[string]any{}, ""); err == nil {
				t.Error("expected error for missing path parameter")
			}
		}
	}
}

func TestBearerOnlyForSecuredOperations(t *testing.T) {
	b := newBridge(nil, "http://api", "", testToken, http.DefaultClient)
	ops, err := parseOperations([]byte(specV2))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		req, err := b.buildRequest(context.Background(), op, map[string]any{"lang": "de"}, testToken)
		if err != nil {
			t.Fatal(err)
		}
		got := req.Header.Get("Authorization")
		want := ""
		if op.Name == "greetV2" {
			want = "Bearer " + testToken
		}
		if got != want {
			t.Errorf("%s: Authorization = %q, want %q", op.Name, got, want)
		}
	}
}

func TestSecurityInheritanceAndOverride(t *testing.T) {
	// Document-level security applies to all operations; "security": [] opts out.
	spec := `{
	  "security": [{"bearerAuth": []}],
	  "components": {"securitySchemes": {"bearerAuth": {"type": "http", "scheme": "bearer"}}},
	  "paths": {
	    "/a": {"get": {"operationId": "inherits"}},
	    "/b": {"get": {"operationId": "public", "security": []}}
	  }
	}`
	ops, err := parseOperations([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, op := range ops {
		got[op.Name] = op.Bearer
	}
	if !got["inherits"] || got["public"] {
		t.Errorf("bearer flags = %v, want inherits=true public=false", got)
	}
}

// TestHTTPForwardsClientToken runs the MCP server over streamable HTTP and
// checks that the MCP client's bearer token reaches the secured API operation
// (and only that one).
func TestHTTPForwardsClientToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var spec atomic.Value
	spec.Store(specV2)
	api := fakeAPI(t, &spec)

	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	b := newBridge(server, api.URL, api.URL+"/openapi.json", "", api.Client()) // no API_TOKEN
	b.forwardAuth = true
	if err := b.sync(ctx); err != nil {
		t.Fatal(err)
	}
	mcpHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	// Registered first, so it runs last: client sessions (and their open
	// streams) must be closed before the server, or Close blocks.
	t.Cleanup(mcpHTTP.Close)

	connect := func(token string) *mcp.ClientSession {
		httpClient := &http.Client{Transport: headerTransport{"Authorization": "Bearer " + token}}
		if token == "" {
			httpClient = http.DefaultClient
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, nil)
		cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: mcpHTTP.URL, HTTPClient: httpClient}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cs.Close() })
		return cs
	}
	args := map[string]any{"lang": "de", "body": map[string]any{"name": "Ivan"}}

	// With the right token: works. fakeAPI also fails the test if helloV1 gets a token.
	cs := connect(testToken)
	if got := callText(t, ctx, cs, "greetV2", args); got != `{"greeting":"Hallo, Ivan!","lang":"de"}` {
		t.Errorf("greetV2 = %s", got)
	}
	callText(t, ctx, cs, "helloV1", map[string]any{"name": "Ivan"})

	// Without a token: the API rejects the call and the LLM gets a tool error.
	res, err := connect("").CallTool(ctx, &mcp.CallToolParams{Name: "greetV2", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected tool error without token")
	}
}

type headerTransport map[string]string

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func assertTools(t *testing.T, ctx context.Context, cs *mcp.ClientSession, want ...string) {
	t.Helper()
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("tools = %v, want %v", got, want)
		}
	}
}

func callText(t *testing.T, ctx context.Context, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("%s returned error: %v", name, res.Content)
	}
	var out json.RawMessage
	text := res.Content[0].(*mcp.TextContent).Text
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s: response is not JSON: %s", name, text)
	}
	compact, _ := json.Marshal(out)
	return string(compact)
}

func waitForNotification(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no tools/list_changed notification received")
	}
}
