#!/usr/bin/env python3
"""Local WeChat analytics powered by wechat-local-analytics."""

from __future__ import annotations

import argparse
import csv
import html
import json
import os
import re
import shutil
import subprocess
import sys
from collections import Counter
from datetime import datetime, timedelta
from pathlib import Path
from typing import Any, Iterable


KIND_TO_TYPE = {
    "text": 1,
    "image": 3,
    "voice": 34,
    "card": 42,
    "video": 43,
    "sticker": 47,
    "location": 48,
    "link": 49,
    "file": 49,
    "quote": 49,
    "forward_chat": 49,
    "miniprogram": 49,
    "transfer": 49,
    "red_packet": 49,
    "system": 10000,
}

TYPE_NAMES = {
    1: "文本",
    3: "图片",
    34: "语音",
    42: "名片",
    43: "视频",
    47: "表情",
    48: "位置",
    49: "应用/文件",
    10000: "系统",
}

WEEKDAY_NAMES = ["周日", "周一", "周二", "周三", "周四", "周五", "周六"]
STOP_WORDS = {
    "这个", "那个", "然后", "就是", "还是", "可以", "不是", "没有", "一个",
    "我们", "你们", "他们", "什么", "怎么", "这么", "因为", "所以", "但是",
    "如果", "已经", "现在", "好的", "哈哈", "哈哈哈", "一下", "这里", "那里",
}

OFFLINE_MARKER = ".wetrace-offline-copy.json"
OFFLINE_UPDATING_MARKER = ".wetrace-offline-copy.updating.json"
OFFLINE_SOURCE = "wechat-cli/offline-db-copy"


class WetraceError(RuntimeError):
    pass


def emit(value: Any) -> None:
    print(json.dumps(value, ensure_ascii=False, indent=2))


def parse_time_range(value: str | None) -> tuple[str | None, str | None]:
    if not value:
        return None, None
    raw = value.strip()
    now = datetime.now()
    normalized = raw.lower().replace(" ", "_")
    relative = {
        "last_week": 7,
        "最近一周": 7,
        "近一周": 7,
        "last_month": 30,
        "最近一个月": 30,
        "近一个月": 30,
        "last_year": 365,
        "最近一年": 365,
        "近一年": 365,
    }
    if raw in relative or normalized in relative:
        days = relative.get(raw, relative.get(normalized))
        return (now - timedelta(days=days)).strftime("%Y-%m-%d %H:%M:%S"), None
    match = re.fullmatch(r"(?:last_|最近|近)(\d+)(?:_days|天)", normalized)
    if match:
        return (now - timedelta(days=int(match.group(1)))).strftime("%Y-%m-%d %H:%M:%S"), None
    if "~" in raw:
        start, end = (part.strip() for part in raw.split("~", 1))
        if re.fullmatch(r"\d{4}-\d{2}-\d{2}", end):
            end += " 23:59:59"
        return start or None, end or None
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", raw):
        return raw + " 00:00:00", raw + " 23:59:59"
    raise WetraceError(f"不支持的时间范围: {value}")


def safe_filename(value: str) -> str:
    cleaned = re.sub(r"[<>:\"/\\|?*\x00-\x1f]+", "_", value).strip(" ._")
    return cleaned[:80] or "wechat"


def validate_offline_db_root(value: str | None = None) -> tuple[Path, dict[str, Any]]:
    raw = value or os.environ.get("WETRACE_OFFLINE_DB_ROOT")
    if not raw:
        raise WetraceError(
            "Wetrace 只允许读取离线副本；请先运行 scripts/create-wetrace-offline-copy.ps1，"
            "再设置 WETRACE_OFFLINE_DB_ROOT"
        )
    root = Path(raw).expanduser().resolve()
    if not (root / "db_storage").is_dir():
        raise WetraceError(f"离线副本目录不包含 db_storage: {root}")
    updating_marker = root / OFFLINE_UPDATING_MARKER
    if updating_marker.is_file():
        raise WetraceError(
            f"离线副本正在更新或上次更新未完成；请在微信退出后重新运行增量更新脚本: {root}"
        )
    marker_path = root / OFFLINE_MARKER
    if not marker_path.is_file():
        raise WetraceError(f"离线副本缺少安全标记 {OFFLINE_MARKER}: {root}")
    try:
        marker = json.loads(marker_path.read_text(encoding="utf-8-sig"))
    except (OSError, json.JSONDecodeError) as exc:
        raise WetraceError(f"无法读取离线副本安全标记: {marker_path}") from exc
    if marker.get("format_version") != 1 or marker.get("status") != "complete":
        raise WetraceError(f"离线副本标记无效或复制未完成: {marker_path}")
    source_raw = marker.get("source_account_root")
    if not source_raw:
        raise WetraceError(f"离线副本标记缺少 source_account_root: {marker_path}")
    source = Path(source_raw).expanduser().resolve()
    if source == root:
        raise WetraceError("离线副本不能与微信在线数据目录相同")
    configured_live = os.environ.get("WECHAT_CLI_DB_ROOT")
    if configured_live and Path(configured_live).expanduser().resolve() == root:
        raise WetraceError("WETRACE_OFFLINE_DB_ROOT 与当前 WECHAT_CLI_DB_ROOT 相同，拒绝读取疑似在线目录")
    return root, marker


class WechatCLI:
    def __init__(self, executable: str | None = None):
        candidate = executable or os.environ.get("WECHAT_CLI_BIN") or shutil.which("wechat-cli")
        if not candidate:
            raise WetraceError("找不到 wechat-cli；请安装后重试或设置 WECHAT_CLI_BIN")
        self.executable = self._resolve_executable(candidate)
        self.offline_root, self.offline_marker = validate_offline_db_root()
        state_override = os.environ.get("WETRACE_OFFLINE_STATE_DIR")
        self.state_dir = (
            Path(state_override).expanduser().resolve()
            if state_override
            else self.offline_root / ".wechat-cli-state"
        )

    @staticmethod
    def _resolve_executable(candidate: str) -> str:
        path = Path(candidate).expanduser()
        if path.suffix.lower() not in {".cmd", ".bat"}:
            return str(path)
        try:
            shim = path.read_text(encoding="utf-8", errors="replace")
        except OSError as exc:
            raise WetraceError(f"无法读取 wechat-cli 命令转发文件: {path}") from exc
        match = re.search(r'^\s*"([^"]+\.exe)"\s+%\*\s*$', shim, flags=re.MULTILINE | re.IGNORECASE)
        if not match or not Path(match.group(1)).is_file():
            raise WetraceError("无法安全解析 wechat-cli.cmd；请将 WECHAT_CLI_BIN 指向 wechat-cli.exe")
        return match.group(1)

    def _run(self, argv: list[str]) -> dict[str, Any]:
        env = os.environ.copy()
        env.setdefault("PYTHONUTF8", "1")
        env["WECHAT_CLI_STRICT_READ_ONLY"] = "1"
        env["WECHAT_CLI_DB_ROOT"] = str(self.offline_root)
        env["WECHAT_CLI_STATE_DIR"] = str(self.state_dir)
        command = [self.executable, *argv]
        completed = subprocess.run(
            command,
            shell=False,
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            env=env,
            timeout=180,
        )
        stdout = completed.stdout.strip()
        if not stdout:
            raise WetraceError(completed.stderr.strip() or f"wechat-cli 退出码 {completed.returncode}")
        try:
            envelope = json.loads(stdout)
        except json.JSONDecodeError as exc:
            raise WetraceError(f"wechat-cli 返回了无效 JSON: {stdout[:300]}") from exc
        if not envelope.get("ok"):
            error = envelope.get("error") or {}
            raise WetraceError(error.get("message") or json.dumps(error, ensure_ascii=False))
        return envelope.get("data") or {}

    def call(self, tool: str, arguments: dict[str, Any] | None = None) -> dict[str, Any]:
        payload = json.dumps(arguments or {}, ensure_ascii=False, separators=(",", ":"))
        return self._run(["call-json", tool, payload])

    def status(self) -> dict[str, Any]:
        return self._run(["status"])


def normalize_message(row: dict[str, Any]) -> dict[str, Any]:
    identity = row.get("id") or {}
    voice = row.get("voice") or {}
    transcript = voice.get("transcript") or {}
    kind = row.get("kind") or row.get("kind_name") or "unknown"
    text = row.get("text") or row.get("content_summary") or ""
    return {
        "local_id": identity.get("local_id", row.get("local_id")),
        "server_id": identity.get("server_id_str", row.get("server_id_str")),
        "talker": identity.get("talker", row.get("talker")),
        "create_time": row.get("create_time"),
        "time": row.get("time") or row.get("create_time_human"),
        "sender": row.get("sender") or row.get("sender_display_name"),
        "sender_wxid": row.get("sender_wxid"),
        "is_from_me": bool(row.get("is_from_me")),
        "kind": kind,
        "type": KIND_TO_TYPE.get(kind, row.get("base_kind", 0)),
        "text": text,
        "voice_duration_ms": voice.get("duration_ms"),
        "voice_transcript_status": transcript.get("status") if voice else None,
        "warnings": row.get("warnings") or voice.get("warnings") or [],
    }


class WetraceLocal:
    def __init__(self, cli: WechatCLI):
        self.cli = cli

    def sessions(self, keyword: str | None, limit: int, offset: int, type_filter: str | None = None) -> dict[str, Any]:
        args: dict[str, Any] = {"limit": limit, "offset": offset}
        if keyword:
            args["keyword"] = keyword
        if type_filter:
            args["type_filter"] = type_filter
        data = self.cli.call("sessions", args)
        return {
            "source": OFFLINE_SOURCE,
            "scope": data.get("query", {}),
            "warnings": data.get("warnings", []),
            "sessions": data.get("sessions", []),
        }

    def all_sessions(self, max_sessions: int, type_filter: str = "private,group") -> tuple[list[dict[str, Any]], dict[str, Any]]:
        if max_sessions < 1:
            raise WetraceError("--session-limit 必须大于 0")
        offset = 0
        sessions: list[dict[str, Any]] = []
        has_more = False
        while len(sessions) < max_sessions:
            page = self.sessions(None, min(500, max_sessions - len(sessions)), offset, type_filter)
            rows = page["sessions"]
            sessions.extend(rows)
            offset += len(rows)
            has_more = bool(page["scope"].get("has_more"))
            if not rows or not has_more:
                break
        return sessions, {
            "session_limit": max_sessions,
            "scanned_sessions": len(sessions),
            "sessions_truncated": bool(has_more and len(sessions) >= max_sessions),
        }

    def contacts(self, keyword: str | None, limit: int, offset: int) -> dict[str, Any]:
        args: dict[str, Any] = {"limit": limit, "offset": offset}
        if keyword:
            args["keyword"] = keyword
        data = self.cli.call("contacts", args)
        return {
            "source": OFFLINE_SOURCE,
            "scope": data.get("query", {}),
            "contacts": data.get("contacts", []),
            "warnings": data.get("warnings", []),
        }

    def message_page(
        self,
        talker: str,
        limit: int,
        offset: int,
        time_range: str | None = None,
        sender: str | None = None,
        keyword: str | None = None,
        kind: str | None = None,
        order: str = "desc",
    ) -> dict[str, Any]:
        after, before = parse_time_range(time_range)
        args: dict[str, Any] = {
            "chat": talker,
            "limit": limit,
            "offset": offset,
            "order": order,
            "display_order": "query",
            "view": "agent",
            "include_media_paths": False,
        }
        for key, value in (("after", after), ("before", before), ("sender", sender), ("keyword", keyword), ("type", kind)):
            if value is not None:
                args[key] = value
        data = self.cli.call("messages", args)
        return {
            "source": OFFLINE_SOURCE,
            "scope": data.get("query", {}),
            "messages": [normalize_message(row) for row in data.get("messages", [])],
            "warnings": data.get("warnings", []),
        }

    def all_messages(
        self,
        talker: str,
        time_range: str | None,
        max_messages: int,
        sender: str | None = None,
        keyword: str | None = None,
        kind: str | None = None,
    ) -> tuple[list[dict[str, Any]], dict[str, Any]]:
        page_size = min(5000, max_messages)
        offset = 0
        messages: list[dict[str, Any]] = []
        last_scope: dict[str, Any] = {}
        while len(messages) < max_messages:
            page = self.message_page(
                talker=talker,
                limit=min(page_size, max_messages - len(messages)),
                offset=offset,
                time_range=time_range,
                sender=sender,
                keyword=keyword,
                kind=kind,
                order="desc",
            )
            rows = page["messages"]
            last_scope = page["scope"]
            messages.extend(rows)
            offset += len(rows)
            if not rows or not last_scope.get("has_more"):
                break
        messages.sort(key=lambda row: row.get("create_time") or 0)
        scope = {
            "talker": last_scope.get("talker", talker),
            "display_name": last_scope.get("display_name", talker),
            "time_range": time_range or "all",
            "returned": len(messages),
            "max_messages": max_messages,
            "truncated": bool(last_scope.get("has_more") and len(messages) >= max_messages),
        }
        return messages, scope

    def search(self, keyword: str, talker: str | None, sender: str | None, kind: str | None, time_range: str | None, limit: int, offset: int) -> dict[str, Any]:
        after, before = parse_time_range(time_range)
        args: dict[str, Any] = {"keyword": keyword, "limit": limit, "offset": offset}
        for key, value in (("chat", talker), ("sender", sender), ("type", kind), ("after", after), ("before", before)):
            if value is not None:
                args[key] = value
        data = self.cli.call("search", args)
        return {
            "source": OFFLINE_SOURCE,
            "scope": data.get("query", {}),
            "messages": data.get("messages", []),
            "warnings": data.get("warnings", []),
        }

    def context(self, talker: str, local_id: int, before: int, after: int) -> dict[str, Any]:
        data = self.cli.call("message_context", {
            "chat": talker,
            "local_id": local_id,
            "before_count": before,
            "after_count": after,
            "include_media_paths": False,
        })
        return {
            "source": OFFLINE_SOURCE,
            "scope": data.get("query", {}),
            "messages": [normalize_message(row) for row in data.get("messages", [])],
        }


def analyze(messages: list[dict[str, Any]], scope: dict[str, Any]) -> dict[str, Any]:
    hourly = Counter()
    weekday = Counter()
    daily = Counter()
    monthly = Counter()
    kinds = Counter()
    members = Counter()
    heatmap = [[0 for _ in range(24)] for _ in range(7)]
    from_me = 0
    voice_unavailable = 0

    for message in messages:
        timestamp = message.get("create_time")
        if not timestamp:
            continue
        dt = datetime.fromtimestamp(timestamp)
        sunday_index = (dt.weekday() + 1) % 7
        hourly[dt.hour] += 1
        weekday[sunday_index] += 1
        daily[dt.strftime("%Y-%m-%d")] += 1
        monthly[dt.strftime("%Y-%m")] += 1
        heatmap[sunday_index][dt.hour] += 1
        kinds[message.get("kind") or "unknown"] += 1
        members[message.get("sender") or message.get("sender_wxid") or "未知"] += 1
        from_me += int(bool(message.get("is_from_me")))
        if message.get("kind") == "voice" and message.get("voice_transcript_status") != "ok":
            voice_unavailable += 1

    total = len(messages)
    active_days = len(daily)
    peak_hour = max(hourly, key=hourly.get) if hourly else None
    peak_weekday = max(weekday, key=weekday.get) if weekday else None
    type_rows = []
    for kind, count in kinds.most_common():
        raw_type = KIND_TO_TYPE.get(kind, 0)
        type_rows.append({
            "kind": kind,
            "type": raw_type,
            "type_name": TYPE_NAMES.get(raw_type, kind),
            "count": count,
            "percentage": round(count * 100 / total, 2) if total else 0,
        })

    return {
        "source": OFFLINE_SOURCE,
        "scope": scope,
        "summary": {
            "total_messages": total,
            "active_days": active_days,
            "average_per_active_day": round(total / active_days, 2) if active_days else 0,
            "from_me": from_me,
            "from_others": total - from_me,
            "first_message_time": messages[0].get("time") if messages else None,
            "last_message_time": messages[-1].get("time") if messages else None,
            "peak_hour": peak_hour,
            "peak_weekday": WEEKDAY_NAMES[peak_weekday] if peak_weekday is not None else None,
            "voice_without_transcript": voice_unavailable,
        },
        "hourly": [{"hour": hour, "count": hourly[hour]} for hour in range(24)],
        "weekday": [{"weekday": day, "name": WEEKDAY_NAMES[day], "count": weekday[day]} for day in range(7)],
        "daily": [{"date": key, "count": daily[key]} for key in sorted(daily)],
        "monthly": [{"month": key, "count": monthly[key]} for key in sorted(monthly)],
        "type_distribution": type_rows,
        "member_activity": [
            {"name": name, "count": count, "percentage": round(count * 100 / total, 2) if total else 0}
            for name, count in members.most_common(50)
        ],
        "heatmap": heatmap,
    }


def analyze_global(
    local: WetraceLocal,
    analysis_type: str,
    time_range: str | None,
    session_limit: int,
    max_messages_per_chat: int,
) -> dict[str, Any]:
    if max_messages_per_chat < 1:
        raise WetraceError("--max-messages-per-chat 必须大于 0")
    sessions, session_scope = local.all_sessions(session_limit)
    all_messages: list[dict[str, Any]] = []
    session_rows: list[dict[str, Any]] = []
    truncated_chats: list[str] = []
    failed_chats: list[dict[str, str]] = []

    for session in sessions:
        talker = session.get("username")
        if not talker:
            continue
        try:
            messages, scope = local.all_messages(talker, time_range, max_messages_per_chat)
        except (WetraceError, subprocess.TimeoutExpired) as exc:
            failed_chats.append({
                "talker": talker,
                "display_name": session.get("display_name") or talker,
                "error": str(exc),
            })
            continue
        if scope["truncated"]:
            truncated_chats.append(talker)
        all_messages.extend(messages)
        report = analyze(messages, scope)
        summary = report["summary"]
        session_rows.append({
            "talker": talker,
            "display_name": session.get("display_name") or talker,
            "chat_type": session.get("chat_type"),
            "total_messages": summary["total_messages"],
            "from_me": summary["from_me"],
            "from_others": summary["from_others"],
            "active_days": summary["active_days"],
            "first_message_time": summary["first_message_time"],
            "last_message_time": summary["last_message_time"],
            "truncated": scope["truncated"],
        })

    session_rows.sort(key=lambda row: (-row["total_messages"], row["display_name"]))
    scope = {
        "time_range": time_range or "all",
        **session_scope,
        "max_messages_per_chat": max_messages_per_chat,
        "active_sessions": sum(row["total_messages"] > 0 for row in session_rows),
        "total_messages_scanned": len(all_messages),
        "truncated_chat_count": len(truncated_chats),
        "truncated_chats": truncated_chats,
        "failed_chat_count": len(failed_chats),
        "failed_chats": failed_chats,
        "truncated": bool(session_scope["sessions_truncated"] or truncated_chats or failed_chats),
    }
    if analysis_type == "top_contacts":
        data: Any = [row for row in session_rows if row["total_messages"] > 0]
    else:
        global_report = analyze(all_messages, scope)
        data = {
            "summary": global_report["summary"],
            "monthly": global_report["monthly"],
            "weekday": global_report["weekday"],
            "hourly": global_report["hourly"],
            "type_distribution": global_report["type_distribution"],
            "top_contacts": [row for row in session_rows if row["total_messages"] > 0][:50],
        }
    return {"source": OFFLINE_SOURCE, "scope": scope, "analysis": analysis_type, "data": data}


def repeat_analysis(messages: Iterable[dict[str, Any]], limit: int = 30) -> list[dict[str, Any]]:
    grouped: dict[str, dict[str, Any]] = {}
    for message in messages:
        if message.get("kind") != "text":
            continue
        text = re.sub(r"\s+", " ", (message.get("text") or "").strip())
        if not text or len(text) > 100:
            continue
        item = grouped.setdefault(text, {"content": text, "count": 0, "users": set()})
        item["count"] += 1
        item["users"].add(message.get("sender") or "未知")
    rows = [item for item in grouped.values() if item["count"] >= 2]
    rows.sort(key=lambda row: (-row["count"], row["content"]))
    return [{**row, "users": sorted(row["users"])} for row in rows[:limit]]


def wordcloud(messages: Iterable[dict[str, Any]], limit: int = 100) -> list[dict[str, Any]]:
    counts = Counter()
    for message in messages:
        if message.get("kind") != "text":
            continue
        text = message.get("text") or ""
        for token in re.findall(r"[A-Za-z][A-Za-z0-9_+-]{1,}|[\u4e00-\u9fff]{2,8}", text):
            token = token.lower()
            if token not in STOP_WORDS:
                counts[token] += 1
    return [{"word": token, "count": count} for token, count in counts.most_common(limit)]


def render_dashboard(report: dict[str, Any], output: Path) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    payload = (
        json.dumps(report, ensure_ascii=False)
        .replace("&", "\\u0026")
        .replace("<", "\\u003c")
        .replace(">", "\\u003e")
        .replace("\u2028", "\\u2028")
        .replace("\u2029", "\\u2029")
    )
    title = html.escape(report["scope"].get("display_name") or report["scope"].get("talker") or "微信会话")
    document = f"""<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>{title} - Wetrace</title><style>
:root{{--bg:#f5f7f8;--surface:#fff;--text:#172027;--muted:#64727d;--line:#dbe2e6;--green:#16865b;--blue:#2769a8;--amber:#b36a14;--red:#b8473f}}
*{{box-sizing:border-box}}body{{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 "Segoe UI","Microsoft YaHei",sans-serif;letter-spacing:0}}
header{{background:#15252d;color:#fff;padding:28px 32px}}header h1{{font-size:24px;margin:0 0 4px}}header p{{margin:0;color:#c7d2d7}}
main{{max-width:1180px;margin:auto;padding:24px}}.metrics{{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px;margin-bottom:24px}}
.metric{{background:var(--surface);border:1px solid var(--line);border-radius:6px;padding:16px}}.metric strong{{display:block;font-size:25px}}.metric span{{color:var(--muted)}}
section{{margin:0 0 28px}}h2{{font-size:17px;margin:0 0 12px}}.grid{{display:grid;grid-template-columns:1.2fr .8fr;gap:20px}}
.panel{{background:var(--surface);border:1px solid var(--line);border-radius:6px;padding:18px;min-width:0}}canvas{{width:100%;height:260px;display:block}}
.heatmap{{display:grid;grid-template-columns:48px repeat(24,minmax(13px,1fr));gap:3px;align-items:center;overflow:auto}}.heatmap .cell{{aspect-ratio:1;border-radius:2px;background:#edf1f2;min-width:13px}}.heatmap .label{{color:var(--muted);font-size:11px}}.heatmap .hour{{text-align:center;font-size:10px;color:var(--muted)}}
table{{width:100%;border-collapse:collapse}}th,td{{text-align:left;padding:9px;border-bottom:1px solid var(--line)}}th{{color:var(--muted);font-weight:600}}.bar{{height:7px;background:#e8edef;border-radius:3px;overflow:hidden}}.bar i{{display:block;height:100%;background:var(--green)}}
.note{{border-left:3px solid var(--amber);padding:8px 12px;background:#fff8ec;color:#67451c}}footer{{color:var(--muted);padding:8px 0 30px}}
@media(max-width:760px){{header{{padding:22px 18px}}main{{padding:16px}}.metrics{{grid-template-columns:repeat(2,1fr)}}.grid{{grid-template-columns:1fr}}}}
</style></head><body><header><h1>{title}</h1><p>Wetrace 本地微信分析报告</p></header><main>
<div class="metrics" id="metrics"></div><section class="grid"><div class="panel"><h2>每日消息趋势</h2><canvas id="trend"></canvas></div><div class="panel"><h2>消息类型</h2><div id="types"></div></div></section>
<section class="panel"><h2>每周时段热力图</h2><div class="heatmap" id="heatmap"></div></section>
<section class="grid"><div class="panel"><h2>成员活跃度</h2><table><thead><tr><th>成员</th><th>消息</th><th>占比</th></tr></thead><tbody id="members"></tbody></table></div><div class="panel"><h2>数据范围</h2><div id="scope"></div></div></section>
<footer>数据由 wechat-cli 从本机微信数据库只读获取。报告生成于 <span id="generated"></span>。</footer></main>
<script>const R={payload};const S=R.summary;const q=s=>document.querySelector(s);const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({{'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}}[c]));const metric=(v,l)=>`<div class="metric"><strong>${{esc(v)}}</strong><span>${{esc(l)}}</span></div>`;
q('#metrics').innerHTML=metric(S.total_messages,'消息总数')+metric(S.active_days,'活跃天数')+metric(S.average_per_active_day,'活跃日均消息')+metric((S.peak_hour??'-')+':00','峰值时段');
const types=R.type_distribution||[],maxType=Math.max(1,...types.map(x=>x.count));q('#types').innerHTML=types.map(x=>`<p>${{esc(x.type_name)}} <b>${{esc(x.count)}}</b></p><div class="bar"><i style="width:${{Math.max(0,Math.min(100,Number(x.count)/maxType*100))}}%"></i></div>`).join('');
q('#members').innerHTML=(R.member_activity||[]).slice(0,12).map(x=>`<tr><td>${{esc(x.name)}}</td><td>${{esc(x.count)}}</td><td>${{esc(x.percentage)}}%</td></tr>`).join('');
q('#scope').innerHTML=`<p><b>时间范围：</b>${{esc(R.scope.time_range)}}</p><p><b>读取消息：</b>${{esc(R.scope.returned)}}</p><p><b>达到上限：</b>${{R.scope.truncated?'是':'否'}}</p><p class="note">未落盘语音仅计入数量和时长，不分析内容：${{esc(S.voice_without_transcript)}} 条。</p>`;
const hm=q('#heatmap');hm.innerHTML='<span></span>'+Array.from({{length:24}},(_,h)=>`<span class="hour">${{h}}</span>`).join('');const maxH=Math.max(1,...R.heatmap.flat());R.heatmap.forEach((row,d)=>{{hm.innerHTML+=`<span class="label">${{['周日','周一','周二','周三','周四','周五','周六'][d]}}</span>`+row.map(v=>`<span class="cell" title="${{v}} 条" style="background:rgba(22,134,91,${{v?0.16+0.84*v/maxH:0.04}})"></span>`).join('')}});
const c=q('#trend'),ctx=c.getContext('2d'),D=R.daily||[];function draw(){{const dpr=devicePixelRatio||1,w=c.clientWidth,h=c.clientHeight;c.width=w*dpr;c.height=h*dpr;ctx.scale(dpr,dpr);ctx.clearRect(0,0,w,h);const vals=D.map(x=>x.count),mx=Math.max(1,...vals);ctx.strokeStyle='#2769a8';ctx.lineWidth=2;ctx.beginPath();D.forEach((x,i)=>{{const px=16+(w-32)*(D.length<2?0.5:i/(D.length-1)),py=h-20-(h-40)*x.count/mx;i?ctx.lineTo(px,py):ctx.moveTo(px,py)}});ctx.stroke()}}addEventListener('resize',draw);draw();q('#generated').textContent=new Date().toLocaleString();</script></body></html>"""
    output.write_text(document, encoding="utf-8")


def export_messages(messages: list[dict[str, Any]], report: dict[str, Any], output: Path, format_name: str) -> None:
    output.parent.mkdir(parents=True, exist_ok=True)
    if format_name == "json":
        output.write_text(json.dumps({"report": report, "messages": messages}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    elif format_name == "csv":
        with output.open("w", newline="", encoding="utf-8-sig") as handle:
            writer = csv.DictWriter(handle, fieldnames=["time", "sender", "is_from_me", "kind", "text", "local_id", "server_id"])
            writer.writeheader()
            writer.writerows({key: row.get(key) for key in writer.fieldnames} for row in messages)
    elif format_name == "txt":
        lines = [f"[{row.get('time')}] {row.get('sender')}: {row.get('text')}" for row in messages]
        output.write_text("\n".join(lines) + "\n", encoding="utf-8")
    elif format_name == "html":
        render_dashboard(report, output)
    else:
        raise WetraceError(f"脚本直接支持 html/csv/json/txt；{format_name} 请由对应文档工具基于 JSON 导出")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Wetrace 本地微信分析工具（wechat-cli 数据源）")
    parser.add_argument("--wechat-cli", help="wechat-cli 可执行文件路径")
    sub = parser.add_subparsers(dest="command", required=True)

    doctor = sub.add_parser("doctor", help="检查 wechat-cli 只读状态")
    doctor.set_defaults(action="doctor")

    sessions = sub.add_parser("sessions", help="查询会话")
    sessions.add_argument("--keyword")
    sessions.add_argument("--type-filter", choices=["private", "group", "private,group"])
    sessions.add_argument("--limit", type=int, default=50)
    sessions.add_argument("--offset", type=int, default=0)
    sessions.set_defaults(action="sessions")

    messages = sub.add_parser("messages", help="读取会话消息")
    messages.add_argument("--talker", required=True)
    messages.add_argument("--sender")
    messages.add_argument("--keyword")
    messages.add_argument("--kind")
    messages.add_argument("--time-range")
    messages.add_argument("--limit", type=int, default=50)
    messages.add_argument("--offset", type=int, default=0)
    messages.add_argument("--reverse", action="store_true")
    messages.set_defaults(action="messages")

    contacts = sub.add_parser("contacts", help="查询联系人")
    contacts.add_argument("--keyword")
    contacts.add_argument("--limit", type=int, default=50)
    contacts.add_argument("--offset", type=int, default=0)
    contacts.set_defaults(action="contacts")

    need = sub.add_parser("need-contact", help="查找长期未联系的私聊")
    need.add_argument("--days", type=int, default=30)
    need.add_argument("--limit", type=int, default=200)
    need.set_defaults(action="need-contact")

    chatrooms = sub.add_parser("chatrooms", help="查询群聊")
    chatrooms.add_argument("--keyword")
    chatrooms.add_argument("--limit", type=int, default=50)
    chatrooms.add_argument("--offset", type=int, default=0)
    chatrooms.set_defaults(action="chatrooms")

    chatroom = sub.add_parser("chatroom", help="查询群成员")
    chatroom.add_argument("id")
    chatroom.add_argument("--limit", type=int, default=500)
    chatroom.set_defaults(action="chatroom")

    search = sub.add_parser("search", help="全文搜索")
    search.add_argument("--keyword", required=True)
    search.add_argument("--talker")
    search.add_argument("--sender")
    search.add_argument("--kind")
    search.add_argument("--time-range")
    search.add_argument("--limit", type=int, default=50)
    search.add_argument("--offset", type=int, default=0)
    search.set_defaults(action="search")

    context = sub.add_parser("search-context", help="读取消息上下文")
    context.add_argument("--talker", required=True)
    context.add_argument("--local-id", "--seq", dest="local_id", type=int, required=True)
    context.add_argument("--before", type=int, default=10)
    context.add_argument("--after", type=int, default=10)
    context.set_defaults(action="context")

    analysis = sub.add_parser("analysis", help="会话或跨会话统计")
    analysis.add_argument("analysis_type", choices=["summary", "hourly", "daily", "weekday", "monthly", "type", "member", "repeat", "wordcloud", "top_contacts", "annual"])
    analysis.add_argument("session_id", nargs="?", help="单会话统计必填；top_contacts/annual 不填")
    analysis.add_argument("--time-range")
    analysis.add_argument("--max-messages", type=int, default=20000)
    analysis.add_argument("--session-limit", type=int, default=50, help="跨会话统计最多扫描的会话数")
    analysis.add_argument("--max-messages-per-chat", type=int, default=5000, help="跨会话统计每个会话最多读取的消息数")
    analysis.add_argument("--year", type=int, help="annual 的年份；默认当前年份")
    analysis.set_defaults(action="analysis")

    dashboard = sub.add_parser("dashboard", help="生成会话仪表板数据或 HTML")
    dashboard.add_argument("--talker", required=True)
    dashboard.add_argument("--time-range")
    dashboard.add_argument("--max-messages", type=int, default=20000)
    dashboard.add_argument("--html", nargs="?", const="auto")
    dashboard.set_defaults(action="dashboard")

    export = sub.add_parser("export", help="导出会话")
    export.add_argument("export_type", choices=["chat"])
    export.add_argument("--talker", required=True)
    export.add_argument("--name")
    export.add_argument("--time-range")
    export.add_argument("--format", choices=["html", "csv", "json", "txt"], default="html")
    export.add_argument("--output")
    export.add_argument("--max-messages", type=int, default=50000)
    export.set_defaults(action="export")
    return parser


def main() -> int:
    if hasattr(sys.stdout, "reconfigure"):
        sys.stdout.reconfigure(encoding="utf-8")
        sys.stderr.reconfigure(encoding="utf-8")
    args = build_parser().parse_args()

    try:
        local = WetraceLocal(WechatCLI(args.wechat_cli))
        if args.action == "doctor":
            status = local.cli.status()
            emit({
                "ok": True,
                "source": OFFLINE_SOURCE,
                "strict_read_only": True,
                "offline_db_root": str(local.cli.offline_root),
                "snapshot_created_at": local.cli.offline_marker.get("created_at"),
                "status": status.get("status", status),
            })
        elif args.action == "sessions":
            emit(local.sessions(args.keyword, args.limit, args.offset, args.type_filter))
        elif args.action == "messages":
            emit(local.message_page(args.talker, args.limit, args.offset, args.time_range, args.sender, args.keyword, args.kind, "asc" if args.reverse else "desc"))
        elif args.action == "contacts":
            emit(local.contacts(args.keyword, args.limit, args.offset))
        elif args.action == "need-contact":
            data = local.sessions(None, args.limit, 0, "private")
            cutoff = int((datetime.now() - timedelta(days=args.days)).timestamp())
            rows = [row for row in data["sessions"] if (row.get("last_timestamp") or 0) < cutoff]
            emit({"source": data["source"], "scope": {"days": args.days, "scanned": len(data["sessions"])}, "sessions": rows})
        elif args.action == "chatrooms":
            emit(local.sessions(args.keyword, args.limit, args.offset, "group"))
        elif args.action == "chatroom":
            data = local.cli.call("group_members", {"chat": args.id, "limit": args.limit})
            emit({"source": OFFLINE_SOURCE, "scope": data.get("query", {}), "members": data.get("members", [])})
        elif args.action == "search":
            emit(local.search(args.keyword, args.talker, args.sender, args.kind, args.time_range, args.limit, args.offset))
        elif args.action == "context":
            emit(local.context(args.talker, args.local_id, args.before, args.after))
        elif args.action == "analysis" and args.analysis_type in {"top_contacts", "annual"}:
            if args.session_id:
                raise WetraceError(f"{args.analysis_type} 是跨会话统计，不接受 session_id")
            if args.year and args.analysis_type != "annual":
                raise WetraceError("--year 仅用于 annual")
            if args.year and args.time_range:
                raise WetraceError("--year 和 --time-range 不能同时使用")
            time_range = args.time_range
            if args.analysis_type == "annual" and not time_range:
                year = args.year or datetime.now().year
                time_range = f"{year:04d}-01-01~{year:04d}-12-31"
            emit(analyze_global(local, args.analysis_type, time_range, args.session_limit, args.max_messages_per_chat))
        elif args.action in {"analysis", "dashboard", "export"}:
            if args.action == "analysis" and not args.session_id:
                raise WetraceError(f"analysis {args.analysis_type} 需要 session_id")
            talker = args.session_id if args.action == "analysis" else args.talker
            messages, scope = local.all_messages(talker, args.time_range, args.max_messages)
            report = analyze(messages, scope)
            if args.action == "analysis":
                mapping = {
                    "summary": report["summary"], "hourly": report["hourly"], "daily": report["daily"],
                    "weekday": report["weekday"], "monthly": report["monthly"], "type": report["type_distribution"],
                    "member": report["member_activity"], "repeat": repeat_analysis(messages), "wordcloud": wordcloud(messages),
                }
                emit({"source": report["source"], "scope": scope, "analysis": args.analysis_type, "data": mapping[args.analysis_type]})
            elif args.action == "dashboard":
                output = None
                if args.html:
                    export_root = Path.home() / "wetrace-exports"
                    output = export_root / f"dashboard_{safe_filename(scope.get('display_name') or talker)}_{datetime.now():%Y%m%d-%H%M%S}.html" if args.html == "auto" else Path(args.html).expanduser()
                    render_dashboard(report, output.resolve())
                emit({**report, "html_path": str(output.resolve()) if output else None})
            else:
                export_root = Path.home() / "wetrace-exports"
                extension = args.format
                output = Path(args.output).expanduser() if args.output else export_root / f"chat_{safe_filename(args.name or scope.get('display_name') or talker)}_{datetime.now():%Y%m%d-%H%M%S}.{extension}"
                export_messages(messages, report, output.resolve(), args.format)
                emit({"ok": True, "source": report["source"], "scope": scope, "path": str(output.resolve()), "format": args.format})
        return 0
    except (WetraceError, subprocess.TimeoutExpired) as exc:
        emit({"ok": False, "error": type(exc).__name__, "message": str(exc)})
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
