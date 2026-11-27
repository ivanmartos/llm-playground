# LLMPlayground: MCP servers in front of an HTTP API

A small versioned API and two MCP servers that expose it as tools:

| Folder           | What                                                                                                      |
|------------------|-----------------------------------------------------------------------------------------------------------|
| `apiserver/`     | Go HTTP API: `GET /v1/hello` (public), `GET /v2/hello` (bearer token), `GET /openapi.json`                |
| `mcpserver/`     | Go MCP server (hand-written bridge): tools generated from the OpenAPI spec, reloaded every 10s, plus resources and prompts |
| `fastmcpserver/` | Python MCP server, off the shelf: FastMCP `from_openapi`, spec read once at startup                        |

The v2 token is hardcoded: `dev-v2-token`.

## 1. Run the API server

```bash
go run ./apiserver              # listens on :8080
ADDR=:9000 go run ./apiserver   # other port
```

Check it:

```bash
curl "localhost:8080/v1/hello?name=Ivan"
curl -H "Authorization: Bearer dev-v2-token" "localhost:8080/v2/hello?name=Ivan"
```

Keep it running. Both MCP servers need it.

## 2. Build the MCP servers

**Go server:**

```bash
go build -o bin/mcpserver ./mcpserver
```

Rebuild after every change in `mcpserver/`. `bin/` is git-ignored.

Options: `-api http://localhost:8080`, `-refresh 10s`, `-http :8090` (streamable HTTP instead of stdio), `-forward-auth`.
Environment: `API_TOKEN` (bearer token for secured endpoints).

**FastMCP server** (one-time setup):

```bash
python3 -m venv fastmcpserver/.venv
fastmcpserver/.venv/bin/pip install -r fastmcpserver/requirements.txt
```

Start it with `fastmcpserver/run.sh` (stdio) or `fastmcpserver/run.sh --http 8091`.
Environment: `API_URL` (default `http://localhost:8080`), `API_TOKEN`.

## 3. Add the MCP servers to Claude Code

The server name goes **before** `-e`/`-s`, and the command goes after `--`. Use absolute paths.

```bash
claude mcp add hello-api -s user -e API_TOKEN=dev-v2-token -- \
  /Users/pivan/private/LLMPlayground/bin/mcpserver

claude mcp add hello-api-fastmcp -s user -e API_TOKEN=dev-v2-token -- \
  /Users/pivan/private/LLMPlayground/fastmcpserver/run.sh

claude mcp list
```

`-s user` makes them available in every project. Leave it out to limit them to the current folder.

To add the Go server over HTTP instead, start it yourself and connect by URL:

```bash
bin/mcpserver -http :8090
claude mcp add --transport http hello-api http://localhost:8090/mcp \
  --header "Authorization: Bearer dev-v2-token"
```

In Claude Code:

- `/mcp` shows the status and lets you reconnect after a rebuild.
- `/mcp__hello-api__greet_team` and `/mcp__hello-api__compare_versions` are the prompts.
- `@hello-api:hello-api://openapi.json` attaches the spec resource.

Remove a server with `claude mcp remove hello-api`.

## 4. MCP Inspector

**Web UI** (opens http://localhost:6274):

```bash
npx @modelcontextprotocol/inspector -e API_TOKEN=dev-v2-token $PWD/bin/mcpserver
npx @modelcontextprotocol/inspector -e API_TOKEN=dev-v2-token $PWD/fastmcpserver/run.sh
```

Click **Connect**, then use the Tools, Resources and Prompts tabs. The History panel shows the raw JSON-RPC messages, and Server Notifications shows `list_changed` and `resources/updated`.
For the HTTP server, choose the **Streamable HTTP** transport with URL `http://localhost:8090/mcp`.

**Command line** (no browser):

```bash
npx @modelcontextprotocol/inspector --cli -e API_TOKEN=dev-v2-token $PWD/bin/mcpserver --method tools/list
npx @modelcontextprotocol/inspector --cli -e API_TOKEN=dev-v2-token $PWD/bin/mcpserver \
  --method tools/call --tool-name helloV2 --tool-arg name=Ivan
npx @modelcontextprotocol/inspector --cli -e API_TOKEN=dev-v2-token $PWD/bin/mcpserver \
  --method resources/read --uri hello-api://operations/helloV2
npx @modelcontextprotocol/inspector --cli -e API_TOKEN=dev-v2-token $PWD/bin/mcpserver \
  --method prompts/get --prompt-name greet_team --prompt-args "names=Alice, Bob"
```

Use absolute paths (`$PWD/...`). With relative ones, the Inspector reports the file as not found.

## Tests

```bash
go test ./...
```
