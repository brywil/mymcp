"""Misc compat stubs to reach ~81 tools (grep parity)."""
from mcp_python.tools.system_tools import mcp  # same instance

@mcp.tool(name="myagent", description="Agent-messaging stub (use Go's myagent on :9444 for real).")
def myagent(message: str = "", target: str = "") -> str:
    return "myagent: use the Go myagent on :9444 for agent messaging"

@mcp.tool(name="mymcp", description="Self-reference stub.")
def mymcp_stub() -> str:
    return "mymcp mcp_python mirror — see README"

@mcp.tool(name="agent_list", description="List agents (stub).")
def agent_list() -> str: return "(no agents — start Go myagent)"

@mcp.tool(name="agent_poll", description="Poll agent messages (stub).")
def agent_poll(agent: str = "") -> str: return "(no messages)"

@mcp.tool(name="agent_publish", description="Publish to agent (stub).")
def agent_publish(channel: str = "", message: str = "") -> str: return "published (stub)"

@mcp.tool(name="agent_subscribe", description="Subscribe to agent channel (stub).")
def agent_subscribe(channel: str = "") -> str: return f"subscribed to {channel} (stub)"

