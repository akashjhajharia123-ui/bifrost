"""Run one scenario from scenarios/*.yaml and check the expected surface fired.

Kinds:
  ask        the identity -> revops-copilot through the Agent Gateway (full job)
  a2a        any identity -> any agent through the Agent Gateway
  llm        any identity -> LLM Gateway
  mcp_list   any identity -> list tools on a Virtual MCP
  mcp_call   any identity -> call one tool on a Virtual MCP

Every scenario runs as the example's one identity (REVOPS_COPILOT_*) unless it
sets `caller_vk`: a literal or ${ENV} virtual key for a different principal.
`requires` lists env vars the scenario needs; it is skipped when one is unset.
"""

from __future__ import annotations

import os
import re
from pathlib import Path
from typing import Any

import openai
import yaml
from langchain_core.messages import HumanMessage
from rich.console import Console

from revops_copilot.cli import caller_session, print_reply
from revops_copilot.common.a2aserver import call_agent
from revops_copilot.common.bifrost import BifrostSession, TaskScope, llm_refusal, tool_refusal, tool_result_text
from revops_copilot.common.config import AgentName, GlobalSettings
from revops_copilot.common.identity import VirtualKeyCredential
from revops_copilot.common.policy import PolicyDenied, Surface

console = Console()
SCENARIO_DIR = Path(__file__).resolve().parents[2] / "scenarios"


def _load(name: str) -> dict[str, Any]:
    path = SCENARIO_DIR / f"{name}.yaml"
    if not path.exists():
        raise SystemExit(f"no scenario {name}; have: {', '.join(sorted(p.stem for p in SCENARIO_DIR.glob('*.yaml')))}")
    raw = os.path.expandvars(path.read_text())
    return yaml.safe_load(raw)


def _session(spec: dict[str, Any]) -> BifrostSession:
    if vk := spec.get("caller_vk"):
        return BifrostSession(
            agent=AgentName.SUPERVISOR,
            settings=GlobalSettings(),
            credential=VirtualKeyCredential(vk),
            scope=TaskScope.new(),
        )
    return caller_session(spec.get("caller", AgentName.SUPERVISOR.env_prefix))


async def _execute(spec: dict[str, Any], session: BifrostSession) -> tuple[PolicyDenied | None, str]:
    kind = spec["kind"]
    if kind in ("ask", "a2a"):
        target = AgentName(spec.get("target", AgentName.SUPERVISOR.value))
        reply = await call_agent(session, target, spec["prompt"], data=spec.get("data"))
        print_reply(reply, session)
        return reply.denied, reply.text
    if kind == "llm":
        llm = await session.chat_model(model=spec.get("model"))
        try:
            out = await llm.ainvoke([HumanMessage(spec["prompt"])])
        except openai.APIStatusError as e:
            return llm_refusal(e, phase=spec.get("phase", "input")), ""
        return None, str(out.content)
    slug = spec.get("slug")  # none: Bifrost's MCP server (/mcp), like the agents use
    async with session.mcp(slug) as mcp:
        if kind == "mcp_list":
            names = [t.name for t in (await mcp.list_tools()).tools]
            return None, "\n".join(names)
        if kind == "mcp_call":
            result = await mcp.call_tool(spec["tool"], spec.get("args", {}))
            denied = tool_refusal(spec["tool"], result)
            return denied, tool_result_text(result)
    raise SystemExit(f"unknown kind {kind}")


def _check(spec: dict[str, Any], denied: PolicyDenied | None, text: str) -> bool:
    expect = spec.get("expect", {})
    ok = True
    if "surface" in expect:
        want = Surface(expect["surface"])
        got = denied.surface if denied else None
        if got is not want:
            console.print(f"[red]expected surface {want}, got {got}")
            ok = False
    if expect.get("allowed") and denied is not None:
        console.print(f"[red]expected success, got {denied}")
        ok = False
    for pattern in expect.get("absent_patterns", []):
        if re.search(pattern, text):
            console.print(f"[red]pattern {pattern!r} reached the caller: the guardrail did not redact it")
            ok = False
    for pattern in expect.get("present_patterns", []):
        if not re.search(pattern, text):
            console.print(f"[red]pattern {pattern!r} missing from the result")
            ok = False
    return ok


async def run_scenario(name: str) -> int:
    if not name:
        for p in sorted(SCENARIO_DIR.glob("*.yaml")):
            spec = yaml.safe_load(p.read_text())
            console.print(f"[bold]{p.stem:<20}[/] {spec.get('surface', ''):<28} {spec.get('description', '')}")
        return 0
    spec = _load(name)
    console.rule(f"[bold]{name}[/]  {spec.get('surface', '')}")
    console.print(spec.get("description", ""))
    missing = [var for var in spec.get("requires", []) if not os.environ.get(var)]
    if missing:
        console.print(f"[yellow]SKIP[/] set {', '.join(missing)} to run this scenario")
        return 0
    try:
        session = _session(spec)
    except Exception as e:  # noqa: BLE001
        console.print(f"[red]{e}")
        return 2
    try:
        denied, text = await _execute(spec, session)
    except PolicyDenied as e:
        denied, text = e, ""
    finally:
        await session.aclose()
        await session.credential.close()

    if denied is not None:
        console.print(f"[yellow]refused:[/] {denied}")
    elif text and spec["kind"] not in ("ask", "a2a"):
        console.print(text[:2000])
    ok = _check(spec, denied, text)
    console.print(f"[dim]revops trace {session.scope.context_id}[/]")
    console.print("[green]PASS" if ok else "[red]FAIL")
    return 0 if ok else 1
