"""Print what Bifrost logged for one job, ordered by time, labelled by surface.

One job is one Bifrost session (x-bf-session-id = the A2A context id), so three
read-only queries cover it:

* GET /api/agents/history?context_id=<id>  A2A hops (Agent Gateway)
* GET /api/logs?session_id=<id>            LLM calls
* GET /api/mcp-logs?session_id=<id>        MCP tool calls

Field names differ slightly across Bifrost versions, so rows are read
defensively and unknown fields are skipped.
"""

from __future__ import annotations

import os
from typing import Any

import httpx
from rich.console import Console
from rich.table import Table

from revops_copilot.common.config import GlobalSettings

console = Console()


def _items(body: Any, *keys: str) -> list[dict[str, Any]]:
    if isinstance(body, list):
        return [x for x in body if isinstance(x, dict)]
    if isinstance(body, dict):
        for k in keys:
            if isinstance(body.get(k), list):
                return [x for x in body[k] if isinstance(x, dict)]
    return []


def _get(row: dict[str, Any], *keys: str) -> str:
    for k in keys:
        cur: Any = row
        for part in k.split("."):
            cur = cur.get(part) if isinstance(cur, dict) else None
        if cur not in (None, "", [], {}):
            return str(cur)
    return ""


def _who(row: dict[str, Any]) -> str:
    return _get(row, "user_name", "user_email", "user_id", "virtual_key_name", "virtual_key.name", "virtual_key_id")


def _guardrail(row: dict[str, Any]) -> str:
    text = _get(row, "guardrail_result", "guardrails", "metadata.guardrails", "error_details.error.type")
    return "guardrail" if "guardrail" in text.lower() else ""


def _surfaces(kind: str, row: dict[str, Any]) -> str:
    status = _get(row, "status").lower()
    err = _get(row, "error_details.error.message", "error_details.message", "error", "error_message").lower()
    tags = {"a2a": "1,2", "llm": "1,3,4,8", "mcp": "1,5,6,7"}[kind]
    if "guardrail" in err or _guardrail(row):
        tags += " guardrail"
    if any(c in err for c in ("blocked", "exceeded", "limited", "not allowed")):
        tags += " governance"
    return tags if status not in ("error", "failed", "rejected") else f"[red]{tags} refused"


async def trace(session_id: str) -> int:
    g = GlobalSettings()
    token = os.environ.get("BIFROST_ADMIN_TOKEN", "")
    headers = {"Authorization": f"Bearer {token}"} if token else {}
    rows: list[tuple[str, str, str, str, str, str]] = []
    async with httpx.AsyncClient(base_url=g.bifrost_url, headers=headers, timeout=30) as http:
        queries = (
            ("a2a", "/api/agents/history", {"context_id": session_id, "limit": 200}, ("data", "logs", "items", "history")),
            ("llm", "/api/logs", {"session_id": session_id, "limit": 200}, ("logs", "data", "items")),
            ("mcp", "/api/mcp-logs", {"session_id": session_id, "limit": 200}, ("logs", "data", "items")),
        )
        for kind, path, params, keys in queries:
            r = await http.get(path, params=params)
            if r.status_code != 200:
                console.print(f"[yellow]{path}: {r.status_code} {r.text[:200]}")
                continue
            for row in _items(r.json(), *keys):
                what = {
                    "a2a": _get(row, "agent_name", "agent", "method"),
                    "llm": _get(row, "model", "provider"),
                    "mcp": _get(row, "tool_name", "tool", "mcp_client_name"),
                }[kind]
                rows.append(
                    (
                        _get(row, "timestamp", "created_at", "started_at"),
                        kind,
                        what,
                        _who(row),
                        _get(row, "status"),
                        _surfaces(kind, row),
                    )
                )
    rows.sort(key=lambda r: r[0])
    table = Table("time", "hop", "target", "identity", "status", "surfaces", title=f"session {session_id}")
    for row in rows:
        table.add_row(*row)
    console.print(table)
    if not rows:
        console.print("No rows. Check BIFROST_ADMIN_TOKEN, log retention, and that the agents sent x-bf-session-id.")
    return 0
