"""Map a PDF onto whatever upload tool the Drive MCP server exposes.

The Drive MCP is in developer preview and its tool schema may change, so the
publisher reads the tool's input schema at runtime and fills the fields by
name. If a required field cannot be filled, it fails with the schema in the
message instead of guessing.
"""

from __future__ import annotations

import base64
import json
import re
from dataclasses import dataclass
from typing import Any

from mcp.types import Tool

NAME_KEYS = ("name", "title", "filename", "fileName", "file_name")
# Current names first: Drive's create_file deprecates content and mimeType ("DO NOT
# USE") and requires contentMimeType whenever content is sent.
CONTENT_KEYS = ("base64Content", "contentBase64", "content", "base64", "data", "fileContent", "file_content", "body")
MIME_KEYS = ("contentMimeType", "mimeType", "mime_type", "contentType", "content_type")
KEEP_TYPE_KEYS = ("disableConversionToGoogleType",)
PARENT_KEYS = ("parentId", "parent_id", "folderId", "folder_id", "parent", "parents")
DESCRIPTION_KEYS = ("description",)
ENCODING_KEYS = ("encoding", "contentEncoding")

_UPLOAD_HINTS = ("upload", "create_file", "createfile", "create-file", "files_create", "create")


class UploadMappingError(RuntimeError):
    pass


def pick_upload_tool(tools: list[Tool], override: str = "") -> Tool:
    """The configured tool, or the first one whose name looks like an upload."""
    by_name = {t.name: t for t in tools}
    if override:
        if override not in by_name:
            raise UploadMappingError(
                f"DRIVE_UPLOAD_TOOL={override} is not offered to this identity. Offered: {sorted(by_name)}"
            )
        return by_name[override]
    for hint in _UPLOAD_HINTS:
        for t in tools:
            if hint in t.name.lower().replace("-", "_") or hint in t.name.lower():
                return t
    raise UploadMappingError(f"no upload tool found; set DRIVE_UPLOAD_TOOL. Offered: {sorted(by_name)}")


def _first(props: dict[str, Any], keys: tuple[str, ...]) -> str | None:
    for k in keys:
        if k in props:
            return k
    return None


@dataclass
class UploadSpec:
    filename: str
    data: bytes
    media_type: str
    folder_id: str = ""
    description: str = ""


def build_args(tool: Tool, spec: UploadSpec) -> dict[str, Any]:
    schema = tool.inputSchema or {}
    props: dict[str, Any] = schema.get("properties", {}) or {}
    required: list[str] = list(schema.get("required", []) or [])
    args: dict[str, Any] = {}

    if key := _first(props, NAME_KEYS):
        args[key] = spec.filename
    if key := _first(props, CONTENT_KEYS):
        args[key] = base64.b64encode(spec.data).decode()
        if enc := _first(props, ENCODING_KEYS):
            args[enc] = "base64"
    if key := _first(props, MIME_KEYS):
        args[key] = spec.media_type
    if spec.folder_id and (key := _first(props, PARENT_KEYS)):
        args[key] = [spec.folder_id] if props[key].get("type") == "array" else spec.folder_id
    if key := _first(props, KEEP_TYPE_KEYS):
        args[key] = True  # upload the PDF as a PDF; Drive would otherwise convert some types
    if spec.description and (key := _first(props, DESCRIPTION_KEYS)):
        args[key] = spec.description

    missing = [r for r in required if r not in args]
    if missing:
        raise UploadMappingError(
            f"cannot fill required fields {missing} of {tool.name}; schema: {json.dumps(schema)[:800]}"
        )
    return args


_LINK = re.compile(r"https://(?:drive|docs)\.google\.com/[^\s\"')]+")


def parse_result(text: str, structured: Any) -> tuple[str, str]:
    """(file_id, link) from the tool's reply, whichever shape it uses."""
    candidates: list[Any] = [structured]
    try:
        candidates.append(json.loads(text))
    except (ValueError, TypeError):
        pass
    for c in candidates:
        if isinstance(c, dict):
            link = c.get("webViewLink") or c.get("url") or c.get("link") or ""
            file_id = c.get("id") or c.get("fileId") or ""
            if link or file_id:
                if not link and file_id:
                    link = f"https://drive.google.com/file/d/{file_id}/view"
                return str(file_id), str(link)
    m = _LINK.search(text or "")
    return "", m.group(0) if m else ""
