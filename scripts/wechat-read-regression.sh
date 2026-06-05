#!/usr/bin/env bash
set -euo pipefail

CLI="${WECHAT_CLI_BIN:-wechat-cli}"
CHAT="${WECHAT_READ_TEST_CHAT:-}"
KEYWORD="${WECHAT_READ_TEST_KEYWORD:-}"
OUT="${WECHAT_READ_TEST_OUT:-${TMPDIR:-/tmp}/wechat-read-regression-$(date +%Y%m%d-%H%M%S)}"
ALLOW_EMPTY_SEARCH="${WECHAT_READ_ALLOW_EMPTY_SEARCH:-0}"

mkdir -p -m 700 "$OUT"
chmod 700 "$OUT"
echo "[info] output: $OUT" >&2
echo "[warn] regression artifacts contain local WeChat data; do not upload or share this directory" >&2

on_exit() {
  local code=$?
  if [[ "$code" -ne 0 ]]; then
    echo "[FAIL] WeChat read regression failed with exit code $code" >&2
    echo "       output: $OUT" >&2
  fi
}
trap on_exit EXIT

py_bin() {
  if command -v python3 >/dev/null 2>&1; then
    printf '%s\n' python3
  elif command -v python >/dev/null 2>&1; then
    printf '%s\n' python
  else
    printf '%s\n' ""
  fi
}

PY="$(py_bin)"
if [[ -z "$PY" ]]; then
  echo "[FAIL] python3/python is required for JSON assertions" >&2
  exit 1
fi
if [[ -z "$CHAT" || -z "$KEYWORD" ]]; then
  echo "[FAIL] set WECHAT_READ_TEST_CHAT and WECHAT_READ_TEST_KEYWORD explicitly" >&2
  exit 1
fi

run_step() {
  local name="$1"
  shift
  echo "[run] $name: $*" >&2
  "$@" --pretty >"$OUT/$name.json"
  "$PY" - "$OUT/$name.json" <<'PY'
import json, sys
path = sys.argv[1]
with open(path, "r", encoding="utf-8") as f:
    doc = json.load(f)
if not doc.get("ok"):
    print("[FAIL] %s returned ok=false: %s" % (path, doc.get("error")), file=sys.stderr)
    sys.exit(1)
PY
}

json_value() {
  local path="$1"
  local expr="$2"
  "$PY" - "$path" "$expr" <<'PY'
import json, sys
path, expr = sys.argv[1], sys.argv[2]
with open(path, "r", encoding="utf-8") as f:
    doc = json.load(f)
cur = doc
for part in expr.split("."):
    if not part:
        continue
    if part.endswith("]"):
        key, idx = part[:-1].split("[", 1)
        cur = cur.get(key, [])
        cur = cur[int(idx)] if len(cur) > int(idx) else None
    elif isinstance(cur, dict):
        cur = cur.get(part)
    else:
        cur = None
    if cur is None:
        break
if cur is None:
    print("")
elif isinstance(cur, (dict, list)):
    print(json.dumps(cur, ensure_ascii=False))
else:
    print(cur)
PY
}

assert_nonempty_messages() {
  local path="$1"
  "$PY" - "$path" <<'PY'
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    doc = json.load(f)
messages = doc.get("data", {}).get("messages", [])
if not messages:
    print("[FAIL] timeline returned no messages", file=sys.stderr)
    sys.exit(1)
PY
}

run_step agent "$CLI" agent
run_step status "$CLI" status
run_step coverage "$CLI" coverage
run_step workflows "$CLI" workflows
run_step resolve_chat "$CLI" resolve-chat "$CHAT" --type-filter group
run_step sessions "$CLI" sessions --limit 10
run_step timeline "$CLI" timeline "$CHAT" --limit 20
assert_nonempty_messages "$OUT/timeline.json"

ANCHOR_LOCAL_ID="$(json_value "$OUT/timeline.json" "data.messages[0].id.local_id")"
if [[ -z "$ANCHOR_LOCAL_ID" ]]; then
  echo "[FAIL] first timeline row has no id.local_id; cannot verify context/anchor/tail workflows" >&2
  exit 1
fi
run_step context "$CLI" context "$CHAT" --local-id "$ANCHOR_LOCAL_ID" --before-count 5 --after-count 5
run_step timeline_before_message "$CLI" timeline "$CHAT" --before-message "$ANCHOR_LOCAL_ID" --limit 5
run_step timeline_after_message "$CLI" timeline "$CHAT" --after-message "$ANCHOR_LOCAL_ID" --limit 5
run_step tail_messages "$CLI" tail "$CHAT" --since-local-id "$ANCHOR_LOCAL_ID" --limit 5
run_step tail_sessions "$CLI" tail --mode sessions --limit 5

run_step search "$CLI" search "$KEYWORD" --in "$CHAT" --limit 10
run_step search_context "$CLI" search-context "$KEYWORD" --in "$CHAT" --limit 10 --context-limit 3 --before-count 5 --after-count 5
"$PY" - "$OUT/search_context.json" "$ALLOW_EMPTY_SEARCH" <<'PY'
import json, sys
path, allow_empty = sys.argv[1], sys.argv[2] == "1"
with open(path, "r", encoding="utf-8") as f:
    doc = json.load(f)
query = doc.get("data", {}).get("query", {})
if query.get("returned", 0) == 0:
    if allow_empty:
        sys.exit(0)
    print("[FAIL] search-context returned no hits; choose a WECHAT_READ_TEST_KEYWORD with at least one hit or set WECHAT_READ_ALLOW_EMPTY_SEARCH=1 for smoke mode", file=sys.stderr)
    sys.exit(1)
if query.get("contexts_returned", 0) <= 0:
    print("[FAIL] search-context returned hits but no expanded context", file=sys.stderr)
    sys.exit(1)
PY
SEARCH_LOCAL_ID="$(json_value "$OUT/search.json" "data.messages[0].id.local_id")"
SEARCH_TALKER="$(json_value "$OUT/search.json" "data.messages[0].chat.talker")"
if [[ -z "$SEARCH_LOCAL_ID" || -z "$SEARCH_TALKER" ]]; then
  if [[ "$ALLOW_EMPTY_SEARCH" == "1" ]]; then
    echo "[warn] search returned no expandable hit for keyword '$KEYWORD' (allowed by WECHAT_READ_ALLOW_EMPTY_SEARCH=1)" >&2
  else
    echo "[FAIL] search returned no expandable hit for keyword '$KEYWORD'" >&2
    exit 1
  fi
else
	run_step search_manual_context "$CLI" context "$SEARCH_TALKER" --local-id "$SEARCH_LOCAL_ID" --before-count 5 --after-count 5
fi

run_step media_images "$CLI" media "$CHAT" --type image --limit 10
run_step members "$CLI" members "$CHAT" --limit 100

EXPORT_PATH="$OUT/chat.jsonl"
echo "[run] export: $CLI export $CHAT --path $EXPORT_PATH --format jsonl" >&2
"$CLI" export "$CHAT" --path "$EXPORT_PATH" --format jsonl --pretty >"$OUT/export.json"
"$PY" - "$OUT/export.json" "$EXPORT_PATH" <<'PY'
import json, os, sys
with open(sys.argv[1], "r", encoding="utf-8") as f:
    doc = json.load(f)
if not doc.get("ok"):
    print("[FAIL] export returned ok=false: %s" % doc.get("error"), file=sys.stderr)
    sys.exit(1)
if not os.path.exists(sys.argv[2]) or os.path.getsize(sys.argv[2]) == 0:
    print("[FAIL] export output missing or empty: %s" % sys.argv[2], file=sys.stderr)
    sys.exit(1)
PY

echo "[OK] WeChat read regression passed"
echo "     output: $OUT"
