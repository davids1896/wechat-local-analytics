package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

type cliOptions struct {
	Pretty         bool
	StrictReadOnly bool
}

type cliSuccessEnvelope struct {
	OK      bool   `json:"ok"`
	Tool    string `json:"tool,omitempty"`
	Command string `json:"command,omitempty"`
	Data    any    `json:"data"`
}

type cliErrorEnvelope struct {
	OK    bool     `json:"ok"`
	Error cliError `json:"error"`
}

type cliError struct {
	Code              string   `json:"code"`
	Message           string   `json:"message"`
	Tool              string   `json:"tool,omitempty"`
	Command           string   `json:"command,omitempty"`
	NextAction        string   `json:"next_action,omitempty"`
	SuggestedCommands []string `json:"suggested_commands,omitempty"`
}

type cliCommandSpec struct {
	Command     string   `json:"command"`
	Aliases     []string `json:"aliases,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Usage       string   `json:"usage"`
	Positional  string   `json:"positional,omitempty"`
	Description string   `json:"description,omitempty"`
	Examples    []string `json:"examples,omitempty"`
}

func (s cliCommandSpec) MarshalJSON() ([]byte, error) {
	type alias cliCommandSpec
	return json.Marshal(struct {
		Name string `json:"name"`
		alias
	}{
		Name:  s.Command,
		alias: alias(s),
	})
}

var cliCommandSpecs = []cliCommandSpec{
	{Command: "tools", Aliases: []string{"list-tools", "list_tools"}, Usage: appName + " tools [--profile assistant|maintenance|all]", Description: "List tool schemas. Defaults to the high-signal assistant surface; use --profile all for every compatibility/maintenance tool.", Examples: []string{appName + " tools", appName + " tools --profile all"}},
	{Command: "agent", Aliases: []string{"read-os", "read_os", "os"}, Tool: "read_os", Usage: appName + " agent [--mode overview|coverage|workflows|status]", Description: "Agent-first WeChat Read OS entrypoint: capability map, workflows, coverage matrix, and readiness status.", Examples: []string{appName + " agent --pretty", appName + " agent --mode coverage --pretty"}},
	{Command: "status", Aliases: []string{"doctor-lite", "doctor_lite"}, Tool: "read_os", Usage: appName + " status", Description: "Show local readiness status without reading large message bodies.", Examples: []string{appName + " status --pretty"}},
	{Command: "coverage", Tool: "read_os", Usage: appName + " coverage", Description: "Show the WeChat Read OS coverage matrix.", Examples: []string{appName + " coverage --pretty"}},
	{Command: "workflows", Aliases: []string{"playbook", "recipes"}, Tool: "read_os", Usage: appName + " workflows", Description: "Show agent workflows and command recipes.", Examples: []string{appName + " workflows --pretty"}},
	{Command: "update", Aliases: []string{"upgrade", "self-update", "self_update"}, Usage: appName + " update [--dry-run] [--tag vX.Y.Z]", Description: "Update an installed release to the latest GitHub release.", Examples: []string{appName + " update", appName + " update --dry-run"}},
	{Command: "asr", Aliases: []string{"voice-asr", "voice_asr"}, Usage: appName + " asr <status|setup>", Description: "Check or install the optional local voice transcription runtime.", Examples: []string{appName + " asr status --pretty", appName + " asr setup --dry-run --pretty", appName + " asr setup --model large-v3"}},
	{Command: "asr status", Usage: appName + " asr status", Description: "Show local voice ASR readiness without writing files.", Examples: []string{appName + " asr status --pretty"}},
	{Command: "asr setup", Usage: appName + " asr setup [--dry-run] [--model large-v3] [--skip-model-download]", Description: "Create the wechat-cli ASR virtualenv, install faster-whisper, and optionally preload the default model.", Examples: []string{appName + " asr setup --dry-run --pretty", appName + " asr setup --model large-v3", appName + " asr setup --skip-model-download"}},
	{Command: "companion", Aliases: []string{"sidecar"}, Usage: appName + " companion [--addr 127.0.0.1:18789] [--desktop=false|--browser|--open=false]", Description: "Start the read-only local WeChat Assistant V1 sidecar GUI. On macOS it opens a native WebKit desktop window by default.", Examples: []string{appName + " companion", appName + " companion --browser", appName + " companion --addr 127.0.0.1:18789 --open=false"}},
	{Command: "call", Usage: appName + " call <command-or-tool> [--key value ...]", Description: "Call a command/tool with key/value CLI arguments.", Examples: []string{appName + ` call timeline --chat "$CHAT" --limit 20`}},
	{Command: "call-json", Aliases: []string{"call_json"}, Usage: appName + " call-json <command-or-tool> '<json args>'", Description: "Call a command/tool with a JSON argument object from argv or stdin.", Examples: []string{appName + ` call-json timeline "{\"chat\":\"$CHAT\",\"limit\":20}"`, appName + ` call-json search-context "{\"keyword\":\"$KEYWORD\",\"limit\":5}"`}},
	{Command: "tool-schema", Aliases: []string{"describe", "describe-tool", "tool_schema"}, Usage: appName + " tool-schema <command-or-tool>", Description: "Return one command/tool schema.", Examples: []string{appName + " tool-schema timeline"}},
	{Command: "cache", Usage: appName + " cache <status|refresh|rebuild>", Description: "Metadata cache subcommands.", Examples: []string{appName + " cache status"}},
	{Command: "cache status", Tool: "cache_status", Usage: appName + " cache status", Examples: []string{appName + " cache status"}},
	{Command: "cache refresh", Tool: "cache_refresh", Usage: appName + " cache refresh [--force] [--background]", Examples: []string{appName + " cache refresh --force"}},
	{Command: "cache rebuild", Tool: "cache_rebuild", Usage: appName + " cache rebuild", Examples: []string{appName + " cache rebuild"}},
	{Command: "sessions", Tool: "sessions", Usage: appName + " sessions [--limit 20] [--type-filter private,group]", Examples: []string{appName + " sessions --limit 20", appName + ` sessions --type-filter group --keyword "$KEYWORD"`}},
	{Command: "resolve-chat", Aliases: []string{"resolve_chat"}, Tool: "resolve_chat", Usage: appName + " resolve-chat <chat> [--type-filter private]", Positional: "query", Examples: []string{appName + ` resolve-chat "$CHAT" --type-filter group`}},
	{Command: "contacts", Tool: "contacts", Usage: appName + " contacts [--keyword 李]", Examples: []string{appName + " contacts --keyword 李 --limit 20"}},
	{Command: "history", Aliases: []string{"messages"}, Tool: "messages", Usage: appName + " history <chat> [--limit 50] [--after 2026-05-11] [--view agent]", Positional: "chat", Examples: []string{appName + ` history "$CHAT" --view agent --limit 50`}},
	{Command: "timeline", Aliases: []string{"chat-timeline", "chat_timeline", "conversation-view", "conversation_view"}, Tool: "chat_timeline", Usage: appName + " timeline <chat> [--limit 10] [--display-order asc]", Positional: "chat", Examples: []string{appName + ` timeline "$CHAT" --limit 20`, appName + ` timeline "$CHAT" --limit 20 --offset 20`}},
	{Command: "context", Aliases: []string{"message-context", "message_context", "around"}, Tool: "message_context", Usage: appName + " context <chat> --local-id 123 [--before-count 20] [--after-count 20]", Positional: "chat", Examples: []string{appName + ` context "$CHAT" --local-id 123 --before-count 10 --after-count 10`, appName + ` context "$CHAT" --server-id-str 9876543210 --pretty`}},
	{Command: "tail", Aliases: []string{"watch", "observe", "events"}, Tool: "read_events", Usage: appName + " tail [chat] [--since-local-id 123] [--jsonl] [--follow]", Positional: "chat", Description: "Read-only event tail for agents. Normal mode returns the standard envelope; --jsonl/--follow emit newline-delimited event objects.", Examples: []string{appName + ` tail "$CHAT" --since-local-id 123`, appName + ` tail "$CHAT" --since-local-id 123 --jsonl`, appName + " watch --mode sessions --jsonl --follow"}},
	{Command: "media", Aliases: []string{"media-resources", "media_resources", "attachments"}, Tool: "media_resources", Usage: appName + " media <chat> [--local-id 123] [--type image|video|file]", Positional: "chat", Examples: []string{appName + ` media "$CHAT" --local-id 10`, appName + ` media "$CHAT" --type image --limit 20`}},
	{Command: "search", Tool: "search", Usage: appName + " search <keyword> [--in <chat>] [--after 2026-01-01] [--type text]", Positional: "keyword", Examples: []string{appName + ` search "$KEYWORD" --in "$CHAT" --limit 10`}},
	{Command: "search-context", Aliases: []string{"search_context", "search-with-context", "search_with_context"}, Tool: "search_with_context", Usage: appName + " search-context <keyword> [--in <chat>] [--before-count 5] [--after-count 5]", Positional: "keyword", Examples: []string{appName + ` search-context "$KEYWORD" --in "$CHAT" --limit 5`, appName + ` search-with-context "$KEYWORD" --context-limit 3 --before-count 10 --after-count 10`}},
	{Command: "members", Aliases: []string{"group-members", "group_members"}, Tool: "group_members", Usage: appName + " members <group>", Positional: "chat", Examples: []string{appName + ` members "$CHAT" --limit 50`}},
	{Command: "unread", Tool: "unread", Usage: appName + " unread [--limit 50]", Examples: []string{appName + " unread --limit 50"}},
	{Command: "stats", Tool: "stats", Usage: appName + " stats", Examples: []string{appName + " stats"}},
	{Command: "favorites", Tool: "favorites", Usage: appName + " favorites [--limit 20]", Examples: []string{appName + " favorites --limit 20"}},
	{Command: "red-packets", Aliases: []string{"red_packets"}, Tool: "red_packets", Usage: appName + " red-packets [--limit 20]", Examples: []string{appName + " red-packets --limit 20"}},
	{Command: "transfers", Tool: "transfers", Usage: appName + " transfers [--limit 20]", Examples: []string{appName + " transfers --limit 20"}},
	{Command: "sns-feed", Aliases: []string{"sns", "sns_feed"}, Tool: "sns_feed", Usage: appName + " sns-feed [--limit 20]", Examples: []string{appName + " sns-feed --limit 20"}},
	{Command: "sns-search", Aliases: []string{"sns_search"}, Tool: "sns_search", Usage: appName + " sns-search <keyword>", Positional: "keyword", Examples: []string{appName + ` sns-search "$KEYWORD" --limit 20`}},
	{Command: "sns-notifications", Aliases: []string{"sns_notifications"}, Tool: "sns_notifications", Usage: appName + " sns-notifications [--include-read]", Examples: []string{appName + " sns-notifications --include-read"}},
	{Command: "schema", Tool: "schema", Usage: appName + " schema [--subdir session] [--file session.db]", Examples: []string{appName + " schema --subdir session --file session.db"}},
	{Command: "sql", Tool: "sql", Usage: appName + " sql <query>", Positional: "query", Examples: []string{appName + " sql 'select count(*) as n from Session' --subdir session --file session.db"}},
	{Command: "announcements", Aliases: []string{"chatroom-announcements", "chatroom_announcements"}, Tool: "chatroom_announcements", Usage: appName + " announcements [chatroom-id]", Positional: "chatroom_id", Examples: []string{appName + ` announcements "$CHAT" --limit 20`}},
	{Command: "forward-history", Aliases: []string{"forward_history"}, Tool: "forward_history", Usage: appName + " forward-history [--limit 20]", Examples: []string{appName + " forward-history --limit 20"}},
	{Command: "export", Aliases: []string{"export-messages", "export_messages"}, Tool: "export_messages", Usage: appName + " export <chat> --path /tmp/messages.jsonl [--format jsonl|markdown|html] [--view agent|raw]", Positional: "chat", Examples: []string{appName + ` export "$CHAT" --path /tmp/chat.jsonl --format jsonl`, appName + ` export "$CHAT" --path /tmp/chat.raw.jsonl --view raw`}},
}

func maybeRunCLI(args []string) bool {
	opts, args, err := parseGlobalCLIOptions(args)
	if err != nil {
		exitCLIError(opts, 2, "invalid_global_argument", err.Error(), "", "")
	}
	if len(args) == 0 {
		runCLIHelp("", opts)
		return true
	}
	if hasHelpFlag(args[1:]) {
		runCLIHelp(helpTargetForCommand(args), opts)
		return true
	}
	switch args[0] {
	case "-h", "--help", "help":
		runCLIHelp(strings.Join(args[1:], " "), opts)
		return true
	case "-v", "--version", "version":
		writeCLISuccess("version", "version", map[string]any{
			"name":    appName,
			"version": appVersion,
		}, opts)
		return true
	case "tools", "list-tools", "list_tools":
		runToolsCLI(args[1:], opts)
		return true
	case "agent", "read-os", "read_os", "os":
		runToolCLI("read_os", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "status", "doctor-lite", "doctor_lite":
		flags := parseKVFlags(args[1:])
		if flags["mode"] == nil {
			flags["mode"] = "status"
		}
		runToolCLI("read_os", flags, opts, args[0])
		return true
	case "coverage":
		flags := parseKVFlags(args[1:])
		if flags["mode"] == nil {
			flags["mode"] = "coverage"
		}
		runToolCLI("read_os", flags, opts, args[0])
		return true
	case "workflows", "playbook", "recipes":
		flags := parseKVFlags(args[1:])
		if flags["mode"] == nil {
			flags["mode"] = "workflows"
		}
		runToolCLI("read_os", flags, opts, args[0])
		return true
	case "update", "upgrade", "self-update", "self_update":
		runUpdateCLI(args[1:], opts)
		return true
	case "asr", "voice-asr", "voice_asr":
		runASRCLI(args[1:], opts)
		return true
	case "companion", "sidecar":
		runCompanionCLI(args[1:], opts)
		return true
	case "call":
		runGenericToolCLI(args[1:], opts)
		return true
	case "call-json", "call_json":
		runToolJSONCLI(args[1:], opts)
		return true
	case "tool-schema", "tool_schema", "describe", "describe-tool":
		runToolSchemaCLI(args[1:], opts)
		return true
	case "cache":
		runCacheCLI(args[1:], opts)
		return true
	case "resolve-chat", "resolve_chat":
		flags := parseKVFlags(args[1:])
		if q := firstPositional(args[1:]); q != "" {
			flags["query"] = q
		}
		runToolCLI("resolve_chat", flags, opts, args[0])
		return true
	case "sessions":
		runToolCLI("sessions", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "contacts":
		runToolCLI("contacts", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "history", "messages":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("messages", flags, opts, args[0])
		return true
	case "timeline", "chat-timeline", "chat_timeline", "conversation-view", "conversation_view":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("chat_timeline", flags, opts, args[0])
		return true
	case "context", "message-context", "message_context", "around":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("message_context", flags, opts, args[0])
		return true
	case "tail", "watch", "observe", "events":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runTailCLI(flags, opts, args[0])
		return true
	case "media", "media-resources", "media_resources", "attachments":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("media_resources", flags, opts, args[0])
		return true
	case "search":
		flags := parseKVFlags(args[1:])
		if kw := firstPositional(args[1:]); kw != "" && flags["keyword"] == nil {
			flags["keyword"] = kw
		}
		if v, ok := flags["in"]; ok && flags["chat"] == nil {
			flags["chat"] = v
			delete(flags, "in")
		}
		runToolCLI("search", flags, opts, args[0])
		return true
	case "search-context", "search_context", "search-with-context", "search_with_context":
		flags := parseKVFlags(args[1:])
		if kw := firstPositional(args[1:]); kw != "" && flags["keyword"] == nil {
			flags["keyword"] = kw
		}
		if v, ok := flags["in"]; ok && flags["chat"] == nil {
			flags["chat"] = v
			delete(flags, "in")
		}
		runToolCLI("search_with_context", flags, opts, args[0])
		return true
	case "members", "group-members", "group_members":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["chatroom_id"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("group_members", flags, opts, args[0])
		return true
	case "stats":
		runToolCLI("stats", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "unread":
		runToolCLI("unread", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "export", "export-messages", "export_messages":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["talker"] == nil && flags["chat"] == nil {
			flags["chat"] = chat
		}
		runToolCLI("export_messages", flags, opts, args[0])
		return true
	case "favorites":
		runToolCLI("favorites", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "red-packets", "red_packets":
		runToolCLI("red_packets", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "transfers":
		runToolCLI("transfers", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "sns", "sns-feed", "sns_feed":
		runToolCLI("sns_feed", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "sns-search", "sns_search":
		flags := parseKVFlags(args[1:])
		if kw := firstPositional(args[1:]); kw != "" && flags["keyword"] == nil {
			flags["keyword"] = kw
		}
		runToolCLI("sns_search", flags, opts, args[0])
		return true
	case "sns-notifications", "sns_notifications":
		runToolCLI("sns_notifications", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "sql":
		flags := parseKVFlags(args[1:])
		if q := firstPositional(args[1:]); q != "" && flags["query"] == nil {
			flags["query"] = q
		}
		runToolCLI("sql", flags, opts, args[0])
		return true
	case "schema":
		runToolCLI("schema", parseKVFlags(args[1:]), opts, args[0])
		return true
	case "announcements", "chatroom-announcements", "chatroom_announcements":
		flags := parseKVFlags(args[1:])
		if chat := firstPositional(args[1:]); chat != "" && flags["chatroom_id"] == nil {
			flags["chatroom_id"] = chat
		}
		runToolCLI("chatroom_announcements", flags, opts, args[0])
		return true
	case "forward-history", "forward_history":
		runToolCLI("forward_history", parseKVFlags(args[1:]), opts, args[0])
		return true
	default:
		exitCLIError(opts, 2, "unknown_command", fmt.Sprintf("unknown command %q", args[0]), "", args[0])
		return true
	}
}

func runToolsCLI(args []string, opts cliOptions) {
	flags := parseKVFlags(args)
	profile := firstNonEmpty(getStr(flags, "profile"), getStr(flags, "view"))
	tools, ok := listedToolDefsForProfile(profile)
	if !ok {
		exitCLIError(opts, 2, "invalid_argument", fmt.Sprintf("invalid tools profile=%q: must be assistant, maintenance, or all", profile), "tools", "tools")
	}
	if profile == "" {
		profile = "assistant"
	}
	schemaProfile := "assistant"
	if profile == "all" {
		schemaProfile = "all"
	}
	data := map[string]any{
		"query": map[string]any{
			"tool":               "tools",
			"command":            "tools",
			"profile":            profile,
			"schema_profile":     schemaProfile,
			"available_profiles": []string{"assistant", "maintenance", "all"},
			"returned":           len(tools),
		},
		"tools": tools,
	}
	writeCLISuccess("tools", "tools", data, opts)
}

func runGenericToolCLI(args []string, opts cliOptions) {
	if len(args) == 0 {
		exitCLIError(opts, 2, "missing_tool", "usage: "+appName+" call <command-or-tool> [--key value ...]", "", "call")
	}
	name, ok := callableToolNameForTarget(args[0])
	if !ok {
		exitCLIError(opts, 2, "unknown_tool", fmt.Sprintf("unknown command or tool %q", args[0]), args[0], "call")
	}
	runToolCLI(name, parseKVFlags(args[1:]), opts, "call")
}

func runToolJSONCLI(args []string, opts cliOptions) {
	if len(args) == 0 {
		exitCLIError(opts, 2, "missing_tool", "usage: "+appName+" call-json <command-or-tool> '<json args>'", "", "call-json")
	}
	name, ok := callableToolNameForTarget(args[0])
	if !ok {
		exitCLIError(opts, 2, "unknown_tool", fmt.Sprintf("unknown command or tool %q", args[0]), args[0], "call-json")
	}
	raw := ""
	if len(args) > 1 {
		raw = args[1]
	} else {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			exitCLIError(opts, 1, "stdin_read_error", err.Error(), name, "call-json")
		}
		raw = string(data)
	}
	flags, err := decodeCLIJSONArgs(raw)
	if err != nil {
		exitCLIError(opts, 1, "invalid_json", "invalid json args: "+err.Error(), name, "call-json")
	}
	runToolCLI(name, flags, opts, "call-json")
}

func decodeCLIJSONArgs(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var flags map[string]any
	if err := dec.Decode(&flags); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values are not allowed")
		}
		return nil, err
	}
	if flags == nil {
		flags = map[string]any{}
	}
	for key, value := range flags {
		normalized, err := normalizeCLIJSONValue(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		flags[key] = normalized
	}
	return flags, nil
}

func normalizeCLIJSONValue(value any) (any, error) {
	switch value := value.(type) {
	case json.Number:
		if n, err := strconv.ParseInt(value.String(), 10, 64); err == nil {
			return n, nil
		}
		// Tool schemas expose integers, not arbitrary JSON numbers. Preserve
		// compatibility with integral forms such as 1.0/1e3 only inside the JSON
		// safe-integer range; larger IDs must be written as ordinary integer
		// tokens (handled exactly above) or supplied through the *_str fields.
		f, err := strconv.ParseFloat(value.String(), 64)
		const maxSafeJSONInteger = float64(1<<53 - 1)
		if err != nil || f < -maxSafeJSONInteger || f > maxSafeJSONInteger {
			return nil, fmt.Errorf("number %q is outside the safe integer range", value.String())
		}
		n := int64(f)
		if f != float64(n) {
			return nil, fmt.Errorf("number %q is not an integer", value.String())
		}
		return n, nil
	case map[string]any:
		for key, item := range value {
			normalized, err := normalizeCLIJSONValue(item)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			value[key] = normalized
		}
		return value, nil
	case []any:
		for i, item := range value {
			normalized, err := normalizeCLIJSONValue(item)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", i, err)
			}
			value[i] = normalized
		}
		return value, nil
	default:
		return value, nil
	}
}

func callableToolNameForTarget(target string) (string, bool) {
	_, tool, ok := cliHelpForTarget(target)
	if !ok || tool.Name == "" {
		return "", false
	}
	return tool.Name, true
}

func runToolSchemaCLI(args []string, opts cliOptions) {
	flags := parseKVFlags(args)
	profile := firstNonEmpty(getStr(flags, "profile"), getStr(flags, "view"))
	targetArgs := schemaTargetArgs(args)
	if len(targetArgs) == 0 {
		exitCLIError(opts, 2, "missing_tool", "usage: "+appName+" tool-schema <command-or-tool>", "tool_schema", "tool-schema")
		return
	}
	if profile != "" && profile != "assistant" && profile != "all" {
		exitCLIError(opts, 2, "invalid_argument", fmt.Sprintf("invalid tool-schema profile=%q: must be assistant or all", profile), "tool_schema", "tool-schema")
	}
	target := strings.Join(targetArgs, " ")
	if _, _, ok := cliHelpForTarget(target); !ok {
		exitCLIError(opts, 2, "unknown_help_target", fmt.Sprintf("unknown command or tool %q", target), "tool_schema", "tool-schema")
	}
	writeCLIToolSchema(target, profile, opts)
}

func runCacheCLI(args []string, opts cliOptions) {
	if len(args) == 0 {
		runCLIHelp("cache", opts)
		return
	}
	if hasHelpFlag(args[1:]) {
		runCLIHelp("cache "+args[0], opts)
		return
	}
	switch args[0] {
	case "status":
		runToolCLI("cache_status", parseKVFlags(args[1:]), opts, "cache status")
	case "refresh":
		runToolCLI("cache_refresh", parseKVFlags(args[1:]), opts, "cache refresh")
	case "rebuild":
		runToolCLI("cache_rebuild", parseKVFlags(args[1:]), opts, "cache rebuild")
	default:
		exitCLIError(opts, 2, "unknown_command", fmt.Sprintf("unknown cache command %q", args[0]), "", "cache")
	}
}

func runToolCLI(name string, flags map[string]any, opts cliOptions, command string) {
	data, errCode, err := runToolResult(name, flags, command)
	if err != nil {
		exitCLIError(opts, 1, errCode, err.Error(), name, command)
	}
	writeCLISuccess(name, command, data, opts)
}

func runToolResult(name string, flags map[string]any, command string) (any, string, error) {
	if err := validateToolArgs(name, flags); err != nil {
		return nil, cliErrorCode(err), err
	}
	responseArgs := cliToolResponseArgs(name, flags)
	flags = cliToolFetchArgs(name, responseArgs)
	srv := &server{}
	var result any
	var err error
	switch name {
	case "read_os":
		result, err = srv.toolReadOS(flags)
	case "resolve_chat":
		result, err = srv.toolResolveChat(flags)
	case "sessions":
		result, err = srv.toolSessions(flags)
	case "contacts":
		result, err = srv.toolContacts(flags)
	case "cache_status":
		result, err = srv.toolCacheStatus(flags)
	case "cache_refresh":
		result, err = srv.toolCacheRefresh(flags)
	case "cache_rebuild":
		result, err = srv.toolCacheRebuild(flags)
	case "stats":
		result, err = srv.toolStats(flags)
	case "unread":
		result, err = srv.toolUnread(flags)
	case "export_messages":
		result, err = srv.toolExportMessages(flags)
	case "search":
		result, err = srv.toolSearch(flags)
	case "search_with_context":
		result, err = srv.toolSearchWithContext(flags)
	case "messages":
		result, err = srv.toolMessages(flags)
	case "chat_timeline":
		result, err = srv.toolChatTimeline(flags)
	case "message_context":
		result, err = srv.toolMessageContext(flags)
	case "read_events":
		result, err = srv.toolReadEvents(flags)
	case "media_resources":
		result, err = srv.toolMediaResources(flags)
	case "group_members":
		result, err = srv.toolGroupMembers(flags)
	case "favorites":
		result, err = srv.toolFavorites(flags)
	case "red_packets":
		result, err = srv.toolRedPackets(flags)
	case "transfers":
		result, err = srv.toolTransfers(flags)
	case "sns_feed":
		result, err = srv.toolSnsFeed(flags)
	case "sns_search":
		result, err = srv.toolSnsSearch(flags)
	case "sns_notifications":
		result, err = srv.toolSnsNotifications(flags)
	case "sns":
		result, err = srv.toolSns(flags)
	case "sql":
		result, err = srv.toolSQL(flags)
	case "schema":
		result, err = srv.toolSchema(flags)
	case "chatroom_announcements":
		result, err = srv.toolChatroomAnnouncements(flags)
	case "forward_history":
		result, err = srv.toolForwardHistory(flags)
	default:
		err = fmt.Errorf("unknown cli tool %q", name)
	}
	if err != nil {
		return nil, "tool_error", err
	}
	return cliAgentDataEnvelope(name, command, responseArgs, result), "", nil
}

func cliToolFetchArgs(tool string, args map[string]any) map[string]any {
	limit := getInt(args, "limit", 0)
	if limit <= 0 || cliResultListKey(tool) == "" {
		return args
	}
	if tool == "messages" && getStr(args, "view") == "agent" {
		return args
	}
	out := copyToolArgs(args)
	out["limit"] = int64(limit + 1)
	return out
}

func cliToolResponseArgs(tool string, args map[string]any) map[string]any {
	out := copyToolArgs(args)
	if getInt(out, "limit", 0) <= 0 {
		if limit := cliToolDefaultLimit(tool); limit > 0 {
			out["limit"] = int64(limit)
		}
	}
	return out
}

func cliToolDefaultLimit(tool string) int {
	switch tool {
	case "sessions", "contacts", "messages", "media_resources", "favorites", "red_packets", "transfers", "sns_notifications", "forward_history", "unread":
		return 50
	case "search", "sns_feed", "sns_search", "chatroom_announcements":
		return 20
	case "group_members":
		return 100
	case "sql":
		return 200
	default:
		return 0
	}
}

func runTailCLI(flags map[string]any, opts cliOptions, command string) {
	jsonl := getBoolDefault(flags, "jsonl", false)
	follow := getBoolDefault(flags, "follow", false)
	if !jsonl && !follow {
		runToolCLI("read_events", flags, opts, command)
		return
	}
	if err := validateToolArgs("read_events", flags); err != nil {
		exitCLIError(opts, 1, cliErrorCode(err), err.Error(), "read_events", command)
	}
	srv := &server{}
	enc := json.NewEncoder(os.Stdout)
	for {
		result, err := srv.toolReadEvents(flags)
		if err != nil {
			exitCLIError(opts, 1, "tool_error", err.Error(), "read_events", command)
		}
		env, _ := result.(map[string]any)
		if err := writeReadEventsJSONL(enc, env); err != nil {
			exitCLIError(opts, 1, "io_error", err.Error(), "read_events", command)
		}
		if cursor := rowString(wcdb.Row(env), "cursor"); cursor != "" {
			flags["cursor"] = cursor
		}
		if !follow {
			return
		}
		time.Sleep(readEventsPollInterval(flags))
	}
}

func writeReadEventsJSONL(enc *json.Encoder, env map[string]any) error {
	for _, event := range mapSliceAny(env["events"]) {
		if err := enc.Encode(event); err != nil {
			return err
		}
	}
	return nil
}

// cliRowsResult carries row-oriented tool output plus source diagnostics without
// exposing an ad-hoc map shape to internal callers that still need the raw rows.
type cliRowsResult struct {
	Rows      []wcdb.Row
	Freshness map[string]any
	Warnings  []string
}

func cliAgentDataEnvelope(tool, command string, args map[string]any, result any) any {
	if _, ok := result.(map[string]any); ok {
		return result
	}
	listKey := cliResultListKey(tool)
	if listKey == "" {
		return result
	}
	rows, ok := cliResultRows(result)
	if !ok {
		return result
	}
	freshness, warnings := cliRowsResultMetadata(result)
	if freshness == nil {
		freshness = cliResultFreshnessMeta(tool)
	}
	query := cliResultQueryMeta(tool, command, args, rows)
	if complete, ok := freshness["complete"].(bool); ok && !complete {
		delete(query, "has_more")
		delete(query, "next_offset")
		query["has_more_unknown"] = true
	}
	limit := getInt(args, "limit", 0)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := compactMap(map[string]any{
		"query":     query,
		"freshness": freshness,
		"warnings":  warnings,
	})
	// A stable empty list is part of the CLI contract; compactMap intentionally
	// removes empty slices, so assign the list key after compaction.
	out[listKey] = cliResultRowsForTool(tool, args, rows)
	return out
}

func cliResultRows(result any) ([]map[string]any, bool) {
	switch rows := result.(type) {
	case []map[string]any:
		if rows == nil {
			rows = make([]map[string]any, 0)
		}
		return rows, true
	case []wcdb.Row:
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any(row))
		}
		return out, true
	case []*snsPost:
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			data, err := json.Marshal(row)
			if err != nil {
				return nil, false
			}
			var mapped map[string]any
			if err := json.Unmarshal(data, &mapped); err != nil {
				return nil, false
			}
			out = append(out, mapped)
		}
		return out, true
	case cliRowsResult:
		return cliResultRows(rows.Rows)
	case *cliRowsResult:
		if rows == nil {
			return make([]map[string]any, 0), true
		}
		return cliResultRows(rows.Rows)
	default:
		return nil, false
	}
}

func cliRowsResultMetadata(result any) (map[string]any, []string) {
	switch result := result.(type) {
	case cliRowsResult:
		return result.Freshness, append([]string(nil), result.Warnings...)
	case *cliRowsResult:
		if result != nil {
			return result.Freshness, append([]string(nil), result.Warnings...)
		}
	}
	return nil, nil
}

func cliResultRowsForTool(tool string, args map[string]any, rows []map[string]any) []map[string]any {
	if tool != "search" {
		return rows
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, cliSearchMessageRow(row, args))
	}
	return out
}

func cliResultFreshnessMeta(tool string) map[string]any {
	switch tool {
	case "contacts", "group_members", "chatroom_announcements":
		return map[string]any{"message_source": "live_contact_db"}
	case "media_resources":
		return map[string]any{
			"message_source":      "live_message_resource_db",
			"metadata_cache_role": "chat/sender display names only",
		}
	case "favorites":
		return map[string]any{
			"message_source":      "live_favorite_db",
			"metadata_cache_role": "display names only",
		}
	case "red_packets", "transfers":
		return map[string]any{
			"message_source":      "live_general_db",
			"message_enrichment":  "live_message_db_best_effort",
			"metadata_cache_role": "chat/sender display names only",
		}
	case "sns_feed", "sns_search", "sns_notifications":
		return map[string]any{"message_source": "live_sns_db"}
	case "forward_history":
		return map[string]any{
			"message_source":      "live_general_db",
			"metadata_cache_role": "display names only",
		}
	case "sql":
		return map[string]any{"message_source": "live_selected_db_read_only"}
	case "schema":
		return map[string]any{"message_source": "live_db_schema"}
	}
	return nil
}

func cliSearchMessageRow(row map[string]any, args map[string]any) map[string]any {
	createTime := int64MapValue(row, "create_time")
	text := firstNonEmpty(stringMapValue(row, "content_summary"), stringMapValue(row, "content"))
	match := stringMapValue(row, "content")
	originalTextChars := maxInt(len([]rune(text)), len([]rune(match)))
	maxChars := getInt(args, "max_text_chars", 0)
	if getBoolDefault(args, "snippet_only", false) && maxChars == 0 {
		maxChars = 180
	}
	textTruncated := false
	if maxChars > 0 {
		textTruncated = len([]rune(text)) > maxChars || len([]rune(match)) > maxChars
		text = truncateRunes(text, maxChars)
		match = truncateRunes(match, maxChars)
	}
	out := compactMap(map[string]any{
		"id": compactMap(map[string]any{
			"local_id": row["local_id"],
			"talker":   row["talker"],
		}),
		"time":                   formatUnixLocal(createTime),
		"time_iso":               cliFormatUnixISO(createTime),
		"create_time":            row["create_time"],
		"sender":                 firstNonEmpty(stringMapValue(row, "sender_display_name"), stringMapValue(row, "sender_wxid")),
		"sender_wxid":            row["sender_wxid"],
		"sender_group_nickname":  row["sender_group_nickname"],
		"sender_contact_display": row["sender_contact_display"],
		"chat": compactMap(map[string]any{
			"talker":       row["talker"],
			"display_name": row["talker_display_name"],
			"chat_type":    row["chat_type"],
		}),
		"kind":  row["kind_name"],
		"text":  text,
		"match": match,
	})
	if !getBoolDefault(args, "include_text", true) {
		delete(out, "text")
		delete(out, "match")
	}
	if maxChars > 0 || !getBoolDefault(args, "include_text", true) {
		out["original_text_chars"] = originalTextChars
	}
	if maxChars > 0 {
		out["text_truncated"] = textTruncated
	}
	return out
}

func applyAgentTextOutputOptions(messages []map[string]any, args map[string]any) {
	for _, msg := range messages {
		applyAgentMessageTextOutputOptions(msg, args)
	}
}

func applyAgentMessageTextOutputOptions(msg map[string]any, args map[string]any) {
	if msg == nil {
		return
	}
	text, ok := msg["text"].(string)
	if !ok {
		return
	}
	originalTextChars := len([]rune(text))
	maxChars := getInt(args, "max_text_chars", 0)
	if getBoolDefault(args, "snippet_only", false) && maxChars == 0 {
		maxChars = 180
	}
	if maxChars > 0 {
		textTruncated := len([]rune(text)) > maxChars
		msg["text"] = truncateRunes(text, maxChars)
		msg["original_text_chars"] = originalTextChars
		msg["text_truncated"] = textTruncated
	}
	if !getBoolDefault(args, "include_text", true) {
		delete(msg, "text")
		msg["original_text_chars"] = originalTextChars
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func queryFromMeArg(args map[string]any) bool {
	if getBoolDefault(args, "from_me", false) {
		return true
	}
	sender := strings.ToLower(strings.TrimSpace(getStr(args, "sender")))
	return sender == "me" || sender == "self"
}

func cliFormatUnixISO(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}

func cliResultListKey(tool string) string {
	switch tool {
	case "sessions", "unread":
		return "sessions"
	case "contacts":
		return "contacts"
	case "search":
		return "messages"
	case "messages":
		return "messages"
	case "media_resources":
		return "media"
	case "group_members":
		return "members"
	case "favorites":
		return "favorites"
	case "red_packets":
		return "red_packets"
	case "transfers":
		return "transfers"
	case "sns_feed", "sns_search":
		return "posts"
	case "sns_notifications":
		return "notifications"
	case "schema":
		return "tables"
	case "sql":
		return "rows"
	case "chatroom_announcements":
		return "announcements"
	case "forward_history":
		return "forwards"
	default:
		return ""
	}
}

func cliResultQueryMeta(tool, command string, args map[string]any, rows []map[string]any) map[string]any {
	limit := getInt(args, "limit", 0)
	offset := getInt(args, "offset", 0)
	returned := len(rows)
	hasMore := false
	if limit > 0 && returned > limit {
		returned = limit
		hasMore = true
	}
	meta := compactMap(map[string]any{
		"tool":        tool,
		"command":     command,
		"chat":        firstNonEmpty(getStr(args, "chat"), getStr(args, "talker"), getStr(args, "chatroom_id")),
		"keyword":     getStr(args, "keyword"),
		"type":        firstNonEmpty(getStr(args, "type"), getStr(args, "kind_name"), getStr(args, "type_filter"), getStr(args, "filter")),
		"sender":      getStr(args, "sender"),
		"from_me":     queryFromMeArg(args),
		"after":       getStr(args, "after"),
		"before":      getStr(args, "before"),
		"limit":       limit,
		"offset":      offset,
		"returned":    returned,
		"has_more":    hasMore,
		"next_offset": 0,
	})
	meta["returned"] = returned
	meta["has_more"] = hasMore
	if limit > 0 {
		meta["limit"] = limit
		meta["offset"] = offset
		if hasMore {
			meta["next_offset"] = offset + returned
		} else {
			delete(meta, "next_offset")
		}
	} else {
		delete(meta, "limit")
		delete(meta, "offset")
		delete(meta, "next_offset")
	}
	return meta
}

func cliToolNames() []string {
	return []string{
		"read_os",
		"resolve_chat",
		"sessions",
		"contacts",
		"cache_status",
		"cache_refresh",
		"cache_rebuild",
		"stats",
		"unread",
		"export_messages",
		"search",
		"search_with_context",
		"messages",
		"chat_timeline",
		"message_context",
		"read_events",
		"media_resources",
		"group_members",
		"favorites",
		"red_packets",
		"transfers",
		"sns",
		"sns_feed",
		"sns_search",
		"sns_notifications",
		"sql",
		"schema",
		"chatroom_announcements",
		"forward_history",
	}
}

func writeJSONCLI(v any, opts cliOptions) {
	enc := json.NewEncoder(os.Stdout)
	if opts.Pretty {
		enc.SetIndent("", "  ")
	}
	_ = enc.Encode(v)
}

func writeCLISuccess(tool, command string, data any, opts cliOptions) {
	writeJSONCLI(cliSuccessEnvelope{
		OK:      true,
		Tool:    tool,
		Command: command,
		Data:    data,
	}, opts)
}

func exitCLIError(opts cliOptions, code int, errCode, message, tool, command string) {
	advice := cliErrorAdvice(errCode, message, tool, command)
	writeJSONCLI(cliErrorEnvelope{
		OK: false,
		Error: cliError{
			Code:              errCode,
			Message:           message,
			Tool:              tool,
			Command:           command,
			NextAction:        advice.NextAction,
			SuggestedCommands: advice.SuggestedCommands,
		},
	}, opts)
	os.Exit(code)
}

type cliAdvice struct {
	NextAction        string
	SuggestedCommands []string
}

func cliErrorAdvice(errCode, message, tool, command string) cliAdvice {
	msg := strings.ToLower(message)
	cmd := cliSchemaCommandForError(tool, command)
	schemaCmd := ""
	if cmd != "" {
		schemaCmd = appName + " tool-schema " + cmd
	}
	switch errCode {
	case "unknown_command":
		return cliAdvice{
			NextAction:        "List commands or start from the agent entrypoint.",
			SuggestedCommands: []string{appName + " agent --pretty", appName + " --help"},
		}
	case "unknown_help_target":
		return cliAdvice{
			NextAction:        "List known tools/commands, then request the exact schema.",
			SuggestedCommands: []string{appName + " tools", appName + " --help"},
		}
	case "missing_tool":
		return cliAdvice{
			NextAction:        "Choose a tool from the tool list, or use the agent entrypoint for workflows.",
			SuggestedCommands: []string{appName + " tools", appName + " agent --pretty"},
		}
	case "invalid_json":
		return cliAdvice{
			NextAction:        "Pass a valid JSON object as argv or stdin.",
			SuggestedCommands: []string{appName + ` call-json timeline '{"chat":"$CHAT","limit":20}'`},
		}
	case "missing_required_argument", "unknown_argument", "invalid_argument":
		cmds := []string{}
		if schemaCmd != "" {
			cmds = append(cmds, schemaCmd)
		}
		cmds = append(cmds, appName+" agent --mode workflows --pretty")
		return cliAdvice{
			NextAction:        "Inspect the command schema and retry with documented arguments.",
			SuggestedCommands: cmds,
		}
	}
	switch {
	case strings.Contains(msg, "libwcdb") || strings.Contains(msg, "wcdb"):
		return cliAdvice{
			NextAction:        "Use an installed release or set WECHAT_CLI_WCDB_LIB/WECHAT_CLI_WCDB_DYLIB to the bundled WCDB library.",
			SuggestedCommands: []string{appName + " status --pretty"},
		}
	case strings.Contains(msg, "cache index") || strings.Contains(msg, "requires cache index"):
		return cliAdvice{
			NextAction:        "Refresh metadata cache, or retry with a raw talker/wxid from resolve-chat.",
			SuggestedCommands: []string{appName + " cache refresh", appName + ` resolve-chat "$CHAT" --type-filter group`},
		}
	case strings.Contains(msg, "chat") && (strings.Contains(msg, "not found") || strings.Contains(msg, "ambiguous") || strings.Contains(msg, "requires")):
		return cliAdvice{
			NextAction:        "Resolve the human chat name first, then retry with the returned username/talker.",
			SuggestedCommands: []string{appName + ` resolve-chat "$CHAT"`, appName + ` timeline "$CHAT" --limit 20`},
		}
	case strings.Contains(msg, "talker or chat is required"):
		return cliAdvice{
			NextAction:        "Pass a chat name or raw talker. If only a human name is known, run resolve-chat first.",
			SuggestedCommands: []string{appName + ` resolve-chat "$CHAT"`, appName + ` timeline "$CHAT" --limit 20`},
		}
	case strings.Contains(msg, "keyword is required"):
		return cliAdvice{
			NextAction:        "Pass the keyword as the first positional argument or --keyword.",
			SuggestedCommands: []string{appName + ` search "$KEYWORD" --limit 20`, appName + ` search-context "$KEYWORD" --context-limit 3`},
		}
	case strings.Contains(msg, "local_id or server_id is required") || strings.Contains(msg, "anchor message not found"):
		return cliAdvice{
			NextAction:        "Use a local_id/server_id from timeline/search rows, then retry context.",
			SuggestedCommands: []string{appName + ` timeline "$CHAT" --limit 20`, appName + ` search-context "$KEYWORD" --context-limit 3`},
		}
	case strings.Contains(msg, "no schema-2") || strings.Contains(msg, "enc_key") || strings.Contains(msg, "wxkey"):
		return cliAdvice{
			NextAction:        "Prepare or refresh local WeChat DB keys, then rerun the read command.",
			SuggestedCommands: []string{"~/.local/share/wechat-cli/wxkey bootstrap", appName + " status --pretty"},
		}
	default:
		cmds := []string{appName + " status --pretty", appName + " agent --pretty"}
		if schemaCmd != "" {
			cmds = append([]string{schemaCmd}, cmds...)
		}
		return cliAdvice{
			NextAction:        "Inspect status/schema, then retry with the documented workflow.",
			SuggestedCommands: cmds,
		}
	}
}

func cliSchemaCommandForError(tool, command string) string {
	if command == "call" || command == "call-json" || command == "call_json" {
		return firstNonEmpty(cliCommandForTool(tool), tool)
	}
	return firstNonEmpty(command, cliCommandForTool(tool), tool)
}

func cliCommandForTool(tool string) string {
	for _, spec := range cliCommandSpecs {
		if spec.Tool == tool && spec.Command != "" {
			return spec.Command
		}
	}
	return ""
}

func cliErrorCode(err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "missing required argument"):
		return "missing_required_argument"
	case strings.HasPrefix(msg, "unknown argument"):
		return "unknown_argument"
	case strings.HasPrefix(msg, "invalid argument"):
		return "invalid_argument"
	default:
		return "invalid_argument"
	}
}

func parseGlobalCLIOptions(args []string) (cliOptions, []string, error) {
	opts := cliOptions{}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		key, val, hasValue, ok := splitGlobalFlag(a)
		if !ok {
			out = append(out, a)
			continue
		}
		if !hasValue && i+1 < len(args) && isBoolLiteral(args[i+1]) {
			val = args[i+1]
			hasValue = true
			i++
		}
		switch key {
		case "json":
			if hasValue {
				if _, err := parseBoolValue(key, val); err != nil {
					return opts, nil, err
				}
			}
		case "pretty":
			if !hasValue {
				opts.Pretty = true
				continue
			}
			b, err := parseBoolValue(key, val)
			if err != nil {
				return opts, nil, err
			}
			opts.Pretty = b
		case "compact":
			if !hasValue {
				opts.Pretty = false
				continue
			}
			b, err := parseBoolValue(key, val)
			if err != nil {
				return opts, nil, err
			}
			opts.Pretty = !b
		case "strict_read_only", "read_only":
			b := true
			if hasValue {
				parsed, err := parseBoolValue(key, val)
				if err != nil {
					return opts, nil, err
				}
				b = parsed
			}
			opts.StrictReadOnly = b
			if b {
				_ = os.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "1")
			} else {
				_ = os.Setenv("WECHAT_CLI_STRICT_READ_ONLY", "0")
			}
		default:
			out = append(out, a)
		}
	}
	return opts, out, nil
}

func splitGlobalFlag(arg string) (key, val string, hasValue bool, ok bool) {
	if !strings.HasPrefix(arg, "--") {
		return "", "", false, false
	}
	raw := strings.TrimPrefix(arg, "--")
	key, val, hasValue = strings.Cut(raw, "=")
	key = strings.ReplaceAll(key, "-", "_")
	switch key {
	case "json", "pretty", "compact", "strict_read_only", "read_only":
		return key, val, hasValue, true
	default:
		return "", "", false, false
	}
}

func parseBoolValue(key, val string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(val)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value for --%s: %q", strings.ReplaceAll(key, "_", "-"), val)
	}
}

func parseKVFlags(args []string) map[string]any {
	out := map[string]any{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		a = strings.TrimPrefix(a, "--")
		key, val, hasEq := strings.Cut(a, "=")
		key = strings.ReplaceAll(key, "-", "_")
		if !hasEq {
			if isBoolCLIFlag(key) {
				if i+1 < len(args) && isBoolLiteral(args[i+1]) {
					val = args[i+1]
					i++
				} else {
					out[key] = true
					continue
				}
			} else if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				val = args[i+1]
				i++
			} else {
				out[key] = true
				continue
			}
		}
		if strings.HasSuffix(key, "_str") {
			out[key] = val
			continue
		}
		if n, err := strconv.ParseInt(val, 10, 64); err == nil {
			out[key] = n
			continue
		}
		if val == "true" {
			out[key] = true
			continue
		}
		if val == "false" {
			out[key] = false
			continue
		}
		out[key] = val
	}
	return out
}

func isBoolCLIFlag(key string) bool {
	switch key {
	case "allow_remote", "background", "browser", "debug", "desktop", "follow", "force", "friends_only", "from_me", "groups_only", "include_anchor", "include_debug", "include_images", "include_local_paths", "include_media_paths", "include_read", "include_status", "include_text", "jsonl", "no_open", "open", "snippet_only", "stats":
		return true
	default:
		return false
	}
}

func isBoolLiteral(s string) bool {
	return s == "true" || s == "false"
}

func hasHelpFlag(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func helpTargetForCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if args[0] == "cache" || args[0] == "asr" || args[0] == "voice-asr" || args[0] == "voice_asr" {
		for _, a := range args[1:] {
			if a != "-h" && a != "--help" {
				prefix := "cache"
				if args[0] != "cache" {
					prefix = "asr"
				}
				return prefix + " " + a
			}
		}
		if args[0] == "cache" {
			return "cache"
		}
		return "asr"
	}
	return args[0]
}

func firstPositional(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			key := strings.TrimPrefix(a, "--")
			key, _, _ = strings.Cut(key, "=")
			key = strings.ReplaceAll(key, "-", "_")
			if !strings.Contains(a, "=") && !isBoolCLIFlag(key) && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

func printCLIUsage() {
	runCLIHelp("", cliOptions{Pretty: true})
}

func printCLIUsageTo(w io.Writer) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(cliHelpDocument(""))
}

func writeCLIHelp(target string, opts cliOptions) {
	writeCLISuccess("help", "help", cliHelpDocument(target), opts)
}

func writeCLIToolSchema(target string, profile string, opts cliOptions) {
	writeCLISuccess("tool_schema", "tool-schema", cliHelpDocumentWithProfile(target, profile), opts)
}

func runCLIHelp(target string, opts cliOptions) {
	if strings.TrimSpace(target) != "" {
		if _, _, ok := cliHelpForTarget(target); !ok {
			exitCLIError(opts, 2, "unknown_help_target", fmt.Sprintf("unknown command or tool %q", target), "", target)
		}
	}
	writeCLIHelp(target, opts)
}

func cliHelpDocument(target string) any {
	return cliHelpDocumentWithProfile(target, "")
}

func cliHelpDocumentWithProfile(target string, profile string) any {
	target = strings.TrimSpace(target)
	schemaProfile := "assistant"
	if profile == "all" {
		schemaProfile = "all"
	}
	if target != "" {
		spec, tool, ok := cliHelpForTarget(target)
		if !ok {
			return cliErrorEnvelope{OK: false, Error: cliError{Code: "unknown_help_target", Message: fmt.Sprintf("unknown command or tool %q", target), Command: target}}
		}
		doc := map[string]any{
			"name":           appName,
			"version":        appVersion,
			"command":        spec,
			"global_flags":   cliGlobalFlags(),
			"schema_profile": schemaProfile,
		}
		if tool.Name != "" {
			displayTool := tool
			if schemaProfile != "all" {
				displayTool = displayToolDef(tool)
				doc["full_schema_hint"] = "Use `wechat-cli tool-schema " + spec.Command + " --profile all` or `wechat-cli tools --profile all` for compatibility aliases and maintenance fields."
			}
			doc["tool"] = displayTool
			doc["agent"] = agentHelpForTool(spec, displayTool)
		}
		return doc
	}
	return map[string]any{
		"name":    appName,
		"version": appVersion,
		"output_contract": map[string]any{
			"stdout":              "json",
			"success":             "object with ok=true, tool, command, data",
			"error":               "object with ok=false and error.code/message/next_action/suggested_commands",
			"default":             "compact single JSON envelope",
			"streaming_exception": "tail/watch with --jsonl or --follow emits newline-delimited event objects without the envelope",
		},
		"global_flags": cliGlobalFlags(),
		"commands":     cliCommandSpecs,
	}
}

func schemaTargetArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			out = append(out, a)
			continue
		}
		raw := strings.TrimPrefix(a, "--")
		key, _, hasEq := strings.Cut(raw, "=")
		key = strings.ReplaceAll(key, "-", "_")
		switch key {
		case "profile", "view":
			if !hasEq && i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
			}
			continue
		default:
			out = append(out, a)
		}
	}
	return out
}

func cliGlobalFlags() []map[string]string {
	return []map[string]string{
		{"name": "--json", "description": "Accepted for compatibility; stdout is already JSON."},
		{"name": "--compact", "description": "Emit compact JSON. This is the default."},
		{"name": "--pretty", "description": "Emit indented JSON for inspection."},
		{"name": "--strict-read-only", "description": "Disable local support-file writes such as cache refresh, media decode cache, voice transcript cache, and export."},
		{"name": "--help", "description": "Return machine-readable help for the command without executing it."},
	}
}

func agentHelpForTool(spec cliCommandSpec, tool toolDef) map[string]any {
	out := map[string]any{
		"output": map[string]any{
			"success_envelope": map[string]string{
				"ok":      "true",
				"tool":    tool.Name,
				"command": spec.Command,
				"data":    "tool result payload",
			},
			"error_envelope": "ok=false with error.code, error.message, error.tool, error.command, error.next_action, error.suggested_commands",
		},
	}
	if len(spec.Examples) > 0 {
		out["examples"] = spec.Examples
	}
	props := toolInputProperties(tool)
	var strategy []string
	if hasAnyProp(props, "chat", "talker", "chatroom_id") {
		strategy = append(strategy, "If a human chat name may be ambiguous, run resolve-chat first and pass the returned username as talker/chatroom_id.")
	}
	if hasAnyProp(props, "limit", "offset") && tool.Name != "message_context" {
		strategy = append(strategy, "For complete reads, loop while data.query.has_more is true when available; otherwise increment offset by the returned item count until fewer than limit rows return.")
	}
	if hasAnyProp(props, "after", "before") {
		strategy = append(strategy, "Time filters accept unix seconds, YYYY-MM-DD, YYYY-MM-DDTHH:MM:SS, or YYYY-MM-DD HH:MM:SS in local time.")
	}
	if hasAnyProp(props, "include_debug", "debug") {
		strategy = append(strategy, "Keep debug/include_debug false for normal use; retry with include_debug=true only to diagnose missing media or parser warnings.")
	}
	switch tool.Name {
	case "media_resources":
		strategy = append(strategy, "Use local_id/server_id from timeline/search rows to fetch direct image/video/file paths.")
		strategy = append(strategy, "Forwarded image items are resolved when their source server_id can be matched in message_resource.db and the local media file is cached or decodable.")
	case "export_messages":
		strategy = append(strategy, "Use export for large single-chat outputs instead of keeping all rows in model context.")
		strategy = append(strategy, "Default view=agent writes the same display-ready message shape as timeline; use view=raw only for low-level debugging.")
	case "chat_timeline":
		strategy = append(strategy, "Use timeline as the default chat-reading entrypoint for summarization and recent context.")
		strategy = append(strategy, "Default image refs expose one best readable local path: original/high-resolution when available, thumbnail only as fallback.")
		strategy = append(strategy, "For forwarded image items, success means forward_chat.items[].images[].path exists and forward_image_not_resolved is absent.")
	case "search_with_context":
		strategy = append(strategy, "Use search-context when the next step is to explain what happened around each keyword hit.")
		strategy = append(strategy, "context-limit caps how many hits get expanded; raise it deliberately for broad searches.")
	case "message_context":
		strategy = append(strategy, "Use context after search/timeline when you need to read surrounding messages around a known local_id or server_id.")
		strategy = append(strategy, "Use search -> context for investigation workflows: search finds the line, context restores the conversation around it.")
		strategy = append(strategy, "Use before-count/after-count to control the context window; context is not an offset-paged full-chat reader.")
	case "read_os":
		strategy = append(strategy, "Use agent first when an agent needs to understand available WeChat reading capabilities and quality gates.")
	}
	if len(strategy) > 0 {
		out["strategy"] = strategy
	}
	recovery := []map[string]string{{
		"error":  "missing_required_argument / invalid_argument / unknown_argument",
		"action": "Call tool-schema for this command and retry with the documented properties.",
	}}
	if hasAnyProp(props, "chat", "talker", "chatroom_id") {
		recovery = append(recovery, map[string]string{"error": "chat not found or ambiguous", "action": "Run resolve-chat with type_filter, then retry with the returned username."})
	}
	if hasAnyProp(props, "include_debug", "debug") {
		recovery = append(recovery, map[string]string{"error": "missing media paths or parser warnings", "action": "Retry the same command with include_debug=true and inspect warnings."})
	}
	out["recovery"] = recovery
	return out
}

func toolInputProperties(tool toolDef) map[string]any {
	schema, _ := tool.InputSchema.(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	return props
}

func hasAnyProp(props map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := props[k]; ok {
			return true
		}
	}
	return false
}

func cliHelpForTarget(target string) (cliCommandSpec, toolDef, bool) {
	target = normalizeCLIName(target)
	for _, spec := range cliCommandSpecs {
		if normalizeCLIName(spec.Command) == target {
			return spec, toolDefByName(spec.Tool), true
		}
		for _, alias := range spec.Aliases {
			if normalizeCLIName(alias) == target {
				return spec, toolDefByName(spec.Tool), true
			}
		}
	}
	if td := toolDefByName(target); td.Name != "" {
		return cliCommandSpec{Command: target, Tool: td.Name, Usage: appName + " call " + td.Name + " [--key value ...]"}, td, true
	}
	return cliCommandSpec{}, toolDef{}, false
}

func toolDefByName(name string) toolDef {
	name = normalizeCLIName(name)
	for _, td := range toolDefs {
		if normalizeCLIName(td.Name) == name {
			return annotateToolDef(td)
		}
	}
	return toolDef{}
}

func normalizeCLIName(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.ReplaceAll(s, "-", "_")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}
