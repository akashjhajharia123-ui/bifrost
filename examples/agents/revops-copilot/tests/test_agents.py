"""Supervisor routing, publisher upload mapping, PDF rendering."""

from __future__ import annotations

import base64
from contextlib import asynccontextmanager
from types import SimpleNamespace
from typing import Any

import pytest
from langchain_core.messages import AIMessage
from mcp.types import Tool

from revops_copilot.agents.publisher.upload import (
    UploadMappingError,
    UploadSpec,
    build_args,
    parse_result,
    pick_upload_tool,
)
from revops_copilot.agents.reportwriter.pdf import render
from revops_copilot.common.a2aserver import AgentReply, FileBlob, TaskInput
from revops_copilot.common.bifrost import BifrostSession, TaskScope
from revops_copilot.common.config import AgentName, GlobalSettings
from revops_copilot.common.contracts import (
    AccountHealth,
    MonthCount,
    ReportDraft,
    ReportRequest,
    ReportSection,
    SupportMetrics,
)
from revops_copilot.common.identity import VirtualKeyCredential
from revops_copilot.common.policy import Hop, PolicyDenied, Surface
from revops_copilot.common import toolloop
from revops_copilot.supervisor import agent as supervisor

METRICS = SupportMetrics(
    quarter_label="Q3 2026",
    total_issues=120,
    resolved_issues=101,
    median_first_response_hours=2.5,
    sla_breaches=7,
    issues_by_month=[MonthCount(month="2026-07", count=40), MonthCount(month="2026-08", count=50)],
    issues_by_priority={"urgent": 9, "high": 30},
    top_accounts=[AccountHealth(account="Acme", open_issues=6, sla_breaches=2, note="SSO <rollout>")],
    themes=["SSO setup"],
)

# ---------------------------------------------------------------- contracts


def test_report_request_is_any_period() -> None:
    from datetime import date

    q3 = ReportRequest.quarter(3, 2026)
    assert (q3.start, q3.end, q3.label) == (date(2026, 7, 1), date(2026, 9, 30), "Q3 2026")
    assert ReportRequest.quarter(4, 2026).end == date(2026, 12, 31)
    day = ReportRequest(start=date(2026, 11, 10), end=date(2026, 11, 10))
    assert day.label == "2026-11-10"  # a single day labels itself
    assert ReportRequest(start=date(2026, 11, 3), end=date(2026, 11, 9)).label == "2026-11-03 to 2026-11-09"
    with pytest.raises(ValueError):
        ReportRequest(start=date(2026, 11, 10), end=date(2026, 11, 9))


# ---------------------------------------------------------------- PDF


def test_render_produces_a_pdf_and_escapes_markup() -> None:
    draft = ReportDraft(
        title="Q3 2026 Customer Health",
        executive_summary="Volume up & SLA <steady>.",
        sections=[ReportSection(heading="Volume", body="Up 12%.")],
        risks=["Acme churn risk."],
        recommendations=["Staff SSO desk."],
    )
    pdf = render(draft, METRICS)
    assert pdf.startswith(b"%PDF")
    assert len(pdf) > 5_000


# ---------------------------------------------------------------- publisher


def _tool(name: str, props: dict[str, Any], required: list[str]) -> Tool:
    return Tool(name=name, inputSchema={"type": "object", "properties": props, "required": required})


def test_pick_upload_tool_prefers_override_then_name_hints() -> None:
    tools = [_tool("drive-search_files", {}, []), _tool("drive-upload_file", {}, [])]
    assert pick_upload_tool(tools).name == "drive-upload_file"
    assert pick_upload_tool(tools, "drive-search_files").name == "drive-search_files"
    with pytest.raises(UploadMappingError, match="not offered"):
        pick_upload_tool(tools, "drive-delete_file")


def test_build_args_fills_fields_by_name() -> None:
    tool = _tool(
        "drive-upload_file",
        {
            "name": {"type": "string"},
            "content": {"type": "string"},
            "mimeType": {"type": "string"},
            "parents": {"type": "array"},
            "description": {"type": "string"},
        },
        ["name", "content"],
    )
    args = build_args(tool, UploadSpec("r.pdf", b"%PDF", "application/pdf", "folder-1", "Q3 report"))
    assert args["name"] == "r.pdf"
    assert base64.b64decode(args["content"]) == b"%PDF"
    assert args["parents"] == ["folder-1"]
    assert args["mimeType"] == "application/pdf"


def test_build_args_refuses_to_guess_required_fields() -> None:
    tool = _tool("drive-upload_file", {"name": {"type": "string"}, "driveItem": {"type": "object"}}, ["driveItem"])
    with pytest.raises(UploadMappingError, match="driveItem"):
        build_args(tool, UploadSpec("r.pdf", b"%PDF", "application/pdf"))


def test_parse_result_reads_json_or_links() -> None:
    assert parse_result('{"id":"abc"}', None) == ("abc", "https://drive.google.com/file/d/abc/view")
    assert parse_result("done: https://drive.google.com/file/d/xyz/view.", None)[1].startswith(
        "https://drive.google.com/file/d/xyz/view"
    )


# ---------------------------------------------------------------- supervisor


def _session() -> BifrostSession:
    return BifrostSession(
        agent=AgentName.SUPERVISOR,
        settings=GlobalSettings(bifrost_url="http://bifrost.test"),
        credential=VirtualKeyCredential("sk-bf-sup"),
        scope=TaskScope(context_id="ctx", task_id="t"),
    )


class ScriptedModel:
    """A chat model that answers from a script: tool calls first, then text.
    `unbound` records the calls made without tools (the forced final answer)."""

    def __init__(self, script: list[AIMessage]) -> None:
        self.script = script
        self.unbound_calls = 0

    def bind_tools(self, tools: Any) -> BoundScripted:
        return BoundScripted(self)

    async def ainvoke(self, messages: Any) -> AIMessage:
        self.unbound_calls += 1
        return self.script.pop(0)


class BoundScripted:
    def __init__(self, model: ScriptedModel) -> None:
        self.model = model

    async def ainvoke(self, messages: Any) -> AIMessage:
        return self.model.script.pop(0)


def _call(name: str, **args: Any) -> AIMessage:
    return AIMessage("", tool_calls=[{"name": name, "args": args, "id": f"call-{name}"}])


@pytest.fixture
def wired(monkeypatch: pytest.MonkeyPatch) -> dict[str, Any]:
    calls: list[AgentName] = []
    sent: dict[AgentName, dict[str, Any]] = {}
    behaviour: dict[AgentName, AgentReply] = {}
    script: list[AIMessage] = []

    async def fake_call(session: BifrostSession, target: AgentName, text: str, **kw: Any) -> AgentReply:
        calls.append(target)
        sent[target] = {"text": text, **kw}
        if target in behaviour:
            return behaviour[target]
        if target is AgentName.SUPPORT_ANALYST and kw.get("data"):
            return AgentReply("ok", [], {"support_metrics": METRICS.model_dump(mode="json")}, None, "TASK_STATE_COMPLETED")
        if target is AgentName.SUPPORT_ANALYST:
            return AgentReply("42 issues were created last week.", [], {}, None, "TASK_STATE_COMPLETED")
        if target is AgentName.REPORT_WRITER:
            return AgentReply("ok", [FileBlob("r.pdf", b"%PDF", "application/pdf")], {}, None, "TASK_STATE_COMPLETED")
        return AgentReply("uploaded", [], {"publish": {"link": "https://drive.google.com/file/d/x/view"}}, None, "TASK_STATE_COMPLETED")

    models: list[ScriptedModel] = []

    async def fake_chat_model(self: BifrostSession, **_: Any) -> ScriptedModel:
        models.append(ScriptedModel(script))
        return models[-1]

    @asynccontextmanager
    async def no_mcp_server(self: BifrostSession, slug: str | None = None) -> Any:
        yield SimpleNamespace()  # Bifrost's MCP server with no tools granted

    async def no_tools(_: Any) -> list[Any]:
        return []

    monkeypatch.setattr(supervisor, "call_agent", fake_call)
    monkeypatch.setattr(BifrostSession, "chat_model", fake_chat_model)
    monkeypatch.setattr(BifrostSession, "mcp", no_mcp_server)
    monkeypatch.setattr(toolloop, "discover_tools", no_tools)
    return {"calls": calls, "sent": sent, "behaviour": behaviour, "script": script, "models": models}


async def test_a_question_goes_to_the_analyst_only(wired: dict[str, Any]) -> None:
    wired["script"] += [_call("ask_support_analyst", question="How many issues were created last week?"),
                        AIMessage("42 issues were created last week.")]
    out = await supervisor.run(TaskInput("tell me just the count of issues last week", [], [], _session()))
    assert wired["calls"] == [AgentName.SUPPORT_ANALYST]
    # Question mode: free text, no ReportRequest data part, so nothing is drafted or uploaded.
    assert "data" not in wired["sent"][AgentName.SUPPORT_ANALYST] or not wired["sent"][AgentName.SUPPORT_ANALYST]["data"]
    assert out.text == "42 issues were created last week." and out.files == [] and out.denied is None


async def test_a_report_request_runs_the_pipeline(wired: dict[str, Any]) -> None:
    wired["script"] += [_call("prepare_report", start_date="2026-07-01", end_date="2026-09-30", label="Q3 2026",
                              audience="leadership", upload=True),
                        AIMessage("Published the Q3 2026 report.")]
    out = await supervisor.run(TaskInput("Prepare the Q3 2026 report", [], [], _session()))
    assert wired["calls"] == [AgentName.SUPPORT_ANALYST, AgentName.REPORT_WRITER, AgentName.PUBLISHER]
    assert wired["sent"][AgentName.SUPPORT_ANALYST]["data"] == {
        "start": "2026-07-01", "end": "2026-09-30", "label": "Q3 2026", "audience": "leadership", "focus": ""}
    assert out.data["publish"]["link"].startswith("https://drive.google.com")
    assert out.files[0].name == "r.pdf"
    assert out.text == "Published the Q3 2026 report."


async def test_a_report_without_upload_skips_the_publisher(wired: dict[str, Any]) -> None:
    wired["script"] += [_call("prepare_report", start_date="2026-04-01", end_date="2026-06-30", upload=False),
                        AIMessage("Here is the PDF.")]
    out = await supervisor.run(TaskInput("Write the Q2 2026 report, don't upload it", [], [], _session()))
    assert wired["calls"] == [AgentName.SUPPORT_ANALYST, AgentName.REPORT_WRITER]
    assert out.files[0].name == "r.pdf"


async def test_supervisor_stops_at_the_first_refusal(wired: dict[str, Any]) -> None:
    denied = PolicyDenied(Surface.MCP_GUARDRAIL, Hop.MCP, 400, "PII", "guardrail_intervention")
    wired["behaviour"][AgentName.SUPPORT_ANALYST] = AgentReply("", [], denied.to_data(), denied, "TASK_STATE_REJECTED")
    wired["script"] += [_call("ask_support_analyst", question="list customer emails")]
    out = await supervisor.run(TaskInput("list customer emails", [], [], _session()))
    assert wired["calls"] == [AgentName.SUPPORT_ANALYST]
    assert out.denied is not None and out.denied.surface is Surface.MCP_GUARDRAIL
    assert "7-mcp-tool-guardrail" in out.text


async def test_gateway_refusal_on_delegation_is_reported(wired: dict[str, Any], monkeypatch: pytest.MonkeyPatch) -> None:
    async def blocked(*_: Any, **__: Any) -> AgentReply:
        raise PolicyDenied(Surface.AGENT_GRANT, Hop.A2A, 403, "agent_blocked", "agent_blocked")

    monkeypatch.setattr(supervisor, "call_agent", blocked)
    wired["script"] += [_call("ask_support_analyst", question="how many issues?")]
    out = await supervisor.run(TaskInput("how many issues?", [], [], _session()))
    assert out.denied is not None and out.denied.surface is Surface.AGENT_GRANT


async def test_the_last_round_answers_with_what_it_has(wired: dict[str, Any]) -> None:
    # The model keeps asking the analyst; the last round runs without tools and must answer.
    rounds = supervisor.MAX_ROUNDS
    wired["script"] += [_call("ask_support_analyst", question=f"try {i}") for i in range(rounds - 1)]
    wired["script"] += [AIMessage("42 issues were created last week.")]
    out = await supervisor.run(TaskInput("how many issues last week?", [], [], _session()))
    assert wired["calls"] == [AgentName.SUPPORT_ANALYST] * (rounds - 1)
    assert out.text == "42 issues were created last week."
    assert wired["models"][0].unbound_calls == 1  # the final round had no tools to call


async def test_supervisor_can_call_bifrost_mcp_tools_directly(wired: dict[str, Any], monkeypatch: pytest.MonkeyPatch) -> None:
    from langchain_core.tools import StructuredTool

    called: list[tuple[str, dict[str, Any]]] = []

    class FakeMCP:
        async def call_tool(self, name: str, args: dict[str, Any]) -> Any:
            called.append((name, args))
            return SimpleNamespace(content=[SimpleNamespace(type="text", text="7 issues")], isError=False, structuredContent=None)

    @asynccontextmanager
    async def fake_mcp(self: BifrostSession, slug: str | None = None) -> Any:
        yield FakeMCP()

    async def fake_discover(_: Any) -> list[Any]:
        async def noop(**_: Any) -> str:
            return ""
        return [StructuredTool.from_function(coroutine=noop, name="Pylon-search_issues", description="search issues")]

    monkeypatch.setattr(BifrostSession, "mcp", fake_mcp)
    monkeypatch.setattr(toolloop, "discover_tools", fake_discover)
    wired["script"] += [_call("Pylon-search_issues", created_after="2026-11-04"), AIMessage("7 issues last week.")]
    out = await supervisor.run(TaskInput("how many issues last week?", [], [], _session()))
    assert called == [("Pylon-search_issues", {"created_after": "2026-11-04"})]
    assert wired["calls"] == []  # answered from Bifrost's MCP server, no delegation needed
    assert out.text == "7 issues last week."


def test_build_args_uses_drives_current_create_file_fields() -> None:
    # Drive's create_file: content/mimeType are deprecated ("DO NOT USE"); contentMimeType
    # is required whenever content is sent.
    props = {k: {"type": "string"} for k in ("title", "base64Content", "content", "contentMimeType", "mimeType", "parentId", "textContent")}
    props["disableConversionToGoogleType"] = {"type": "boolean"}
    tool = _tool("Drive-create_file", props, ["title"])
    args = build_args(tool, UploadSpec("q3.pdf", b"%PDF", "application/pdf", "folder-1", "Q3 report"))
    assert args == {
        "title": "q3.pdf",
        "base64Content": base64.b64encode(b"%PDF").decode(),
        "contentMimeType": "application/pdf",
        "parentId": "folder-1",
        "disableConversionToGoogleType": True,  # keep it a PDF, not a Google Doc
    }


async def test_a_report_for_yesterday_runs_the_pipeline(wired: dict[str, Any]) -> None:
    wired["script"] += [_call("prepare_report", start_date="2026-11-10", end_date="2026-11-10", upload=True),
                        AIMessage("Uploaded yesterday's report.")]
    out = await supervisor.run(TaskInput("upload a report of yesterday's issues to Drive", [], [], _session()))
    assert wired["calls"] == [AgentName.SUPPORT_ANALYST, AgentName.REPORT_WRITER, AgentName.PUBLISHER]
    assert wired["sent"][AgentName.SUPPORT_ANALYST]["data"]["label"] == "2026-11-10"
    assert out.data["publish"]["link"].startswith("https://drive.google.com")


def test_prepare_report_accepts_timestamps_as_days() -> None:
    from datetime import date

    call = supervisor.prepare_report.model_validate({"start_date": "2026-11-11T01:11:00Z", "end_date": "2026-11-11T06:11:00Z"})
    assert (call.start_date, call.end_date) == (date(2026, 11, 11), date(2026, 11, 11))


async def test_bad_tool_arguments_go_back_to_the_model(wired: dict[str, Any]) -> None:
    # The model sends an unusable date; it gets the error as a tool result and answers.
    wired["script"] += [_call("prepare_report", start_date="last tuesday", end_date="2026-11-11"),
                        AIMessage("Which dates should the report cover?")]
    out = await supervisor.run(TaskInput("report for last tuesday", [], [], _session()))
    assert wired["calls"] == []  # nothing was delegated with bad arguments
    assert out.denied is None and out.text == "Which dates should the report cover?"
