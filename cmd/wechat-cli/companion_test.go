package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func companionTestToken(t *testing.T, handler http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index status = %d, body=%s", rec.Code, rec.Body.String())
	}
	marker := `const COMPANION_TOKEN = "`
	start := strings.Index(rec.Body.String(), marker)
	if start < 0 {
		t.Fatalf("index missing companion token")
	}
	start += len(marker)
	end := strings.Index(rec.Body.String()[start:], `"`)
	if end < 0 {
		t.Fatalf("unterminated companion token")
	}
	return rec.Body.String()[start : start+end]
}

func companionAuthorizeTestRequest(req *http.Request, token string) {
	req.Host = "127.0.0.1"
	req.Header.Set("X-Wechat-Companion-Token", token)
	req.Header.Set("Origin", "http://"+req.Host)
}

func companionUseTestCPU(t *testing.T, runner companionCPURunnerFunc) {
	t.Helper()
	old := companionCPURunner
	companionCPURunner = runner
	t.Cleanup(func() { companionCPURunner = old })
}

func TestCompanionBuildPromptKeepsRecentMessages(t *testing.T) {
	messages := make([]map[string]any, 0, companionPromptMaxMessages+10)
	for i := 0; i < companionPromptMaxMessages+10; i++ {
		messages = append(messages, map[string]any{
			"id":     map[string]any{"local_id": int64(i + 1)},
			"time":   "2026-06-08 10:00",
			"sender": "用户",
			"kind":   "text",
			"text":   "消息" + string(rune('A'+i%26)),
		})
	}
	messages[0]["text"] = "too-old-message"
	messages[len(messages)-1]["text"] = "latest-message"
	timeline := map[string]any{
		"query": map[string]any{
			"chat":         "AI Native",
			"display_name": "AI Native",
			"oldest_time":  "2026-06-08 09:00",
			"newest_time":  "2026-06-08 10:00",
		},
	}
	req := companionAskRequest{
		Chat:     "AI Native",
		Mode:     "value",
		Question: "有什么值得关注",
	}
	prompt := companionBuildPromptFromContexts(req, []companionChatContext{{
		Chat:        req.Chat,
		DisplayName: companionTimelineTitle(timeline, req.Chat),
		Timeline:    timeline,
		Messages:    messages,
	}}, nil)
	if !strings.Contains(prompt.User, "有什么值得关注") {
		t.Fatalf("prompt missing user question:\n%s", prompt.User)
	}
	if strings.Contains(prompt.User, "too-old-message") {
		t.Fatalf("prompt should keep the most recent messages only:\n%s", prompt.User)
	}
	if !strings.Contains(prompt.User, "latest-message") {
		t.Fatalf("prompt missing latest message:\n%s", prompt.User)
	}
	if len(prompt.User) > companionPromptMaxChars+1024 {
		t.Fatalf("prompt grew unexpectedly: %d", len(prompt.User))
	}
}

func TestCompanionBuildPromptAllowsNoChat(t *testing.T) {
	prompt := companionBuildPromptFromContexts(companionAskRequest{
		Mode:     "custom",
		Question: "直接问一个问题",
	}, nil, nil)
	if !strings.Contains(prompt.User, "当前没有预置微信聊天正文") {
		t.Fatalf("prompt should allow no selected chat:\n%s", prompt.User)
	}
	if !strings.Contains(prompt.User, "直接问一个问题") {
		t.Fatalf("prompt missing question:\n%s", prompt.User)
	}
}

func TestCompanionBuildPromptTreatsMentionsAsHintsOnly(t *testing.T) {
	prompt := companionBuildPromptFromContexts(companionAskRequest{
		Mode:     "custom",
		Question: "图片你没看吗",
		Chat:     "群A",
	}, nil, nil)
	if !strings.Contains(prompt.User, "用户通过 @ 提到的微信会话目标：群A") || !strings.Contains(prompt.User, "当前没有预置微信聊天正文") {
		t.Fatalf("prompt should treat mentions as target hints:\n%s", prompt.User)
	}
	if strings.Contains(prompt.System, "本机已挂载 wechat-cli") || strings.Contains(prompt.System, "直接调用 CLI") {
		t.Fatalf("system prompt should not duplicate the CPU execution boundary:\n%s", prompt.System)
	}
	if !strings.Contains(prompt.System, "不要声称已读到未读取的微信内容") {
		t.Fatalf("system prompt should keep the evidence boundary:\n%s", prompt.System)
	}
	for _, banned := range []string{"默认读 80", `"limit":20`, "没有可用终端"} {
		if strings.Contains(prompt.System, banned) {
			t.Fatalf("system prompt should stay thin and not teach %q:\n%s", banned, prompt.System)
		}
	}
	if strings.Contains(prompt.User, "聊天上下文片段") {
		t.Fatalf("prompt should not include chat body without agent tool calls:\n%s", prompt.User)
	}
}

func TestCompanionBuildPromptIncludesAttachmentsAsUserInput(t *testing.T) {
	prompt := companionBuildPromptFromContexts(companionAskRequest{
		Mode:     "custom",
		Question: "看下这个图和说明",
		Attachments: []companionAttachment{{
			Kind: "image",
			Name: "shot.png",
			MIME: "image/png",
			Size: 1234,
			Path: "/tmp/shot.png",
		}, {
			Kind:        "text",
			Name:        "note.md",
			MIME:        "text/markdown",
			Size:        42,
			Path:        "/tmp/note.md",
			TextPreview: "hello",
		}},
	}, nil, nil)
	for _, want := range []string{"用户本轮附加了文件/图片", "shot.png", "/tmp/shot.png", "note.md", "hello", "不能仅凭文件名臆测"} {
		if !strings.Contains(prompt.User, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt.User)
		}
	}
}

func TestCompanionAskFiltersUntrustedAttachmentPaths(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", stateDir)
	companionUseTestCPU(t, func(ctx context.Context, prompt companionPrompt, handoff map[string]any, req companionAskRequest, emit companionStreamEmitter) (companionCPUResult, error) {
		if strings.Contains(prompt.User, "/etc/passwd") || strings.Contains(fmt.Sprint(handoff), "/etc/passwd") {
			t.Fatalf("untrusted attachment path reached CPU prompt/handoff:\n%s\n%#v", prompt.User, handoff)
		}
		return companionCPUResult{Answer: "CPU answer", Meta: map[string]any{"cpu": "test"}}, nil
	})
	data, status, code, message := companionBuildAskData(context.Background(), companionAskRequest{
		Mode:     "custom",
		Question: "读这个附件",
		Attachments: []companionAttachment{{
			Kind: "text",
			Name: "passwd",
			MIME: "text/plain",
			Path: "/etc/passwd",
		}},
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d code=%s message=%s", status, code, message)
	}
	ctx := mapAny(data["context"])
	if strings.Contains(fmt.Sprint(ctx), "/etc/passwd") {
		t.Fatalf("untrusted attachment path leaked into context: %#v", ctx)
	}
	if strings.Contains(stringMapValue(data, "answer"), "/etc/passwd") {
		t.Fatalf("untrusted attachment path leaked into answer: %#v", data["answer"])
	}
}

func TestCompanionBuildPromptIncludesConversationHistory(t *testing.T) {
	prompt := companionBuildPromptFromContexts(companionAskRequest{
		Mode:     "custom",
		Question: "那这个怎么看",
		History: []companionHistory{{
			Role:    "user",
			Text:    "我和郑宇格最近聊了啥",
			Targets: []string{"郑宇格"},
		}, {
			Role: "assistant",
			Text: "上一轮提到交叉订单。",
		}},
	}, nil, nil)
	for _, want := range []string{"本轮之前的浏览器聊天历史", "用户（目标：郑宇格）：我和郑宇格最近聊了啥", "助手：上一轮提到交叉订单"} {
		if !strings.Contains(prompt.User, want) {
			t.Fatalf("prompt missing history %q:\n%s", want, prompt.User)
		}
	}
	if strings.Contains(prompt.User, "tool-json") || strings.Contains(prompt.User, "tool_calls") {
		t.Fatalf("prompt history should not include rendered tool internals:\n%s", prompt.User)
	}
}

func TestCompanionAskDelegatesToCPUWithoutLocalWechatRead(t *testing.T) {
	called := false
	companionUseTestCPU(t, func(ctx context.Context, prompt companionPrompt, handoff map[string]any, req companionAskRequest, emit companionStreamEmitter) (companionCPUResult, error) {
		called = true
		if req.Question != "我和郑宇格最近聊了啥" {
			t.Fatalf("question = %q", req.Question)
		}
		if !strings.Contains(prompt.User, "我和郑宇格最近聊了啥") {
			t.Fatalf("prompt missing question:\n%s", prompt.User)
		}
		if strings.Contains(prompt.User, "聊天上下文片段") {
			t.Fatalf("companion should not locally pre-read chat body:\n%s", prompt.User)
		}
		cpuPrompt := companionCPUUserPrompt(prompt, handoff)
		for _, want := range []string{"自己调用本机 wechat-cli", "读取条数、分页", "不要预设固定读取条数"} {
			if !strings.Contains(cpuPrompt, want) {
				t.Fatalf("CPU prompt missing %q:\n%s", want, cpuPrompt)
			}
		}
		return companionCPUResult{Answer: "CPU 自己决定读取路径后的回答", Meta: map[string]any{"cpu": "test"}}, nil
	})
	data, status, code, message := companionBuildAskData(context.Background(), companionAskRequest{
		Mode:     "custom",
		Question: "我和郑宇格最近聊了啥",
	}, nil)
	if status != http.StatusOK {
		t.Fatalf("status=%d code=%s message=%s", status, code, message)
	}
	if !called {
		t.Fatalf("CPU runner was not called")
	}
	if got := stringMapValue(data, "answer"); got != "CPU 自己决定读取路径后的回答" {
		t.Fatalf("answer = %q", got)
	}
	if calls, ok := data["tool_calls"].([]companionToolTrace); !ok || len(calls) != 0 {
		t.Fatalf("companion should not fabricate local tool calls: %#v", data["tool_calls"])
	}
}

func TestCompanionAskStreamsCPUEvents(t *testing.T) {
	companionUseTestCPU(t, func(ctx context.Context, prompt companionPrompt, handoff map[string]any, req companionAskRequest, emit companionStreamEmitter) (companionCPUResult, error) {
		if emit != nil {
			emit("cpu_log", map[string]any{"text": "CPU started"})
			emit("tool_start", map[string]any{"id": "tool-1", "tool": "Bash", "command": "wechat-cli sessions", "status": "running"})
			emit("tool_result", map[string]any{"id": "tool-1", "status": "completed", "summary": "found sessions"})
		}
		return companionCPUResult{Answer: "done"}, nil
	})
	events := []string{}
	_, status, code, message := companionBuildAskData(context.Background(), companionAskRequest{
		Mode:     "custom",
		Question: "最近在聊什么",
	}, func(event string, data map[string]any) {
		events = append(events, event+":"+strings.TrimSpace(firstNonEmpty(stringMapValue(data, "text"), stringMapValue(data, "summary"), stringMapValue(data, "command"))))
	})
	if status != http.StatusOK {
		t.Fatalf("status=%d code=%s message=%s", status, code, message)
	}
	got := strings.Join(events, "\n")
	for _, want := range []string{"cpu_log:CPU started", "tool_start:wechat-cli sessions", "tool_result:found sessions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("events missing %q:\n%s", want, got)
		}
	}
}

func TestCompanionAskCPUFailureSurfacesError(t *testing.T) {
	companionUseTestCPU(t, func(ctx context.Context, prompt companionPrompt, handoff map[string]any, req companionAskRequest, emit companionStreamEmitter) (companionCPUResult, error) {
		return companionCPUResult{}, fmt.Errorf("cpu offline")
	})
	data, status, code, message := companionBuildAskData(context.Background(), companionAskRequest{
		Mode:     "custom",
		Question: "你好",
	}, nil)
	if status != http.StatusFailedDependency || code != "cpu_error" || !strings.Contains(message, "cpu offline") {
		t.Fatalf("unexpected error status=%d code=%s message=%s data=%#v", status, code, message, data)
	}
}

func TestCompanionCPUDefaultsDoNotSelectCPU(t *testing.T) {
	for _, key := range []string{
		"WECHAT_CLI_COMPANION_CPU_POLICY",
		"WECHAT_CLI_COMPANION_CPU_PRIMARY",
		"WECHAT_CLI_COMPANION_CPU_FALLBACK",
		"WECHAT_CLI_COMPANION_CPU_CHAIN",
		"BABATA_CPU_POLICY",
		"BABATA_PRIMARY_CPU",
		"BABATA_FALLBACK_CPU",
		"BABATA_CPU_CHAIN",
	} {
		t.Setenv(key, "")
	}
	if companionCPUPolicy() != "" || companionCPUPrimary() != "" || companionCPUFallback() != "" || companionCPUChain() != "" {
		t.Fatalf("defaults policy=%q primary=%q fallback=%q chain=%q", companionCPUPolicy(), companionCPUPrimary(), companionCPUFallback(), companionCPUChain())
	}
}

func TestCompanionCPUSelectionAllowsCompanionEnvOverride(t *testing.T) {
	t.Setenv("WECHAT_CLI_COMPANION_CPU_POLICY", "codex")
	t.Setenv("WECHAT_CLI_COMPANION_CPU_PRIMARY", "claude")
	t.Setenv("WECHAT_CLI_COMPANION_CPU_FALLBACK", "codex")
	t.Setenv("WECHAT_CLI_COMPANION_CPU_CHAIN", "codex,claude")
	if companionCPUPolicy() != "codex" || companionCPUPrimary() != "claude" || companionCPUFallback() != "codex" || companionCPUChain() != "codex,claude" {
		t.Fatalf("overrides policy=%q primary=%q fallback=%q chain=%q", companionCPUPolicy(), companionCPUPrimary(), companionCPUFallback(), companionCPUChain())
	}
}

func TestCompanionCPULogFilterHidesRoutineStartup(t *testing.T) {
	hidden := []string{
		"--- begin Codex output (json -> ~/.wechat-cli/companion-cpu/codex-stream.jsonl, model=gpt-5.5) ---",
		"--- Codex headless home: ~/cc-workspace/state/codex-home/babata-cpu (scope=babata-cpu) ---",
		"--- end Codex output (exit: 0) ---",
	}
	for _, line := range hidden {
		if companionShouldShowCPULog(line) {
			t.Fatalf("routine startup log should be hidden: %q", line)
		}
	}
	visible := []string{
		"--- claude unavailable (claude_exit=1 quota_or_auth_or_circuit_break); falling back to codex ---",
		"[babata-cpu] timeout after 180s; terminating pid=123",
		"babata-cpu: --prompt-file required",
	}
	for _, line := range visible {
		if !companionShouldShowCPULog(line) {
			t.Fatalf("diagnostic log should be visible: %q", line)
		}
	}
}

func TestCompanionCPUStreamParsesClaudeToolEvents(t *testing.T) {
	state := newCompanionCPUStreamState()
	events := []string{}
	emit := func(event string, data map[string]any) {
		events = append(events, event+":"+firstNonEmpty(
			stringMapValue(data, "text"),
			strings.Join([]string{
				stringMapValue(data, "id"),
				stringMapValue(data, "command"),
				stringMapValue(data, "status"),
				stringMapValue(data, "summary"),
			}, "|"),
		))
	}
	companionHandleCPUStreamLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"先看一下。"},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"wechat-cli sessions --limit 5"}}]}}`, state, emit)
	companionHandleCPUStreamLine(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"found sessions"}]}}`, state, emit)
	got := strings.Join(events, "\n")
	for _, want := range []string{"assistant_delta:先看一下。", "tool_start:toolu_1|wechat-cli sessions|running|正在查看最近会话", "tool_result:toolu_1|wechat-cli sessions|completed|found sessions"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in events:\n%s", want, got)
		}
	}
}

func TestCompanionCPUStreamParsesCodexAgentMessagesBetweenTools(t *testing.T) {
	state := newCompanionCPUStreamState()
	events := []string{}
	emit := func(event string, data map[string]any) {
		events = append(events, event+":"+firstNonEmpty(
			stringMapValue(data, "text"),
			strings.Join([]string{
				stringMapValue(data, "id"),
				stringMapValue(data, "command"),
				stringMapValue(data, "status"),
				stringMapValue(data, "summary"),
			}, "|"),
		))
	}
	companionHandleCPUStreamLine(`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"我先确认会话。"}}`, state, emit)
	companionHandleCPUStreamLine(`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"wechat-cli sessions --keyword 郑宇格"}}`, state, emit)
	companionHandleCPUStreamLine(`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"wechat-cli sessions --keyword 郑宇格","status":"completed","exit_code":0,"aggregated_output":"ok sessions"}}`, state, emit)
	companionHandleCPUStreamLine(`{"type":"item.completed","item":{"id":"item_2","type":"agent_message","text":"再读时间线。"}}`, state, emit)
	got := strings.Join(events, "\n")
	wantOrder := []string{
		"assistant_delta:我先确认会话。",
		"tool_start:cmd_1|wechat-cli sessions|running|正在查看最近会话",
		"tool_result:cmd_1|wechat-cli sessions|completed|ok sessions",
		"assistant_delta:再读时间线。",
	}
	last := -1
	for _, want := range wantOrder {
		idx := strings.Index(got, want)
		if idx < 0 {
			t.Fatalf("missing %q in events:\n%s", want, got)
		}
		if idx <= last {
			t.Fatalf("event order wrong for %q in:\n%s", want, got)
		}
		last = idx
	}
}

func TestCompanionCPUStreamParsesCodexCommandEvents(t *testing.T) {
	state := newCompanionCPUStreamState()
	events := []string{}
	emit := func(event string, data map[string]any) {
		events = append(events, event+":"+strings.Join([]string{
			stringMapValue(data, "id"),
			stringMapValue(data, "command"),
			stringMapValue(data, "status"),
			stringMapValue(data, "summary"),
		}, "|"))
	}
	companionHandleCPUStreamLine(`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"wechat-cli timeline --chat selected"}}`, state, emit)
	companionHandleCPUStreamLine(`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"wechat-cli timeline --chat selected","status":"completed","exit_code":0,"aggregated_output":"ok timeline"}}`, state, emit)
	got := strings.Join(events, "\n")
	for _, want := range []string{"tool_start:cmd_1|wechat-cli timeline|running|正在读取聊天记录", "tool_result:cmd_1|wechat-cli timeline|completed|ok timeline"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in events:\n%s", want, got)
		}
	}
}

func TestCompanionCPUCommandInfoParsesWechatTimeline(t *testing.T) {
	info, ok := companionWechatCPUCommandInfo(`/bin/zsh -lc 'PATH="/Users/admin/.local/bin:$PATH" wechat-cli timeline "wxid_private" --limit 80 --offset 240 --display-order asc --pretty | jq -c ".data.messages[]"'`)
	if !ok {
		t.Fatalf("command should parse as wechat-cli")
	}
	if info.Tool != "chat_timeline" || info.Label != "读取聊天记录" || info.DisplayCommand != "wechat-cli timeline" {
		t.Fatalf("unexpected command info: %#v", info)
	}
	if got := fmt.Sprint(info.Args["chat"]); got != "selected" {
		t.Fatalf("chat arg should be redacted, got %#v", info.Args)
	}
	if intMapValue(info.Args, "limit") != 80 || intMapValue(info.Args, "offset") != 240 || fmt.Sprint(info.Args["display_order"]) != "asc" {
		t.Fatalf("timeline args not preserved: %#v", info.Args)
	}
	versionInfo, ok := companionWechatCPUCommandInfo(`/bin/zsh -lc '/Users/admin/.local/share/wechat-cli/wechat-cli --version'`)
	if !ok || versionInfo.Tool != "version" || versionInfo.DisplayCommand != "wechat-cli version" {
		t.Fatalf("version command info = %#v ok=%v", versionInfo, ok)
	}
	if _, ok := versionInfo.Args["chat"]; ok {
		t.Fatalf("version command should not invent chat args: %#v", versionInfo.Args)
	}
}

func TestCompanionCodexToolEventWrapsWechatEnvelope(t *testing.T) {
	state := newCompanionCPUStreamState()
	var result map[string]any
	emit := func(event string, data map[string]any) {
		if event == "tool_result" {
			result = data
		}
	}
	companionHandleCPUStreamLine(`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"wechat-cli sessions --limit 2"}}`, state, emit)
	output := `{"ok":true,"tool":"sessions","data":{"sessions":[{"display_name":"会话一"},{"display_name":"会话二"}]}}`
	companionHandleCPUStreamLine(fmt.Sprintf(`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"wechat-cli sessions --limit 2","status":"completed","exit_code":0,"aggregated_output":%q}}`, output), state, emit)
	if result == nil {
		t.Fatalf("missing tool_result")
	}
	if stringMapValue(result, "label") != "查看最近会话" || stringMapValue(result, "command") != "wechat-cli sessions" {
		t.Fatalf("tool result should be semantically wrapped: %#v", result)
	}
	if !strings.Contains(stringMapValue(result, "summary"), "2 个候选") {
		t.Fatalf("summary should include session count: %#v", result)
	}
	meta := mapAny(result["result"])
	if intMapValue(meta, "session_count") != 2 {
		t.Fatalf("result meta = %#v", meta)
	}
}

func TestCompanionCodexToolEventWrapsContactsEnvelope(t *testing.T) {
	state := newCompanionCPUStreamState()
	var result map[string]any
	emit := func(event string, data map[string]any) {
		if event == "tool_result" {
			result = data
		}
	}
	companionHandleCPUStreamLine(`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":"wechat-cli contacts --keyword Alice --limit 1"}}`, state, emit)
	output := `{"ok":true,"tool":"contacts","data":{"query":{"keyword":"Alice","returned":1},"contacts":[{"display_name":"Alice"}]}}`
	companionHandleCPUStreamLine(fmt.Sprintf(`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":"wechat-cli contacts --keyword Alice --limit 1","status":"completed","exit_code":0,"aggregated_output":%q}}`, output), state, emit)
	if result == nil {
		t.Fatalf("missing tool_result")
	}
	if stringMapValue(result, "label") != "查找联系人" || stringMapValue(result, "command") != "wechat-cli contacts" {
		t.Fatalf("contacts result should be readable: %#v", result)
	}
	if !strings.Contains(stringMapValue(result, "summary"), "1 个结果") {
		t.Fatalf("contacts summary should include count: %#v", result)
	}
	meta := mapAny(result["result"])
	if intMapValue(meta, "contact_count") != 1 {
		t.Fatalf("contacts meta = %#v", meta)
	}
}

func TestCompanionCodexToolEventWrapsVersionEnvelope(t *testing.T) {
	state := newCompanionCPUStreamState()
	var result map[string]any
	emit := func(event string, data map[string]any) {
		if event == "tool_result" {
			result = data
		}
	}
	command := `/bin/zsh -lc '/Users/admin/.local/share/wechat-cli/wechat-cli --version'`
	companionHandleCPUStreamLine(fmt.Sprintf(`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":%q}}`, command), state, emit)
	output := `{"ok":true,"tool":"version","command":"version","data":{"name":"wechat-cli","version":"1.6.19"}}`
	companionHandleCPUStreamLine(fmt.Sprintf(`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":%q,"status":"completed","exit_code":0,"aggregated_output":%q}}`, command, output), state, emit)
	if stringMapValue(result, "label") != "检查版本" || stringMapValue(result, "command") != "wechat-cli version" {
		t.Fatalf("version result should be readable: %#v", result)
	}
	if !strings.Contains(stringMapValue(result, "summary"), "wechat-cli 1.6.19") {
		t.Fatalf("version summary should include version: %#v", result)
	}
}

func TestCompanionCodexToolEventWrapsJSONLineMessages(t *testing.T) {
	singleRow := `{"time":"10:00","sender":"张三","kind":"text","text":"单条"}`
	if _, _, _, _, ok := companionParseToolEnvelope(singleRow); ok {
		t.Fatalf("single message JSON row should not parse as a tool envelope")
	}
	if rows := companionParseJSONLineMessages(singleRow); len(rows) != 1 {
		t.Fatalf("single message JSON row should parse as JSONL message, got %#v", rows)
	}
	state := newCompanionCPUStreamState()
	var result map[string]any
	emit := func(event string, data map[string]any) {
		if event == "tool_result" {
			result = data
		}
	}
	command := `/bin/zsh -lc 'wechat-cli timeline "wxid_private" --limit 2 | jq -c ".data.messages[]"'`
	companionHandleCPUStreamLine(fmt.Sprintf(`{"type":"item.started","item":{"id":"cmd_1","type":"command_execution","command":%q}}`, command), state, emit)
	output := strings.Join([]string{
		`{"time":"10:00","sender":"张三","kind":"text","text":"第一条"}`,
		`{"time":"10:01","sender":"李四","kind":"text","text":"第二条"}`,
	}, "\n")
	companionHandleCPUStreamLine(fmt.Sprintf(`{"type":"item.completed","item":{"id":"cmd_1","type":"command_execution","command":%q,"status":"completed","exit_code":0,"aggregated_output":%q}}`, command, output), state, emit)
	if result == nil {
		t.Fatalf("missing tool_result")
	}
	if stringMapValue(result, "label") != "读取聊天记录" || stringMapValue(result, "command") != "wechat-cli timeline" {
		t.Fatalf("tool result should hide raw shell command: %#v", result)
	}
	meta := mapAny(result["result"])
	if intMapValue(meta, "message_count") != 2 {
		t.Fatalf("message count missing: %#v", result)
	}
	samples := mapSliceAny(result["samples"])
	if len(samples) != 2 || !strings.Contains(stringMapValue(samples[1], "text"), "第二条") {
		t.Fatalf("samples missing readable message previews: %#v", samples)
	}
}

func TestCompanionAskChatsDedupes(t *testing.T) {
	got := companionAskChats(companionAskRequest{
		Chat:  "chat-a",
		Chats: []string{"chat-a", " chat-b ", "", "chat-b"},
	})
	want := []string{"chat-a", "chat-b"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q; all=%#v", i, got[i], want[i], got)
		}
	}
}

func TestCompanionCLIChildEnvWhitelistsLaunchdServiceEnv(t *testing.T) {
	dbRoot := filepath.Join(t.TempDir(), "db")
	t.Setenv("WECHAT_CLI_DB_ROOT", dbRoot)
	t.Setenv("WXKEY_TEST_OPTION", "1")
	t.Setenv("XPC_SERVICE_NAME", "com.r266.wechat-cli-companion")
	t.Setenv("XPC_FLAGS", "1")

	env := companionCLIChildEnv(true)
	if value, ok := companionTestEnvValue(env, "WECHAT_CLI_DB_ROOT"); !ok || value != dbRoot {
		t.Fatalf("WECHAT_CLI_DB_ROOT not preserved in env: %#v", env)
	}
	if value, ok := companionTestEnvValue(env, "WXKEY_TEST_OPTION"); !ok || value != "1" {
		t.Fatalf("WXKEY_TEST_OPTION not preserved in env: %#v", env)
	}
	if _, ok := companionTestEnvValue(env, "XPC_SERVICE_NAME"); ok {
		t.Fatalf("launchd service env should not be inherited: %#v", env)
	}
	if _, ok := companionTestEnvValue(env, "XPC_FLAGS"); ok {
		t.Fatalf("launchd flags should not be inherited: %#v", env)
	}
	if value, ok := companionTestEnvValue(env, "WECHAT_CLI_STRICT_READ_ONLY"); !ok || value != "1" {
		t.Fatalf("strict read-only not set in env: %#v", env)
	}
	path, ok := companionTestEnvValue(env, "PATH")
	if !ok || !strings.Contains(path, "/usr/bin") {
		t.Fatalf("PATH should include a stable system path, got %q", path)
	}
}

func companionTestEnvValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix), true
		}
	}
	return "", false
}

func TestCompanionToolTraceSummarizesTimeline(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "/tmp"
	}
	imagePath := home + "/private/image.png"
	trace := companionBuildToolTrace("tool-1", "chat_timeline", "companion timeline", map[string]any{
		"chat":           "wxid_private",
		"limit":          24,
		"include_images": false,
	}, map[string]any{
		"query": map[string]any{
			"display_name": "AI Native 群",
			"oldest_time":  "2026-06-09 10:00",
			"newest_time":  "2026-06-09 10:05",
		},
		"messages": []map[string]any{{
			"time":   "10:00",
			"sender": "张三",
			"kind":   "text",
			"text":   "讨论上下文工程",
		}, {
			"time":   "10:05",
			"sender": "李四",
			"kind":   "image",
			"text":   imagePath,
			"images": []map[string]any{{"path": imagePath}},
			"link":   map[string]any{"title": "参考资料"},
		}},
	}, "", nil, 12*time.Millisecond)
	if trace.Status != "completed" || trace.Tool != "chat_timeline" {
		t.Fatalf("unexpected trace status/tool: %#v", trace)
	}
	if !strings.Contains(trace.Summary, "AI Native 群") || !strings.Contains(trace.Summary, "图片 1") || !strings.Contains(trace.Summary, "链接 1") {
		t.Fatalf("trace summary missing chat/media context: %#v", trace)
	}
	media := mapAny(trace.Result["media"])
	if intMapValue(media, "images") != 1 || intMapValue(media, "links") != 1 {
		t.Fatalf("trace media counts = %#v", media)
	}
	if len(trace.Samples) != 2 || !strings.Contains(stringMapValue(trace.Samples[1], "text"), "~/private/image.png") {
		t.Fatalf("trace samples should redact home path and keep message preview: %#v", trace.Samples)
	}
}

func TestCompanionToolTraceKeepsAllSessionNames(t *testing.T) {
	trace := companionBuildToolTrace("tool-1", "sessions", "companion sessions", map[string]any{
		"limit": 3,
	}, map[string]any{
		"sessions": []map[string]any{{
			"display_name": "群一",
		}, {
			"display_name": "群二",
		}, {
			"display_name": "群三",
		}},
	}, "", nil, 3*time.Millisecond)
	sessions, ok := trace.Result["sessions"].([]string)
	if !ok {
		t.Fatalf("trace sessions type = %T; trace=%#v", trace.Result["sessions"], trace)
	}
	if len(sessions) != 3 || sessions[2] != "群三" {
		t.Fatalf("trace should keep every returned session name: %#v", sessions)
	}
}

func TestCompanionCLIMountEnvMountsCurrentCLI(t *testing.T) {
	env := companionCLIMountEnv([]string{"PATH=/usr/bin"})
	exe, err := os.Executable()
	if err != nil || exe == "" {
		t.Fatalf("os.Executable = %q/%v", exe, err)
	}
	values := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	if values["WECHAT_CLI_BIN"] != exe || values["WECHAT_CLI_COMPANION_BIN"] != exe {
		t.Fatalf("CLI bin env not mounted: %#v", values)
	}
	parts := strings.Split(values["PATH"], string(os.PathListSeparator))
	if len(parts) == 0 || parts[0] != filepath.Dir(exe) {
		t.Fatalf("PATH should begin with current CLI dir, got %q", values["PATH"])
	}
}

func TestCompanionAPIGuardRequiresPageToken(t *testing.T) {
	handler := newCompanionHandler()
	token := companionTestToken(t, handler)

	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing token status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/status", nil)
	companionAuthorizeTestRequest(req, token)
	req.Header.Set("Origin", "http://evil.test")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross origin status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/ask", strings.NewReader("hello"))
	companionAuthorizeTestRequest(req, token)
	req.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("plain post status = %d", rec.Code)
	}
}

func TestCompanionUploadHandlerStoresAttachment(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("WECHAT_CLI_STATE_DIR", stateDir)
	handler := newCompanionHandler()
	token := companionTestToken(t, handler)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files", "note.md")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := io.WriteString(part, "# hello\nworld"); err != nil {
		t.Fatalf("write part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	companionAuthorizeTestRequest(req, token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	data := mapAny(payload["data"])
	attachments := mapSliceAny(data["attachments"])
	if len(attachments) != 1 {
		t.Fatalf("attachments = %#v", attachments)
	}
	got := attachments[0]
	path := stringMapValue(got, "path")
	if !strings.HasPrefix(path, filepath.Join(stateDir, "companion-uploads")) {
		t.Fatalf("upload path outside state dir: %q", path)
	}
	if stringMapValue(got, "kind") != "text" || !strings.Contains(stringMapValue(got, "text_preview"), "hello") {
		t.Fatalf("attachment metadata = %#v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("uploaded file missing: %v", err)
	}
	previewURL := stringMapValue(got, "url")
	if previewURL == "" {
		t.Fatalf("missing preview url: %#v", got)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, previewURL+"?token="+token, nil)
	req.Host = "127.0.0.1"
	req.Header.Set("Origin", "http://127.0.0.1")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "world") {
		t.Fatalf("preview status=%d body=%q", rec.Code, rec.Body.String())
	}
	if _, err := companionAttachmentPathFromURL("../../etc/passwd"); err == nil {
		t.Fatalf("path traversal should be rejected")
	}
}

func TestCompanionDesktopJXAContainsEscapedURL(t *testing.T) {
	script := companionDesktopJXA(`http://127.0.0.1:18789/?q="微信"`)
	if !strings.Contains(script, "WKWebView") {
		t.Fatalf("desktop script should use WKWebView:\n%s", script)
	}
	if !strings.Contains(script, `http://127.0.0.1:18789/?q=\"微信\"`) {
		t.Fatalf("desktop script should JSON-escape URL:\n%s", script)
	}
}

func TestCompanionIndexHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	newCompanionHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "微信助手 V1") {
		t.Fatalf("index body missing title")
	}
	if strings.Contains(rec.Body.String(), `data-mode=`) {
		t.Fatalf("index body should not expose preset action buttons")
	}
	if !strings.Contains(rec.Body.String(), "问问微信") {
		t.Fatalf("index body missing command input placeholder")
	}
	if !strings.Contains(rec.Body.String(), `<textarea id="questionInput"`) || strings.Contains(rec.Body.String(), `<input id="questionInput"`) {
		t.Fatalf("index body should use a chat-style editable composer textarea")
	}
	if strings.Contains(rec.Body.String(), "customAskBtn") {
		t.Fatalf("index body should not keep temporary ask button id")
	}
	if !strings.Contains(rec.Body.String(), `id="askBtn"`) || !strings.Contains(rec.Body.String(), `aria-label="发送问题"`) {
		t.Fatalf("index body missing stable ask button accessibility")
	}
	if strings.Contains(rec.Body.String(), `el("questionInput").disabled = state.asking`) {
		t.Fatalf("index body should keep the draft input editable while the agent is replying")
	}
	if !strings.Contains(rec.Body.String(), "renderMarkdown") || !strings.Contains(rec.Body.String(), `class="markdown"`) || !strings.Contains(rec.Body.String(), "sanitizeMarkdownURL") {
		t.Fatalf("index body should render assistant answers as sanitized markdown")
	}
	if !strings.Contains(rec.Body.String(), "renderToolCalls") || !strings.Contains(rec.Body.String(), "tool_calls") || !strings.Contains(rec.Body.String(), "tool-call") {
		t.Fatalf("index body should render companion CLI calls as visible tool traces")
	}
	if !strings.Contains(rec.Body.String(), "renderToolArgs") || !strings.Contains(rec.Body.String(), "display_command") {
		t.Fatalf("index body should render readable tool card args and display commands")
	}
	if !strings.Contains(rec.Body.String(), "toolCardSubtitle") || !strings.Contains(rec.Body.String(), "tool-subtitle") || !strings.Contains(rec.Body.String(), "toolCardStatus") {
		t.Fatalf("index body should make collapsed tool cards readable")
	}
	if strings.Contains(rec.Body.String(), "title + \" · \" + command") {
		t.Fatalf("collapsed tool card title should not be dominated by raw commands")
	}
	if !strings.Contains(rec.Body.String(), "eventsHTML + renderMarkdown(residualMessageText(message))") {
		t.Fatalf("index body should keep the streamed-event render path")
	}
	if !strings.Contains(rec.Body.String(), "residualMessageText") || !strings.Contains(rec.Body.String(), "text.trim() === streamed.trim()") {
		t.Fatalf("index body should avoid duplicating streamed assistant deltas after final answer")
	}
	if !strings.Contains(rec.Body.String(), "renderToolDetails") || !strings.Contains(rec.Body.String(), "tool-json") {
		t.Fatalf("index body should expose full collapsed tool details")
	}
	if strings.Contains(rec.Body.String(), `class="' + escapeHtml(cls) + '" open`) {
		t.Fatalf("tool call cards should be collapsed by default")
	}
	if !strings.Contains(rec.Body.String(), "/api/ask-stream") || !strings.Contains(rec.Body.String(), "apiStream") {
		t.Fatalf("index body should stream tool traces before the final agent answer")
	}
	if !strings.Contains(rec.Body.String(), "appendProcessEvent") || !strings.Contains(rec.Body.String(), "cpu_log") || !strings.Contains(rec.Body.String(), "tool_start") || !strings.Contains(rec.Body.String(), "tool_result") {
		t.Fatalf("index body should render CPU process logs and streamed tool cards")
	}
	if !strings.Contains(rec.Body.String(), "companionRequestHistory") || !strings.Contains(rec.Body.String(), "history: history") {
		t.Fatalf("index body should send trimmed browser conversation history to the CPU")
	}
	if !strings.Contains(rec.Body.String(), "sanitizeConversationHistorySessions") || !strings.Contains(rec.Body.String(), "isRoutineCPULogText") {
		t.Fatalf("index body should sanitize previously persisted routine CPU logs")
	}
	if !strings.Contains(rec.Body.String(), "X-Wechat-Companion-Token") || !strings.Contains(rec.Body.String(), "COMPANION_TOKEN") || strings.Contains(rec.Body.String(), companionTokenPlaceholder) {
		t.Fatalf("index body should inject and use a per-page API token")
	}
	if !strings.Contains(rec.Body.String(), "resizeQuestionInput") || !strings.Contains(rec.Body.String(), "!event.shiftKey") {
		t.Fatalf("index body should support a multiline chat composer")
	}
	if !strings.Contains(rec.Body.String(), `id="conversation"`) || !strings.Contains(rec.Body.String(), "renderConversation") {
		t.Fatalf("index body should render a chat-style conversation surface")
	}
	if !strings.Contains(rec.Body.String(), `id="historyList"`) || !strings.Contains(rec.Body.String(), `id="newChatBtn"`) || !strings.Contains(rec.Body.String(), "wechat_assistant_chat_sessions_v4") {
		t.Fatalf("index body should expose local display-only conversation history")
	}
	if !strings.Contains(rec.Body.String(), "wechat_assistant_chat_sessions_v1") || !strings.Contains(rec.Body.String(), "wechat_assistant_chat_sessions_v2") || !strings.Contains(rec.Body.String(), "wechat_assistant_chat_sessions_v3") || !strings.Contains(rec.Body.String(), "clearLegacyConversationHistory") {
		t.Fatalf("index body should clear legacy provider-era conversation history")
	}
	if !strings.Contains(rec.Body.String(), `id="sidebarToggleBtn"`) || !strings.Contains(rec.Body.String(), "sidebar-collapsed") || !strings.Contains(rec.Body.String(), "wechat_assistant_sidebar_collapsed_v1") {
		t.Fatalf("index body should allow collapsing the conversation history sidebar")
	}
	if !strings.Contains(rec.Body.String(), "deleteConversation") || !strings.Contains(rec.Body.String(), "activateConversation") {
		t.Fatalf("index body should support continuing and deleting historical conversations")
	}
	if !strings.Contains(rec.Body.String(), `id="menuSheet"`) || !strings.Contains(rec.Body.String(), `id="menuSettingsBtn"`) {
		t.Fatalf("index body should route settings through the top menu")
	}
	if strings.Contains(rec.Body.String(), "选择会话") || strings.Contains(rec.Body.String(), `id="menuSessionsBtn"`) || strings.Contains(rec.Body.String(), `id="historySheet"`) {
		t.Fatalf("index body should not expose manual session selection")
	}
	if strings.Contains(rec.Body.String(), "伴侣回答") || strings.Contains(rec.Body.String(), "会话历史") || strings.Contains(rec.Body.String(), "查看最近消息") {
		t.Fatalf("index body should not keep the old answer-card/history UI")
	}
	if strings.Contains(rec.Body.String(), `id="historyBtn"`) || strings.Contains(rec.Body.String(), `id="settingsBtn"`) || strings.Contains(rec.Body.String(), `id="expandBtn"`) {
		t.Fatalf("index body should not expose old bottom nav or expand controls")
	}
	if strings.Contains(rec.Body.String(), "message-targets") {
		t.Fatalf("sent chat bubbles should not render selected @ target chips")
	}
	if !strings.Contains(rec.Body.String(), `id="settingsState"`) || !strings.Contains(rec.Body.String(), "CLI 挂载") {
		t.Fatalf("index body should show CLI mount state")
	}
	for _, banned := range []string{
		"providerLabel",
		`id="codexModeBtn"`,
		`id="agentModeBtn"`,
		`id="testProviderBtn"`,
		"/api/provider-test",
		"app-server",
		`agent_session`,
		"providerBaseURL",
		"state.providerMode",
		"wechat_companion_codex_base_url",
		"wechat_companion_agent_endpoint",
		`id="modelInput"`,
		`model: el("`,
		"API Key",
		"API URL",
	} {
		if strings.Contains(rec.Body.String(), banned) {
			t.Fatalf("index body should not expose provider/session residue %q", banned)
		}
	}
	if !strings.Contains(rec.Body.String(), "mentionBox") || !strings.Contains(rec.Body.String(), "mentionMatches") {
		t.Fatalf("index body should expose @ mention completion")
	}
	if !strings.Contains(rec.Body.String(), "pinyinInitials") || !strings.Contains(rec.Body.String(), `"郑": "z"`) {
		t.Fatalf("index body should expose pinyin-initial mention matching")
	}
	if !strings.Contains(rec.Body.String(), `id="attachBtn"`) || !strings.Contains(rec.Body.String(), `id="attachmentInput"`) || !strings.Contains(rec.Body.String(), "addAttachmentFiles") || !strings.Contains(rec.Body.String(), "clipboardData") {
		t.Fatalf("index body should support choosing and pasting attachments")
	}
	if !strings.Contains(rec.Body.String(), "dragover") || !strings.Contains(rec.Body.String(), "renderDraftAttachments") || !strings.Contains(rec.Body.String(), "renderMessageAttachments") {
		t.Fatalf("index body should support drag/drop and visible attachment chips")
	}
	for _, banned := range []string{"provider_error", "provider_used", "compactProviderFailure", "providerErrorTool", "CPU 调用失败", "已准备好交给后端 CPU 的输入"} {
		if strings.Contains(rec.Body.String(), banned) {
			t.Fatalf("index body should not render provider failure residue %q", banned)
		}
	}
	if strings.Contains(rec.Body.String(), "最近 86 条") {
		t.Fatalf("index body should not promise a fixed context size")
	}
}

func TestCompanionFaviconHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rec := httptest.NewRecorder()
	newCompanionHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
