// Command mcpserver is an MCP server that exposes an HTTP API as MCP tools,
// generated from the API's OpenAPI spec.
//
// It re-reads the spec periodically. When endpoints are added, changed or
// removed, it updates its tools and notifies connected clients
// (notifications/tools/list_changed), so no code changes are needed here.
//
// Usage:
//
//	mcpserver                               # stdio transport (Claude Code, Claude Desktop, ...)
//	mcpserver -http :8090                   # streamable HTTP transport at http://localhost:8090/mcp
//	mcpserver -api http://localhost:8080 -refresh 5s
//
// Environment:
//
//	API_TOKEN  bearer token sent to operations whose spec entry requires bearer auth
//
// In HTTP mode with -forward-auth (default), the bearer token from the MCP
// client's Authorization header is forwarded to secured operations instead.
// This "token passthrough" is fine for a playground but forbidden by the MCP
// spec for production (use OAuth + token exchange there).
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	apiBase := flag.String("api", "http://localhost:8080", "base URL of the API")
	specURL := flag.String("spec", "", "URL of the OpenAPI spec (default: <api>/openapi.json)")
	refresh := flag.Duration("refresh", 10*time.Second, "how often to re-read the spec (0 disables)")
	httpAddr := flag.String("http", "", "serve MCP over streamable HTTP on this address instead of stdio")
	forwardAuth := flag.Bool("forward-auth", true, "HTTP mode: forward the MCP client's bearer token to secured API operations")
	flag.Parse()

	if *specURL == "" {
		*specURL = strings.TrimRight(*apiBase, "/") + "/openapi.json"
	}

	// With stdio, stdout carries the MCP protocol: logs must go to stderr.
	log.SetOutput(os.Stderr)
	log.SetPrefix("[mcpserver] ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	server := newMCPServer()

	// The token comes from the environment, not a flag, so it doesn't show up
	// in the process list.
	b := newBridge(server, *apiBase, *specURL, os.Getenv("API_TOKEN"), &http.Client{Timeout: 30 * time.Second})
	b.forwardAuth = *httpAddr != "" && *forwardAuth // stdio has no headers to forward
	b.registerResources()
	registerPrompts(server)
	if err := b.sync(ctx); err != nil {
		log.Printf("initial sync failed (will retry): %v", err)
	}
	if *refresh > 0 {
		go func() {
			t := time.NewTicker(*refresh)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := b.sync(ctx); err != nil {
						log.Printf("sync failed: %v", err)
					}
				}
			}
		}()
	}

	if *httpAddr != "" {
		handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
		mux := http.NewServeMux()
		mux.Handle("/mcp", handler)
		srv := &http.Server{Addr: *httpAddr, Handler: mux}
		go func() {
			<-ctx.Done()
			_ = srv.Close()
		}()
		log.Printf("serving MCP on %s at path /mcp (API %s)", *httpAddr, *apiBase)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	} else {
		log.Printf("serving MCP on stdio (API %s)", *apiBase)
		if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	}

}

// newMCPServer creates the MCP server with the capabilities we need.
func newMCPServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "openapi-bridge", Version: "0.2.0"}, &mcp.ServerOptions{
		// Advertise tools (with listChanged) even if the API is down at startup
		// and we have no tools yet.
		Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}},
		// Setting these makes the server advertise resources.subscribe. The SDK
		// tracks the subscriptions itself; ResourceUpdated notifies subscribers.
		SubscribeHandler:   func(context.Context, *mcp.SubscribeRequest) error { return nil },
		UnsubscribeHandler: func(context.Context, *mcp.UnsubscribeRequest) error { return nil },
	})
}
