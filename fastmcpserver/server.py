"""Off-the-shelf MCP server for the hello API, using FastMCP's OpenAPI support.

Compare with ../mcpserver (hand-written Go bridge). Here, FastMCP does the
spec -> tools mapping and the HTTP calls; this file only wires it up.

Usage:
    .venv/bin/python server.py                 # stdio (for Claude Code)
    .venv/bin/python server.py --http 8091     # streamable HTTP at http://localhost:8091/mcp

Environment:
    API_URL    base URL of the API (default http://localhost:8080)
    API_TOKEN  bearer token for the API (optional)
"""

import argparse
import os

import httpx2
from fastmcp import FastMCP

API_URL = os.environ.get("API_URL", "http://localhost:8080")
API_TOKEN = os.environ.get("API_TOKEN", "")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--http", type=int, metavar="PORT", help="serve over streamable HTTP instead of stdio")
    args = parser.parse_args()

    # The spec is read ONCE, at startup. New endpoints need a restart.
    spec = httpx2.get(f"{API_URL}/openapi.json", timeout=10).json()

    # FastMCP does not read the spec's "security" section: the header set here
    # is sent on EVERY request, public endpoints (v1) included.
    headers = {"Authorization": f"Bearer {API_TOKEN}"} if API_TOKEN else {}
    client = httpx2.AsyncClient(base_url=API_URL, headers=headers, timeout=30)

    mcp = FastMCP.from_openapi(
        openapi_spec=spec,
        client=client,
        name="hello-api-fastmcp",
    )

    if args.http:
        mcp.run(transport="http", host="127.0.0.1", port=args.http, show_banner=False)
    else:
        mcp.run(show_banner=False)


if __name__ == "__main__":
    main()
