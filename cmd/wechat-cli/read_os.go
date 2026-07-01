package main

import (
	"fmt"
	"runtime"
	"time"

	"github.com/r266-tech/wechat-cli/internal/config"
	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

func (s *server) toolReadOS(a map[string]any) (any, error) {
	mode := getStr(a, "mode")
	if mode == "" {
		mode = "overview"
	}
	includeStatus := getBoolDefault(a, "include_status", true)
	includeDebug := includeDebugOutput(a)
	out := map[string]any{
		"identity": map[string]any{
			"name":        appName,
			"version":     appVersion,
			"goal":        "read-only WeChat data OS for agents",
			"contract":    "WeChat read only; no sending, no UI control, no WeChat data mutation. Set WECHAT_CLI_STRICT_READ_ONLY=1 to also disable local support-file writes.",
			"data_policy": "message bodies are live-read from local WeChat DBs; metadata cache is contacts/sessions only",
		},
	}
	switch mode {
	case "overview":
		if includeStatus {
			out["status"] = s.readOSStatus(includeDebug)
		}
		out["entrypoints"] = readOSEntrypoints()
		out["workflows"] = readOSWorkflows()
		out["coverage"] = readOSCoverageMatrix()
		out["quality_gates"] = readOSQualityGates()
	case "coverage":
		out["coverage"] = readOSCoverageMatrix()
	case "workflows":
		out["entrypoints"] = readOSEntrypoints()
		out["workflows"] = readOSWorkflows()
	case "status":
		out["status"] = s.readOSStatus(includeDebug)
	default:
		return nil, errInvalidReadOSMode(mode)
	}
	return out, nil
}

func errInvalidReadOSMode(mode string) error {
	return fmt.Errorf("invalid mode=%q: must be overview / coverage / workflows / status", mode)
}

func (s *server) readOSStatus(includeDebug bool) map[string]any {
	capabilities := readOSCapabilities(false, false, false)
	status := map[string]any{
		"platform": map[string]any{
			"os":   runtime.GOOS,
			"arch": runtime.GOARCH,
		},
		"mode": map[string]any{
			"strict_read_only": strictReadOnlyMode(),
		},
		"capabilities": capabilities,
		"checked_at":   time.Now().Format(time.RFC3339),
	}
	readiness := "ready"
	dbReady := false
	cacheIndexExists := false
	wcdbAvailable := false
	var warnings []string
	var degradedBy []string
	blockedBy := ""
	nextAction := ""
	var suggestedCommands []string
	setBlocked := func(by, action string, commands ...string) {
		readiness = "blocked"
		if blockedBy != "" {
			return
		}
		blockedBy = by
		nextAction = action
		suggestedCommands = append(suggestedCommands, commands...)
	}
	cfgPath, cfgPathErr := config.Path()
	cfg, cfgErr := s.activeConfigNoSetup()
	if cfgErr != nil {
		status["config_error"] = cfgErr.Error()
		setBlocked("config_error", "Run first key setup, then rerun status.", readOSBootstrapCommand(), appName+" status --pretty")
	} else {
		status["account"] = compactMap(map[string]any{
			"wxid":               cfg.Wxid,
			"db_root_configured": cfg.DBRoot != "",
			"schema2_key_count":  len(cfg.Keys),
			"schema2_ready":      cfg.Ready(),
			"image_key_ready":    cfg.ImageKey != "" || cfg.ImageXORKey != nil,
		})
		if includeDebug {
			status["account_debug"] = compactMap(map[string]any{
				"db_root": cfg.DBRoot,
			})
		}
		if cfg.DBRoot == "" {
			setBlocked("db_root_missing", "Configure WeChat DB root through first key setup.", readOSBootstrapCommand(), appName+" status --pretty")
		} else if !cfg.Ready() {
			setBlocked("key_config_missing", "Prepare local WeChat DB keys, then rerun the read command.", readOSBootstrapCommand(), appName+" status --pretty")
		} else {
			dbReady = true
		}
		if paths, err := cachePathsFor(cfg); err == nil {
			cacheIndexExists = fileExists(paths.IndexPath)
			cache := map[string]any{
				"index_exists": cacheIndexExists,
			}
			if includeDebug {
				cache["root_dir"] = paths.RootDir
				cache["index_path"] = paths.IndexPath
			}
			if cfg.DBRoot != "" {
				if dbs, err := listSourceDBs(cfg, paths); err == nil {
					cache["source_db_count"] = len(dbs)
				}
				if wcdbPath, err := findWCDB(); err == nil {
					if err := wcdb.Bootstrap(wcdbPath); err == nil {
						if fresh, reason, err := s.cacheFreshness(cfg, paths); err == nil && !fresh && reason != "" {
							cache["metadata_stale_reason"] = metadataStatusReason(reason)
							if staleCacheIndexUsable(reason) {
								cache["degraded"] = true
								degradedBy = appendUniqueStrings(degradedBy, "metadata_cache_stale")
								warnings = appendUniqueStrings(warnings, "metadata_cache_degraded")
								if readiness == "ready" {
									readiness = "degraded"
								}
							} else {
								cache["blocked_reason"] = metadataStatusReason(reason)
								cache["degraded"] = true
								degradedBy = appendUniqueStrings(degradedBy, "metadata_cache_degraded")
								warnings = appendUniqueStrings(warnings, "metadata_cache_degraded")
								if readiness == "ready" {
									readiness = "degraded"
								}
							}
						}
					}
				}
			}
			status["metadata_cache"] = cache
		}
	}
	if cfgPathErr == nil && includeDebug {
		status["config_path"] = cfgPath
	}
	if wcdbPath, err := findWCDB(); err == nil {
		wcdbAvailable = true
		status["wcdb"] = compactMap(map[string]any{
			"available": true,
			"path":      debugOnlyString(wcdbPath, includeDebug),
		})
	} else {
		status["wcdb"] = map[string]any{
			"available": false,
			"error":     err.Error(),
		}
		setBlocked("wcdb_missing", "Use an installed release or point WECHAT_CLI_WCDB_DYLIB/WECHAT_CLI_WCDB_LIB at the bundled WCDB library.", appName+" status --pretty")
	}
	capabilities = readOSCapabilities(dbReady, wcdbAvailable, cacheIndexExists)
	status["capabilities"] = capabilities
	status["live_read_ok"] = dbReady && wcdbAvailable
	status["readiness"] = readiness
	if len(degradedBy) > 0 {
		status["degraded_by"] = degradedBy
	}
	if len(warnings) > 0 {
		status["warnings"] = warnings
	}
	if blockedBy != "" {
		status["blocked_by"] = blockedBy
		status["next_action"] = nextAction
		status["suggested_commands"] = suggestedCommands
	}
	return status
}

func readOSCapabilities(dbReady, wcdbAvailable, cacheIndexExists bool) map[string]bool {
	liveRead := dbReady && wcdbAvailable
	return map[string]bool{
		"search":          liveRead,
		"sessions":        liveRead,
		"timeline":        liveRead,
		"context":         liveRead,
		"tail":            liveRead,
		"media":           liveRead,
		"voice_asr":       asrReadyBool(asrStatusData()["wechat_voice_ready"]),
		"name_resolution": cacheIndexExists,
	}
}

func readOSBootstrapCommand() string {
	if runtime.GOOS == "windows" {
		return appName + ".exe cache refresh --force"
	}
	return "~/.local/share/" + appName + "/wxkey bootstrap"
}

func debugOnlyString(s string, includeDebug bool) string {
	if includeDebug {
		return s
	}
	return ""
}

func readOSEntrypoints() []map[string]any {
	return []map[string]any{
		{"command": "agent", "tool": "read_os", "use": "agent-first entrypoint for capability matrix, workflows, install/readiness status"},
		{"command": "status", "tool": "read_os", "use": "quick local readiness check"},
		{"command": "coverage", "tool": "read_os", "use": "coverage matrix only"},
		{"command": "workflows", "tool": "read_os", "use": "command recipes only"},
		{"command": "asr status", "tool": "asr", "use": "check optional local voice transcription runtime"},
		{"command": "asr setup", "tool": "asr", "use": "install optional faster-whisper and SILK decode support in a local venv", "local_file_write": true},
		{"command": "sessions", "tool": "sessions", "use": "list recent chats and unread counts"},
		{"command": "resolve-chat", "tool": "resolve_chat", "use": "turn a human name/group name into a stable talker id"},
		{"command": "timeline", "tool": "chat_timeline", "use": "read a chat window in display order; page with offset/next_offset"},
		{"command": "context", "tool": "message_context", "use": "expand before/after messages around a known local_id or server_id"},
		{"command": "tail", "tool": "read_events", "use": "read-only event tail for new chat messages or session/unread changes"},
		{"command": "search", "tool": "search", "use": "global or scoped keyword search over WeChat FTS"},
		{"command": "search-context", "tool": "search_with_context", "use": "search keyword hits and return surrounding context in one call"},
		{"command": "media", "tool": "media_resources", "use": "resolve images, videos, and files from timeline/search ids"},
		{"command": "members", "tool": "group_members", "use": "read group members and display names"},
		{"command": "export", "tool": "export_messages", "use": "explicit local file export for large single-chat reads", "local_file_write": true},
	}
}

func readOSWorkflows() []map[string]any {
	return []map[string]any{
		{
			"name": "read_recent_chat",
			"commands": []string{
				`wechat-cli resolve-chat "$CHAT"`,
				`wechat-cli timeline "$CHAT" --limit 50 --display-order asc`,
			},
		},
		{
			"name": "page_full_chat",
			"commands": []string{
				`wechat-cli timeline "$CHAT" --limit 200 --offset 0`,
				`repeat with data.query.next_offset while data.query.has_more`,
			},
		},
		{
			"name":             "export_large_chat",
			"local_file_write": true,
			"commands": []string{
				`wechat-cli export "$CHAT" --path /tmp/chat.jsonl --format jsonl`,
			},
		},
		{
			"name": "search_then_expand",
			"commands": []string{
				`wechat-cli search "$KEYWORD" --limit 20`,
				`wechat-cli context "$CHAT" --local-id <result.id.local_id> --before-count 20 --after-count 20`,
				`wechat-cli search-context "$KEYWORD" --in "$CHAT" --context-limit 3`,
			},
		},
		{
			"name": "observe_incremental",
			"commands": []string{
				`wechat-cli tail "$CHAT" --since-local-id <last_seen_local_id> --jsonl`,
				`wechat-cli watch --mode sessions --cursor <last_session_cursor> --jsonl`,
			},
		},
		{
			"name": "inspect_media",
			"commands": []string{
				`wechat-cli timeline "$CHAT" --type image --limit 20`,
				`wechat-cli media "$CHAT" --local-id <message.id.local_id> --include-debug`,
			},
		},
		{
			"name": "group_read",
			"commands": []string{
				`wechat-cli members "群名" --limit 200`,
				`wechat-cli search "$KEYWORD" --in "$CHAT" --sender "$SENDER"`,
			},
		},
	}
}

func readOSQualityGates() []map[string]any {
	return []map[string]any{
		qualityGate("install_smoke", `wechat-cli sessions --limit 5 --pretty`, "ok=true and recent sessions returned"),
		qualityGate("agent_entrypoint", `wechat-cli agent --pretty`, "coverage/workflows/status visible from one command"),
		qualityGate("chat_navigation", `wechat-cli timeline "$CHAT" --limit 20`, "query.has_more/next_offset usable for paging"),
		qualityGate("around_context", `wechat-cli context "$CHAT" --local-id "$LOCAL_ID" --before-count 5 --after-count 5`, "anchor plus surrounding messages returned in chronological order"),
		qualityGate("anchor_timeline", `wechat-cli timeline "$CHAT" --before-message "$LOCAL_ID" --limit 10`, "message page can use an anchor instead of only offset"),
		qualityGate("event_tail", `wechat-cli tail "$CHAT" --since-local-id "$LOCAL_ID" --jsonl`, "newer messages, if any, emit as JSONL events with timeline-shaped event.message"),
		qualityGate("search_expand", `wechat-cli search "$KEYWORD" --in "$CHAT" --limit 5`, "search rows carry local_id/talker for context expansion"),
		qualityGate("search_with_context", `wechat-cli search-context "$KEYWORD" --in "$CHAT" --context-limit 3`, "keyword hits include before/anchor/after context windows"),
		qualityGate("media_readability", `wechat-cli media "$CHAT" --type image --limit 10`, "readable local paths or actionable warnings, never opaque .dat as a fake image path"),
		qualityGate("group_members", `wechat-cli members "$CHAT" --limit 50`, "group member display names resolve"),
		qualityGate("export_shape", `wechat-cli export "$CHAT" --path "$TMP/chat.jsonl" --format jsonl`, "agent-view JSONL rows match timeline message shape"),
	}
}

func qualityGate(name, command, pass string) map[string]any {
	out := map[string]any{
		"name":    name,
		"gate":    name,
		"command": command,
		"pass":    pass,
	}
	if name == "export_shape" {
		out["local_file_write"] = true
	}
	return out
}

func readOSCoverageMatrix() []map[string]any {
	return []map[string]any{
		coverageRow("text", "supported", "timeline/search/export", "text, sender, time, ids", "raw message_content, parser fields"),
		coverageRow("image", "supported_with_diagnostics", "timeline/media", "best readable local image path, warnings", "candidate paths, .dat decode status, image-key refresh diagnostics"),
		coverageRow("video", "supported_with_diagnostics", "timeline/media", "readable mp4/thumb paths when local cache exists", "resource ids, variants, raw local path details"),
		coverageRow("voice", "supported_with_diagnostics", "timeline", "voice payload and local ASR transcript when available", "raw SILK/cache path/transcription diagnostics"),
		coverageRow("file", "supported", "timeline/media", "file name, size, readable local path when present", "appattach/raw XML/resource details"),
		coverageRow("link", "supported", "timeline/search", "title, url, source, description/thumb when present", "raw appmsg XML"),
		coverageRow("quote", "supported", "timeline/context", "reply text plus referenced message summary/payload", "nested refermsg XML and parsed fields"),
		coverageRow("forward_chat", "supported_with_diagnostics", "timeline/media", "recursive forward items, text/link/file/image refs when resolvable", "source ids, nested raw items, unresolved media warnings"),
		coverageRow("location", "supported", "timeline/search", "label/poi/lat/lon/scale", "raw location XML"),
		coverageRow("transfer", "supported", "timeline/transfers", "amount, description, payer/receiver, message id", "pay XML/native fields"),
		coverageRow("red_packet", "supported", "timeline/red-packets", "wishing, scene text, sender/session ids", "native URL/raw XML"),
		coverageRow("solitaire", "supported", "timeline/search", "display-ready app payload summary", "raw appmsg XML"),
		coverageRow("miniprogram", "supported", "timeline/search", "title, app id/source, URL/thumb when present", "raw appmsg XML"),
		coverageRow("card", "supported", "timeline/search", "shared contact/card display payload", "raw card XML"),
		coverageRow("sticker", "supported_with_diagnostics", "timeline/media", "sticker summary and media hints when present", "raw emoji XML/media candidates"),
		coverageRow("system_recall", "supported", "timeline/search", "system/recall text as visible in WeChat", "raw system content"),
		coverageRow("group_members", "supported", "members", "username, display name, owner/friend flags", "member stats when requested"),
		coverageRow("moments", "supported", "sns-feed/sns-search/sns-notifications", "posts, comments, likes, media metadata", "raw SNS XML/media keys"),
		coverageRow("favorites", "supported", "favorites", "favorite type, source, title/description/url", "raw favorite XML"),
		coverageRow("chatroom_announcements", "supported", "announcements", "announcement, editor, publish time", "raw contact DB fields"),
	}
}

func coverageRow(kind, status, entrypoint, agentDefault, debug string) map[string]any {
	return map[string]any{
		"kind":          kind,
		"status":        status,
		"entrypoint":    entrypoint,
		"agent_default": agentDefault,
		"debug":         debug,
	}
}
