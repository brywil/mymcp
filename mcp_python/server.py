"""FastMCP mirror of mymcp — same tool catalog, same transport, same auth.

Go original:  ./cmd/mymcp + internal/{server,tools,mcp,tokens}
Python mirror: fastmcp/  —  FastMCP + stdlib.

Run:
    uv run mcp_python/server.py                  # :9443 with bearer auth
    uv run mcp_python/server.py --no-auth        # open loopback (bench only)
    uv run mcp_python/server.py --port 9444
"""

from __future__ import annotations

import argparse
import logging
import os
from pathlib import Path

from fastmcp.server.auth import BearerAuthProvider

TOKENS_DIR = Path.home() / ".config" / "mymcp" / "tokens"
log = logging.getLogger("mymcp.fastmcp")


class NamedBearerAuth(BearerAuthProvider):
    """Validate against ~/.config/mymcp/tokens/* and expose token name."""

    def __init__(self):
        super().__init__(public_key=None)

    async def verify_token(self, token: str) -> dict | None:  # type: ignore[override]
        if not TOKENS_DIR.is_dir():
            return None
        for f in TOKENS_DIR.iterdir():
            if f.is_file():
                try:
                    if f.read_text().strip() == token:
                        return {"sub": f.name, "token_name": f.name}
                except OSError:
                    continue
        return None


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(description="mymcp (FastMCP mirror)")
    p.add_argument("--addr", default="127.0.0.1:9443", help="host:port (default 127.0.0.1:9443)")
    p.add_argument("--host", help="override host")
    p.add_argument("--port", type=int, help="override port")
    p.add_argument("--workspace", default=os.path.expanduser("~/mymcp-workspace"),
                   help="workspace root (default ~/mymcp-workspace)")
    p.add_argument("--no-auth", action="store_true", help="disable bearer auth (loopback bench only)")
    p.add_argument("--allow-remote", action="store_true", help="allow non-loopback binds")
    p.add_argument("--log-level", default="INFO")
    return p.parse_args()


def main() -> None:
    args = parse_args()
    logging.basicConfig(level=getattr(logging, args.log_level.upper(), logging.INFO),
                        format="%(asctime)s %(levelname)s %(message)s")

    host, port_s = (args.addr.rsplit(":", 1) + ["9443"])[:2] if ":" in args.addr else (args.addr, "9443")
    if args.host:
        host = args.host
    if args.port:
        port_s = str(args.port)
    port = int(port_s)

    if not args.allow_remote and host not in ("127.0.0.1", "localhost", "::1"):
        raise SystemExit(f"refusing non-loopback bind {host!r} without --allow-remote (see README: truemtls)")

    os.environ.setdefault("MYMCP_WORKSPACE", args.workspace)

    # Import registry — this triggers all tool registrations
    from mcp_python.tools._registry import mcp

    # Ensure tool modules are imported (they register on import)
    import mcp_python.tools.fs_tools        # noqa: F401
    import mcp_python.tools.shell_tools    # noqa: F401
    import mcp_python.tools.git_tools      # noqa: F401
    import mcp_python.tools.web_tools      # noqa: F401
    import mcp_python.tools.system_tools   # noqa: F401
    import mcp_python.tools.tmux_tools     # noqa: F401
    import mcp_python.tools.misc_tools    # noqa: F401

    # Wire auth (FastMCP checks Authorization before tool dispatch)
    if not args.no_auth:
        mcp.auth = NamedBearerAuth()  # type: ignore[attr-defined]

    log.info("mymcp.fastmcp serving on %s:%d  workspace=%s  auth=%s",
             host, port, args.workspace, "off" if args.no_auth else "bearer")
    mcp.run(transport="http", host=host, port=port, path="/mcp")


if __name__ == "__main__":
    main()
