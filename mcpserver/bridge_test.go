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

// specV2 adds a POST endpoint with a path parameter and a JSON body.
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
        "parameters": [{"name": "lang", "in": "path", "required": true, "schema": {"type": "string"}}],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"type": "object", "properties": {"name": {"type": "string"}}}}}
        }
      }
    }
  }
}`

// fakeAPI serves a swappable spec plus the endpoints it describes.
func fakeAPI(t *testing.T, spec *atomic.Value) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(spec.Load().(string)))
	})
	mux.HandleFunc("GET /v1/hello", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"message": "Hello, " + r.URL.Query().Get("name") + "!"})
	})
	mux.HandleFunc("POST /v2/greet/{lang}", func(w http.ResponseWriter, r *http.Request) {
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
	b := newBridge(server, api.URL, api.URL+"/openapi.json", api.Client())
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
	b := newBridge(nil, "http://api", "", http.DefaultClient)
	ops, err := parseOperations([]byte(specV2))
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.Name == "greetV2" {
			if _, err := b.buildRequest(context.Background(), op, map[string]any{}); err == nil {
				t.Error("expected error for missing path parameter")
			}
		}
	}
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
