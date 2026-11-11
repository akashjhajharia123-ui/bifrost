"""Scenario files, the scenario checker, and trace row parsing."""

from __future__ import annotations

from pathlib import Path

import pytest
import yaml

from revops_copilot import scenarios, trace
from revops_copilot.common.policy import Hop, PolicyDenied, Surface

SCENARIOS = sorted((Path(__file__).resolve().parents[1] / "scenarios").glob("*.yaml"))


def test_every_surface_has_a_scenario() -> None:
    declared = {yaml.safe_load(p.read_text())["surface"] for p in SCENARIOS}
    for surface in Surface:
        if surface is Surface.UNKNOWN:
            continue
        assert surface.value in declared, f"no scenario for {surface}"


@pytest.mark.parametrize("path", SCENARIOS, ids=lambda p: p.stem)
def test_scenarios_are_well_formed(path: Path) -> None:
    spec = yaml.safe_load(path.read_text())
    assert spec["kind"] in {"ask", "a2a", "llm", "mcp_list", "mcp_call"}
    assert spec.get("description")
    expect = spec.get("expect", {})
    if "surface" in expect:
        Surface(expect["surface"])
    if spec["kind"] == "mcp_call":
        assert spec.get("tool")  # slug is optional: no slug means Bifrost's /mcp


def test_load_expands_env(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setenv("BLOCKED_MODEL", "openai/gpt-x")
    assert scenarios._load("model-not-allowed")["model"] == "openai/gpt-x"


def test_check_matches_surface_and_patterns() -> None:
    denied = PolicyDenied(Surface.AGENT_GRANT, Hop.A2A, 403, "agent_blocked", "agent_blocked")
    assert scenarios._check({"expect": {"surface": "2-agent-grant"}}, denied, "")
    assert not scenarios._check({"expect": {"surface": "3-llm-governance"}}, denied, "")
    assert not scenarios._check({"expect": {"allowed": True}}, denied, "")
    pii = {"expect": {"absent_patterns": [r"[\w.]+@[\w.]+"]}}
    assert scenarios._check(pii, None, "contact <REDACTED>")
    assert not scenarios._check(pii, None, "contact jane@example.com")


def test_trace_reads_nested_fields_defensively() -> None:
    row = {"timestamp": "t1", "status": "error", "error_details": {"error": {"message": "guardrail_intervention: PII"}}}
    assert trace._get(row, "error_details.error.message").startswith("guardrail")
    assert "refused" in trace._surfaces("mcp", row)
    assert "guardrail" in trace._surfaces("mcp", row)
    assert trace._items({"logs": [row, "junk"]}, "data", "logs") == [row]


def test_serve_without_identity_exits_with_one_line(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    from revops_copilot import cli

    monkeypatch.chdir(tmp_path)  # no .env here
    monkeypatch.setattr(cli, "load_dotenv", lambda **_: None)
    monkeypatch.setenv("AUTH_MODE", "vk")
    # Only the entry agent holds an identity; subagents boot without one.
    monkeypatch.delenv("REVOPS_COPILOT_VK", raising=False)
    with pytest.raises(SystemExit) as exc:
        cli.main(["serve", "revops-copilot"])
    message = str(exc.value.code)
    assert "revops-copilot" in message and "REVOPS_COPILOT_VK" in message and "BIFROST_SETUP.md" in message


def test_tunnel_reads_ngrok_endpoints_by_name_then_port() -> None:
    from revops_copilot import tunnel
    from revops_copilot.common.config import AgentName

    endpoints = {
        "endpoints": [
            {"name": "revops-copilot", "url": "https://rc.ngrok.app", "upstream": {"url": "http://localhost:9100"}},
            {"name": "other", "url": "https://wrong.ngrok.app", "upstream": {"url": "http://localhost:9101"}},
            {"name": "support-analyst", "url": "https://sa.ngrok.app/", "upstream": {"url": "9101"}},
        ]
    }
    # Deprecated /api/tunnels shape, matched by upstream port.
    tunnels = {"tunnels": [{"name": "x", "public_url": "https://rw.ngrok.app", "config": {"addr": "localhost:9102"}}]}
    ports = {a: p for a, p in zip(AgentName, (9100, 9101, 9102, 9103), strict=True)}
    urls = tunnel.public_urls(ports, endpoints, tunnels)
    assert urls == {
        AgentName.SUPERVISOR: "https://rc.ngrok.app",
        AgentName.SUPPORT_ANALYST: "https://sa.ngrok.app",
        AgentName.REPORT_WRITER: "https://rw.ngrok.app",
    }


def test_tunnel_refuses_public_agents_without_inbound_keys(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    from revops_copilot import tunnel

    monkeypatch.chdir(tmp_path)
    for p in ("REVOPS_COPILOT_", "SUPPORT_ANALYST_", "REPORT_WRITER_"):
        monkeypatch.setenv(f"{p}INBOUND_KEY", "k")
    monkeypatch.delenv("PUBLISHER_INBOUND_KEY", raising=False)
    assert tunnel.missing_inbound_keys() == ["PUBLISHER_INBOUND_KEY"]


def _answers(*values: str):  # type: ignore[no-untyped-def]
    it = iter(values)
    return lambda _prompt: next(it)


def test_preflight_asks_for_the_gateway_url(monkeypatch: pytest.MonkeyPatch) -> None:
    import os

    from revops_copilot import preflight

    monkeypatch.delenv("BIFROST_URL", raising=False)
    # Empty and invalid answers are asked again; a trailing slash is dropped.
    url = preflight.ask_gateway_url(_answers("", "bifrost.example.com", "https://bifrost.example.com/"), tty=True)
    assert url == "https://bifrost.example.com"
    assert os.environ["BIFROST_URL"] == url


def test_preflight_offers_the_configured_url_as_default(monkeypatch: pytest.MonkeyPatch) -> None:
    from revops_copilot import preflight

    monkeypatch.setenv("BIFROST_URL", "https://configured.example")
    assert preflight.ask_gateway_url(_answers(""), tty=True) == "https://configured.example"


def test_preflight_without_a_terminal_needs_bifrost_url(monkeypatch: pytest.MonkeyPatch) -> None:
    from revops_copilot import preflight

    monkeypatch.setenv("BIFROST_URL", "https://configured.example")
    assert preflight.ask_gateway_url(_answers(), tty=False) == "https://configured.example"
    monkeypatch.delenv("BIFROST_URL")
    with pytest.raises(SystemExit, match="BIFROST_URL"):
        preflight.ask_gateway_url(_answers(), tty=False)


async def test_preflight_probes_the_listen_port_and_shows_the_advertised_url(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    import httpx
    import respx

    from revops_copilot import preflight

    monkeypatch.chdir(tmp_path)
    # A public URL in the environment must not change where liveness is probed.
    monkeypatch.setenv("REVOPS_COPILOT_PUBLIC_URL", "https://rc.ngrok.app")
    rows: list[tuple[str, bool | None, str]] = []
    report = preflight.Report()
    monkeypatch.setattr(report, "add", lambda check, ok, detail="": rows.append((check, ok, detail)))
    card = {"supportedInterfaces": [{"url": "https://rc.ngrok.app/"}]}
    with respx.mock(assert_all_called=False) as mock:
        mock.get("http://127.0.0.1:9100/.well-known/agent-card.json").respond(200, json=card)
        for port in (9101, 9102, 9103):
            mock.get(f"http://127.0.0.1:{port}/.well-known/agent-card.json").mock(side_effect=httpx.ConnectError("down"))
        await preflight._local_agents(report)

    assert rows[0] == ("revops-copilot: listening on :9100", True, "card advertises https://rc.ngrok.app/")
    assert rows[1][0] == "support-analyst: listening on :9101"
    assert rows[1][1] is False  # agents down is a failure, not a skip
    assert "make agents-ngrok" in rows[1][2]


def test_tunnel_finds_the_default_ngrok_config_from_config_check() -> None:
    from revops_copilot import tunnel

    out = "Valid configuration file at /Users/me/Library/Application Support/ngrok/ngrok.yml\n"
    assert tunnel.default_config_from_check(out) == "/Users/me/Library/Application Support/ngrok/ngrok.yml"
    # Help text or an error is never mistaken for a path.
    assert tunnel.default_config_from_check("NAME:\n  config - update or migrate ngrok's configuration file\n") == ""


def test_tunnel_reads_the_cloudflared_quick_tunnel_hostname() -> None:
    from revops_copilot import tunnel

    assert tunnel.quick_tunnel_url({"hostname": "calm-river-1234.trycloudflare.com"}) == (
        "https://calm-river-1234.trycloudflare.com"
    )
    # Not ready yet: cloudflared answers with an empty hostname until the tunnel is up.
    assert tunnel.quick_tunnel_url({"hostname": ""}) == ""
    assert tunnel.quick_tunnel_url(None) == ""


def test_tunnel_reads_named_tunnel_hostnames_from_ingress() -> None:
    from revops_copilot import tunnel
    from revops_copilot.common.config import AgentName

    config = {
        "tunnel": "revops",
        "credentials-file": "/home/me/.cloudflared/abc.json",
        "ingress": [
            {"hostname": "revops-copilot.example.com", "service": "http://127.0.0.1:9100"},
            {"hostname": "support-analyst.example.com", "service": "http://localhost:9101"},
            {"hostname": "report-writer.example.com", "service": "http://127.0.0.1:9102/"},
            {"hostname": "publisher.example.com", "service": "http://127.0.0.1:9103"},
            {"service": "http_status:404"},
        ],
    }
    ports = {a: p for a, p in zip(AgentName, (9100, 9101, 9102, 9103), strict=True)}
    assert tunnel.named_tunnel_urls(ports, config) == {
        AgentName.SUPERVISOR: "https://revops-copilot.example.com",
        AgentName.SUPPORT_ANALYST: "https://support-analyst.example.com",
        AgentName.REPORT_WRITER: "https://report-writer.example.com",
        AgentName.PUBLISHER: "https://publisher.example.com",
    }
    # A rule for another port, or the catch-all, never maps to an agent.
    assert tunnel.named_tunnel_urls({AgentName.PUBLISHER: 9999}, config) == {}


def test_tui_formats_a_progress_line_with_depth_surface_and_time() -> None:
    from revops_copilot import tui
    from revops_copilot.common.progress import Progress

    line = tui.format_line(Progress(agent="support-analyst", kind="tool", title="Pylon-search_issues",
                                    detail="212 rows", surfaces="⑥", depth=1, elapsed_ms=812))
    assert line.startswith("  ├ ")
    assert "Pylon-search_issues" in line and "212 rows" in line and "⑥" in line and "0.8s" in line
    top = tui.format_line(Progress(agent="revops-copilot", kind="delegate", title="support-analyst", depth=0))
    assert top.startswith("● ") and "support-analyst" in top


def test_tui_marks_a_running_step() -> None:
    from revops_copilot import tui
    from revops_copilot.common.progress import Progress

    line = tui.format_line(Progress(agent="a", kind="llm", title="decide next step (2/12)", status="start", depth=2))
    assert line.startswith("    ├ … LLM decide next step (2/12)")


async def test_preflight_reports_a_token_failure_as_a_row(monkeypatch: pytest.MonkeyPatch) -> None:
    from types import SimpleNamespace

    from revops_copilot import preflight

    class BadCredential:
        async def auth_headers(self) -> dict[str, str]:
            raise RuntimeError("AADSTS7000215: Invalid client secret provided.")

        async def close(self) -> None:
            return None

    session = SimpleNamespace(credential=BadCredential(), settings=SimpleNamespace(bifrost_url="http://bifrost.test"))
    session.headers = session.credential.auth_headers
    monkeypatch.setattr(preflight, "caller_session", lambda: session)
    rows: list[tuple[str, bool | None, str]] = []
    report = preflight.Report()
    monkeypatch.setattr(report, "add", lambda check, ok, detail="": rows.append((check, ok, detail)))
    assert await preflight._identity(report) is None
    assert rows == [("identity token", False, "RuntimeError: AADSTS7000215: Invalid client secret provided.")]
