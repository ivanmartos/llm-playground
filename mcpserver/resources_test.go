package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectFull starts a bridge (with resources and prompts) against a fake API
// serving spec, and returns a connected client session.
func connectFull(t *testing.T, ctx context.Context, spec *atomic.Value, clientOpts *mcp.ClientOptions) (*bridge, *mcp.ClientSession) {
	t.Helper()
	api := fakeAPI(t, spec)
	server := newMCPServer()
	b := newBridge(server, api.URL, api.URL+"/openapi.json", testToken, api.Client())
	b.registerResources()
	registerPrompts(server)
	if err := b.sync(ctx); err != nil {
		t.Fatal(err)
	}

	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "v0"}, clientOpts).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return b, cs
}

func TestResources(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var spec atomic.Value
	spec.Store(specV1)
	updated := make(chan string, 10)
	b, cs := connectFull(t, ctx, &spec, &mcp.ClientOptions{
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			updated <- req.Params.URI
		},
	})

	caps := cs.InitializeResult().Capabilities
	if caps.Resources == nil || !caps.Resources.Subscribe || caps.Prompts == nil || caps.Tools == nil {
		t.Fatalf("capabilities = %+v", caps)
	}

	// Static resource: the spec.
	list, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Resources) != 1 || list.Resources[0].URI != specURI {
		t.Fatalf("resources = %+v", list.Resources)
	}
	res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: specURI})
	if err != nil {
		t.Fatal(err)
	}
	if res.Contents[0].Text != specV1 {
		t.Errorf("spec resource = %s", res.Contents[0].Text)
	}

	// Template: one resource per operation.
	tmpl, err := cs.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tmpl.ResourceTemplates) != 1 || tmpl.ResourceTemplates[0].URITemplate != operationURITemplate {
		t.Fatalf("templates = %+v", tmpl.ResourceTemplates)
	}
	res, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: operationURIPrefix + "helloV1"})
	if err != nil {
		t.Fatal(err)
	}
	if text := res.Contents[0].Text; !strings.Contains(text, "`GET /v1/hello`") || !strings.Contains(text, "none (public)") {
		t.Errorf("operation resource = %s", text)
	}
	if _, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: operationURIPrefix + "nope"}); err == nil {
		t.Error("expected error for unknown operation")
	}

	// Subscribe to the spec, change it: the client is notified and reads the new one.
	if err := cs.Subscribe(ctx, &mcp.SubscribeParams{URI: specURI}); err != nil {
		t.Fatal(err)
	}
	spec.Store(specV2)
	if err := b.sync(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case uri := <-updated:
		if uri != specURI {
			t.Errorf("updated URI = %s", uri)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no resources/updated notification")
	}
	res, err = cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: operationURIPrefix + "greetV2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Contents[0].Text, "bearer token required") {
		t.Errorf("greetV2 resource = %s", res.Contents[0].Text)
	}

	// An unchanged spec must not notify again.
	if err := b.sync(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case uri := <-updated:
		t.Errorf("unexpected notification for %s", uri)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPrompts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var spec atomic.Value
	spec.Store(specV1)
	_, cs := connectFull(t, ctx, &spec, nil)

	list, err := cs.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list.Prompts {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "compare_versions,greet_team" {
		t.Errorf("prompts = %v", names)
	}

	get := func(name string, args map[string]string) (string, error) {
		res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: args})
		if err != nil {
			return "", err
		}
		return res.Messages[0].Content.(*mcp.TextContent).Text, nil
	}

	text, err := get("greet_team", map[string]string{"names": " Alice, Bob ,, "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "`helloV2`") || !strings.Contains(text, "Alice, Bob.") {
		t.Errorf("greet_team = %s", text)
	}
	text, err = get("greet_team", map[string]string{"names": "Alice", "version": "V1"})
	if err != nil || !strings.Contains(text, "`helloV1`") {
		t.Errorf("greet_team v1 = %q, %v", text, err)
	}
	if _, err := get("greet_team", map[string]string{"names": "Alice", "version": "v3"}); err == nil {
		t.Error("expected error for version v3")
	}
	if _, err := get("compare_versions", nil); err == nil {
		t.Error("expected error for missing name")
	}
	text, err = get("compare_versions", map[string]string{"name": "Ivan"})
	if err != nil || !strings.Contains(text, `"Ivan"`) {
		t.Errorf("compare_versions = %q, %v", text, err)
	}
}
