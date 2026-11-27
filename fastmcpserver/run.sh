#!/bin/sh
# Launcher for the FastMCP server: works from any directory, passes all arguments on.
#   fastmcpserver/run.sh              # stdio
#   fastmcpserver/run.sh --http 8091  # streamable HTTP
DIR="$(cd "$(dirname "$0")" && pwd)"
exec "$DIR/.venv/bin/python" "$DIR/server.py" "$@"
