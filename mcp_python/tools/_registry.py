"""Shared registry shim — exposes the single FastMCP instance to tool modules."""
from fastmcp import FastMCP
# Created here so tool modules can `from mcp_python.tools._registry import mcp`
# without importing server.py (which would trigger argparse).
mcp = FastMCP(name="mymcp", instructions="Host-tool mirror (fastmcp). See server.py for transport/auth.")
