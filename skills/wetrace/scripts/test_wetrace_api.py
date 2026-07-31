from __future__ import annotations

import importlib.util
import tempfile
import unittest
from datetime import datetime
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("wetrace_api.py")
SPEC = importlib.util.spec_from_file_location("wetrace_api", MODULE_PATH)
assert SPEC and SPEC.loader
wetrace = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(wetrace)


def message(sender: str, when: str, from_me: bool = False) -> dict:
    dt = datetime.strptime(when, "%Y-%m-%d %H:%M:%S")
    return {
        "create_time": int(dt.timestamp()),
        "time": when,
        "sender": sender,
        "sender_wxid": sender,
        "is_from_me": from_me,
        "kind": "text",
        "text": "hello",
        "voice_transcript_status": None,
    }


class FakeLocal:
    def all_sessions(self, max_sessions: int, type_filter: str = "private,group"):
        return [
            {"username": "alice", "display_name": "Alice", "chat_type": "private"},
            {"username": "team", "display_name": "Team", "chat_type": "group"},
        ][:max_sessions], {
            "session_limit": max_sessions,
            "scanned_sessions": min(2, max_sessions),
            "sessions_truncated": max_sessions < 2,
        }

    def all_messages(self, talker: str, time_range: str | None, max_messages: int):
        rows = {
            "alice": [message("Alice", "2026-01-01 09:00:00"), message("me", "2026-01-02 10:00:00", True)],
            "team": [message("Bob", "2026-02-01 11:00:00")],
        }[talker][:max_messages]
        return rows, {
            "talker": talker,
            "display_name": talker,
            "time_range": time_range or "all",
            "returned": len(rows),
            "max_messages": max_messages,
            "truncated": max_messages < len({"alice": [1, 2], "team": [1]}[talker]),
        }


class FailingLocal(FakeLocal):
    def all_messages(self, talker: str, time_range: str | None, max_messages: int):
        if talker == "team":
            raise wetrace.WetraceError("unreadable")
        return super().all_messages(talker, time_range, max_messages)


class TimeRangeTests(unittest.TestCase):
    def test_exact_day(self):
        self.assertEqual(
            wetrace.parse_time_range("2026-01-02"),
            ("2026-01-02 00:00:00", "2026-01-02 23:59:59"),
        )

    def test_explicit_range_extends_end_of_day(self):
        self.assertEqual(
            wetrace.parse_time_range("2026-01-01~2026-01-31"),
            ("2026-01-01", "2026-01-31 23:59:59"),
        )


class ExecutableResolutionTests(unittest.TestCase):
    def test_resolves_simple_cmd_shim_without_shell(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable = root / "wechat-cli.exe"
            executable.touch()
            shim = root / "wechat-cli.cmd"
            shim.write_text(f'"{executable}" %*\n', encoding="utf-8")
            self.assertEqual(wetrace.WechatCLI._resolve_executable(str(shim)), str(executable))

    def test_rejects_unparseable_cmd_shim(self):
        with tempfile.TemporaryDirectory() as directory:
            shim = Path(directory) / "wechat-cli.cmd"
            shim.write_text("echo unsafe\n", encoding="utf-8")
            with self.assertRaises(wetrace.WetraceError):
                wetrace.WechatCLI._resolve_executable(str(shim))


class AnalysisTests(unittest.TestCase):
    def test_top_contacts_reports_scope_and_sorting(self):
        report = wetrace.analyze_global(FakeLocal(), "top_contacts", "2026-01-01~2026-12-31", 2, 10)
        self.assertEqual(report["scope"]["scanned_sessions"], 2)
        self.assertFalse(report["scope"]["truncated"])
        self.assertEqual([row["talker"] for row in report["data"]], ["alice", "team"])
        self.assertEqual(report["scope"]["total_messages_scanned"], 3)

    def test_annual_aggregates_messages(self):
        report = wetrace.analyze_global(FakeLocal(), "annual", "2026-01-01~2026-12-31", 2, 10)
        self.assertEqual(report["data"]["summary"]["total_messages"], 3)
        self.assertEqual(report["data"]["summary"]["from_me"], 1)
        self.assertEqual(len(report["data"]["monthly"]), 2)

    def test_failed_chat_marks_global_result_incomplete(self):
        report = wetrace.analyze_global(FailingLocal(), "top_contacts", None, 2, 10)
        self.assertTrue(report["scope"]["truncated"])
        self.assertEqual(report["scope"]["failed_chat_count"], 1)


class DashboardSafetyTests(unittest.TestCase):
    def test_dynamic_text_cannot_close_script(self):
        attack = "</script><script>alert(1)</script>"
        rows = [message(attack, "2026-01-01 09:00:00")]
        report = wetrace.analyze(rows, {
            "talker": "unsafe",
            "display_name": attack,
            "time_range": attack,
            "returned": 1,
            "max_messages": 10,
            "truncated": False,
        })
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "report.html"
            wetrace.render_dashboard(report, output)
            document = output.read_text(encoding="utf-8")
        self.assertNotIn(attack, document)
        self.assertIn("\\u003c/script\\u003e", document)
        self.assertIn("const esc=", document)


if __name__ == "__main__":
    unittest.main()
