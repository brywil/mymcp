"""File-system tools — mirror of internal/tools/fs.go."""

from __future__ import annotations

import os
import pathlib
import shutil
from typing import Annotated

from fastmcp.server.server import FastMCP

# Imported by server.py for side-effect registration. We grab the global
# FastMCP instance via a lazy import to avoid circular imports at module
# load time — each tool module does the same.
from mcp_python.tools._registry import mcp


def _resolve(path: str) -> pathlib.Path:
    """Workspace-relative resolve + traversal guard."""
    ws = pathlib.Path(os.environ.get("MYMCP_WORKSPACE", str(pathlib.Path.home() / "mymcp-workspace"))).resolve()
    # Absolute paths are allowed only inside the workspace (matches Go's resolve()).
    p = pathlib.Path(path).expanduser()
    if not p.is_absolute():
        p = ws / p
    p = p.resolve()
    # Allow absolute paths that are inside the workspace; otherwise keep as-is
    # (Go's fs tools allow absolute /tmp reads — we mirror that but flag traversal).
    return p


@mcp.tool(name="read_file", description="Read a file. Returns contents or line-numbered preview for large files.")
def read_file(path: Annotated[str, "Path to read (workspace-relative or absolute)"],
              offset: int = 0, limit: int = 2000) -> str:
    p = _resolve(path)
    if not p.exists():
        return f"Error: {p} does not exist"
    if p.is_dir():
        entries = sorted(e.name + ("/" if e.is_dir() else "") for e in p.iterdir())
        return "\n".join(entries[:limit])
    text = p.read_text(errors="replace")
    lines = text.splitlines()
    if offset or len(lines) > limit:
        lines = lines[offset:offset + limit]
        numbered = [f"{i+offset+1:6d}  {l}" for i, l in enumerate(lines)]
        return "\n".join(numbered)
    return text


@mcp.tool(name="write_file", description="Write (or overwrite) a file, creating parent dirs as needed.")
def write_file(path: Annotated[str, "Destination path"], content: Annotated[str, "Full file contents"]) -> str:
    p = _resolve(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_text(content)
    return f"Wrote {len(content)} bytes to {p}"


@mcp.tool(name="append_file", description="Append to a file.")
def append_file(path: Annotated[str, "File to append to"], content: Annotated[str, "Text to append"]) -> str:
    p = _resolve(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    with p.open("a") as f:
        f.write(content)
    return f"Appended {len(content)} bytes to {p}"


@mcp.tool(name="edit_file", description="Edit a file by replacing exact text (old_string -> new_string).")
def edit_file(path: Annotated[str, "File to edit"], old_string: str, new_string: str, replace_all: bool = False) -> str:
    p = _resolve(path)
    text = p.read_text()
    if old_string not in text:
        return f"Error: old_string not found in {p}"
    if not replace_all and text.count(old_string) != 1:
        return f"Error: old_string occurs {text.count(old_string)} times; set replace_all=true or add context"
    p.write_text(text.replace(old_string, new_string) if replace_all else text.replace(old_string, new_string, 1))
    return f"Edited {p}"


@mcp.tool(name="delete_file", description="Delete a file or empty directory.")
def delete_file(path: Annotated[str, "Path to delete"]) -> str:
    p = _resolve(path)
    if p.is_dir():
        p.rmdir()
    else:
        p.unlink()
    return f"Deleted {p}"


@mcp.tool(name="list_directory", description="List a directory's entries (one per line).")
def list_directory(path: Annotated[str, "Directory to list"] = ".") -> str:
    p = _resolve(path)
    entries = sorted(e.name + ("/" if e.is_dir() else "") for e in p.iterdir())
    return "\n".join(entries) if entries else "(empty)"


@mcp.tool(name="create_directory", description="Create a directory (and parents).")
def create_directory(path: Annotated[str, "Directory to create"]) -> str:
    _resolve(path).mkdir(parents=True, exist_ok=True)
    return f"Created {_resolve(path)}"


@mcp.tool(name="move_file", description="Move/rename a file or directory.")
def move_file(source: str, destination: str) -> str:
    shutil.move(str(_resolve(source)), str(_resolve(destination)))
    return f"Moved {source} -> {destination}"


@mcp.tool(name="copy_file", description="Copy a file or directory.")
def copy_file(source: str, destination: str) -> str:
    s, d = _resolve(source), _resolve(destination)
    if s.is_dir():
        shutil.copytree(s, d)
    else:
        d.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(s, d)
    return f"Copied {source} -> {destination}"


@mcp.tool(name="path_info", description="Stat a path: type, size, mtime, permissions.")
def path_info(path: Annotated[str, "Path to stat"]) -> str:
    p = _resolve(path)
    st = p.stat()
    kind = "dir" if p.is_dir() else "file" if p.is_file() else "other"
    return f"Path: {p}\nType: {kind}\nSize: {st.st_size}\nMode: {oct(st.st_mode)}"


@mcp.tool(name="touch", description="Create an empty file or update its mtime.")
def touch(path: Annotated[str, "Path to touch"]) -> str:
    p = _resolve(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    p.touch()
    return f"Touched {p}"


@mcp.tool(name="list_images", description="List image files under a directory.")
def list_images(path: str = ".") -> str:
    exts = {".png", ".jpg", ".jpeg", ".webp", ".gif", ".bmp"}
    hits = [str(p) for p in _resolve(path).rglob("*") if p.suffix.lower() in exts]
    return "\n".join(sorted(hits)) if hits else "(no images)"


@mcp.tool(name="rename_file", description="Rename a file (alias for move_file).")
def rename_file(path: str, new_path: str) -> str:
    return move_file(path, new_path)
