package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	companionDefaultAddr          = "127.0.0.1:18789"
	companionDefaultTimelineLimit = 80
	companionMaxTimelineLimit     = 300
	companionPromptMaxMessages    = 90
	companionPromptMaxChars       = 28000
	companionMessageTextMaxChars  = 700
	companionUploadRequestMaxSize = 128 << 20
	companionUploadFileMaxSize    = 64 << 20
	companionAttachmentTextMax    = 6000
	companionCPUDefaultTimeout    = 180 * time.Second
	companionCPUShutdownGrace     = 15 * time.Second
	companionCPUHistoryMaxItems   = 24
	companionCPUHistoryTextMax    = 3000
	companionCPULogTextMax        = 420
	companionCPUToolOutputMax     = 900
)

type companionAskRequest struct {
	Chat        string                `json:"chat"`
	Chats       []string              `json:"chats"`
	Mode        string                `json:"mode"`
	Question    string                `json:"question"`
	Limit       int                   `json:"limit"`
	Attachments []companionAttachment `json:"attachments"`
	History     []companionHistory    `json:"history"`
}

type companionTimelineRequest struct {
	Chat  string `json:"chat"`
	Limit int    `json:"limit"`
}

type companionAttachment struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	MIME        string `json:"mime"`
	Size        int64  `json:"size"`
	Path        string `json:"path"`
	URL         string `json:"url,omitempty"`
	TextPreview string `json:"text_preview,omitempty"`
}

type companionHistory struct {
	Role        string                `json:"role"`
	Text        string                `json:"text"`
	Targets     []string              `json:"targets,omitempty"`
	Attachments []companionAttachment `json:"attachments,omitempty"`
}

type companionChatContext struct {
	Chat        string
	DisplayName string
	Timeline    map[string]any
	Messages    []map[string]any
	Error       string
}

type companionCLIMount struct {
	Command     string         `json:"command"`
	Binary      string         `json:"binary,omitempty"`
	PathPrepend string         `json:"path_prepend,omitempty"`
	Env         map[string]any `json:"env,omitempty"`
}

type companionToolTrace struct {
	ID         string           `json:"id"`
	Tool       string           `json:"tool"`
	Command    string           `json:"command"`
	Status     string           `json:"status"`
	Label      string           `json:"label"`
	Summary    string           `json:"summary"`
	Args       map[string]any   `json:"args,omitempty"`
	Result     map[string]any   `json:"result,omitempty"`
	Samples    []map[string]any `json:"samples,omitempty"`
	Error      string           `json:"error,omitempty"`
	DurationMS int64            `json:"duration_ms"`
}

type companionCPUCommandInfo struct {
	Tool           string
	Label          string
	DisplayCommand string
	Args           map[string]any
	KnownWechat    bool
}

type companionCPUResult struct {
	Answer string
	Meta   map[string]any
}

type companionStreamEmitter func(event string, data map[string]any)
type companionCPURunnerFunc func(ctx context.Context, prompt companionPrompt, handoff map[string]any, req companionAskRequest, emit companionStreamEmitter) (companionCPUResult, error)

var companionCPURunner companionCPURunnerFunc = companionRunCPU

type companionServer struct {
	token string
}

const companionTokenPlaceholder = "__WECHAT_COMPANION_TOKEN__"

func runCompanionCLI(args []string, opts cliOptions) {
	flags := parseKVFlags(args)
	addr := firstNonEmpty(getStr(flags, "addr"), getStr(flags, "listen"), envFirst("WECHAT_CLI_COMPANION_ADDR"))
	if addr == "" {
		addr = companionDefaultAddr
	}
	addr, err := normalizeCompanionAddr(addr)
	if err != nil {
		exitCLIError(opts, 1, "invalid_argument", err.Error(), "companion", "companion")
	}
	if err := validateCompanionAddr(addr, getBoolDefault(flags, "allow_remote", false)); err != nil {
		exitCLIError(opts, 1, "invalid_argument", err.Error(), "companion", "companion")
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		exitCLIError(opts, 1, "companion_error", err.Error(), "companion", "companion")
	}
	localURL := companionURLFromListener(listener)
	srv := &http.Server{
		Handler:           newCompanionHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	if getBoolDefault(flags, "open", true) && !getBoolDefault(flags, "no_open", false) {
		go func() {
			time.Sleep(250 * time.Millisecond)
			if shouldOpenCompanionDesktop(flags) {
				if err := openCompanionDesktop(localURL); err == nil {
					return
				} else {
					fmt.Fprintf(os.Stderr, "[%s] desktop window launch failed: %v; falling back to browser\n", appName, err)
				}
			}
			_ = openCompanionBrowser(localURL)
		}()
	}

	fmt.Fprintf(os.Stderr, "[%s] companion listening on %s\n", appName, localURL)
	fmt.Fprintf(os.Stderr, "[%s] read-only sidecar; Ctrl+C to stop\n", appName)
	if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
		exitCLIError(opts, 1, "companion_error", err.Error(), "companion", "companion")
	}
}

func normalizeCompanionAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return companionDefaultAddr, nil
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		u, err := url.Parse(addr)
		if err != nil {
			return "", err
		}
		addr = u.Host
	}
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr, nil
	}
	if !strings.Contains(addr, ":") {
		return "127.0.0.1:" + addr, nil
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return "", fmt.Errorf("invalid companion addr %q: %w", addr, err)
	}
	return addr, nil
}

func validateCompanionAddr(addr string, allowRemote bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid companion addr %q: %w", addr, err)
	}
	if allowRemote || host == "" || strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("companion only listens on loopback by default; use --addr 127.0.0.1:18789 or pass --allow-remote explicitly")
}

func companionURLFromListener(listener net.Listener) string {
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return "http://" + listener.Addr().String()
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func openCompanionBrowser(localURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", localURL).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", localURL).Start()
	default:
		return exec.Command("xdg-open", localURL).Start()
	}
}

func shouldOpenCompanionDesktop(flags map[string]any) bool {
	if getBoolDefault(flags, "browser", false) {
		return false
	}
	return getBoolDefault(flags, "desktop", runtime.GOOS == "darwin")
}

func openCompanionDesktop(localURL string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("desktop companion window is currently implemented for macOS")
	}
	return exec.Command("osascript", "-l", "JavaScript", "-e", companionDesktopJXA(localURL)).Start()
}

func companionDesktopJXA(localURL string) string {
	escapedURL, _ := json.Marshal(localURL)
	return fmt.Sprintf(`
ObjC.import('Cocoa');
ObjC.import('WebKit');

const app = $.NSApplication.sharedApplication;
app.setActivationPolicy($.NSApplicationActivationPolicyRegular);

const style =
  $.NSWindowStyleMaskTitled |
  $.NSWindowStyleMaskClosable |
  $.NSWindowStyleMaskMiniaturizable |
  $.NSWindowStyleMaskResizable |
  $.NSWindowStyleMaskFullSizeContentView;
const screens = $.NSScreen.screens;
let screenFrame = $.NSScreen.mainScreen.visibleFrame;
for (let i = 0; i < screens.count; i++) {
  const candidate = screens.objectAtIndex(i).visibleFrame;
  if (candidate.origin.x === 0) {
    screenFrame = candidate;
    break;
  }
}
const width = 520;
const height = Math.min(760, Math.max(620, screenFrame.size.height - 48));
const frame = $.NSMakeRect(
  screenFrame.origin.x + screenFrame.size.width - width - 24,
  screenFrame.origin.y + Math.max(24, (screenFrame.size.height - height) / 2),
  width,
  height
);
const window = $.NSWindow.alloc.initWithContentRectStyleMaskBackingDefer(
  frame,
  style,
  $.NSBackingStoreBuffered,
  false
);
window.setTitle('微信助手 V1');
window.setMinSize($.NSMakeSize(390, 620));
window.setTitlebarAppearsTransparent(true);
window.setTitleVisibility($.NSWindowTitleHidden);

const webview = $.WKWebView.alloc.initWithFrame($.NSMakeRect(0, 0, width, height));
webview.setAutoresizingMask($.NSViewWidthSizable | $.NSViewHeightSizable);
window.setContentView(webview);
webview.loadRequest($.NSURLRequest.requestWithURL($.NSURL.URLWithString(%s)));

window.makeKeyAndOrderFront(null);
app.activateIgnoringOtherApps(true);
app.run();
`, string(escapedURL))
}

func newCompanionHandler() http.Handler {
	srv := &companionServer{token: companionRandomID()}
	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.indexHandler)
	mux.HandleFunc("/favicon.ico", companionFaviconHandler)
	mux.HandleFunc("/api/status", srv.guard(companionStatusHandler))
	mux.HandleFunc("/api/sessions", srv.guard(companionSessionsHandler))
	mux.HandleFunc("/api/timeline", srv.guard(companionTimelineHandler))
	mux.HandleFunc("/api/ask", srv.guard(companionAskHandler))
	mux.HandleFunc("/api/ask-stream", srv.guard(companionAskStreamHandler))
	mux.HandleFunc("/api/upload", srv.guard(companionUploadHandler))
	mux.HandleFunc("/api/attachment/", srv.guard(companionAttachmentHandler))
	return mux
}

func (s *companionServer) indexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, strings.ReplaceAll(companionHTML, companionTokenPlaceholder, s.token))
}

func (s *companionServer) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.checkRequest(r); err != nil {
			writeCompanionError(w, http.StatusForbidden, "forbidden", err.Error())
			return
		}
		next(w, r)
	}
}

func (s *companionServer) checkRequest(r *http.Request) error {
	if !companionRequestHostAllowed(r) {
		return fmt.Errorf("request host is not loopback")
	}
	if !s.checkToken(r) {
		return fmt.Errorf("invalid companion token")
	}
	if !companionRequestSameOrigin(r) {
		return fmt.Errorf("request origin is not allowed")
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
		if contentType != "application/json" && contentType != "multipart/form-data" {
			return fmt.Errorf("unsupported content type")
		}
	}
	return nil
}

func (s *companionServer) checkToken(r *http.Request) bool {
	token := strings.TrimSpace(r.Header.Get("X-Wechat-Companion-Token"))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	return token != "" && token == s.token
}

func companionRequestHostAllowed(r *http.Request) bool {
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	if host == "" {
		return false
	}
	hostOnly, _, err := net.SplitHostPort(host)
	if err != nil {
		hostOnly = host
	}
	hostOnly = strings.Trim(strings.ToLower(hostOnly), "[]")
	return hostOnly == "localhost" || hostOnly == "127.0.0.1" || hostOnly == "::1"
}

func companionRequestSameOrigin(r *http.Request) bool {
	for _, key := range []string{"Origin", "Referer"} {
		raw := strings.TrimSpace(r.Header.Get(key))
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil {
			return false
		}
		if !companionOriginMatchesHost(u, r.Host) {
			return false
		}
	}
	return true
}

func companionOriginMatchesHost(u *url.URL, host string) bool {
	if u == nil || !strings.EqualFold(u.Scheme, "http") {
		return false
	}
	return strings.EqualFold(strings.Trim(host, "[]"), strings.Trim(u.Host, "[]"))
}

func companionFaviconHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func companionStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	status := map[string]any{
		"app": map[string]any{
			"name":      appName,
			"version":   appVersion,
			"read_only": true,
			"strict":    strictReadOnlyMode(),
		},
		"cli": companionCLIMountPayload(companionCLIMountInfo()),
	}
	toolCalls := []companionToolTrace{}
	data, errCode, err := companionRunTracedTool("read_os", map[string]any{"mode": "status"}, "companion status", &toolCalls)
	if err != nil {
		status["wechat_error"] = map[string]any{
			"code":    errCode,
			"message": err.Error(),
		}
	} else {
		status["wechat"] = data
	}
	status["tool_calls"] = toolCalls
	writeCompanionJSON(w, http.StatusOK, map[string]any{"ok": true, "data": status})
}

func companionSessionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	limit := companionBoundLimit(queryInt(r, "limit", 40))
	flags := map[string]any{"limit": limit}
	if keyword := strings.TrimSpace(r.URL.Query().Get("keyword")); keyword != "" {
		flags["keyword"] = keyword
	}
	if typeFilter := strings.TrimSpace(r.URL.Query().Get("type_filter")); typeFilter != "" {
		flags["type_filter"] = typeFilter
	}
	toolCalls := []companionToolTrace{}
	data, errCode, err := companionRunTracedTool("sessions", flags, "companion sessions", &toolCalls)
	if err != nil {
		writeCompanionError(w, http.StatusFailedDependency, errCode, err.Error())
		return
	}
	payload := mapAny(data)
	if payload == nil {
		payload = map[string]any{"result": data}
	}
	payload["tool_calls"] = toolCalls
	writeCompanionJSON(w, http.StatusOK, map[string]any{"ok": true, "data": payload})
}

func companionTimelineHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var req companionTimelineRequest
	if err := readCompanionJSON(r, &req); err != nil {
		writeCompanionError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	toolCalls := []companionToolTrace{}
	data, errCode, err := companionLoadTimelineWithTrace(req.Chat, req.Limit, &toolCalls)
	if err != nil {
		writeCompanionError(w, http.StatusFailedDependency, errCode, err.Error())
		return
	}
	payload := mapAny(data)
	if payload == nil {
		payload = map[string]any{"result": data}
	}
	payload["tool_calls"] = toolCalls
	writeCompanionJSON(w, http.StatusOK, map[string]any{"ok": true, "data": payload})
}

func companionAskHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var req companionAskRequest
	if err := readCompanionJSON(r, &req); err != nil {
		writeCompanionError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	data, status, code, message := companionBuildAskData(r.Context(), req, nil)
	if status != http.StatusOK {
		writeCompanionError(w, status, code, message)
		return
	}
	writeCompanionJSON(w, http.StatusOK, map[string]any{"ok": true, "data": data})
}

func companionAskStreamHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var req companionAskRequest
	if err := readCompanionJSON(r, &req); err != nil {
		writeCompanionError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeCompanionError(w, http.StatusInternalServerError, "stream_unavailable", "streaming is not available")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	var writeMu sync.Mutex
	writeEvent := func(event string, data map[string]any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		companionWriteSSE(w, event, data)
		flusher.Flush()
	}
	flusher.Flush()
	data, status, code, message := companionBuildAskData(r.Context(), req, writeEvent)
	if status != http.StatusOK {
		writeEvent("error", map[string]any{"code": code, "message": message})
		return
	}
	writeEvent("answer", data)
	writeEvent("done", map[string]any{})
}

func companionUploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, companionUploadRequestMaxSize)
	if err := r.ParseMultipartForm(companionUploadRequestMaxSize); err != nil {
		writeCompanionError(w, http.StatusBadRequest, "invalid_upload", err.Error())
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeCompanionError(w, http.StatusBadRequest, "missing_file", "no files uploaded")
		return
	}
	attachments := make([]companionAttachment, 0, len(files))
	for _, header := range files {
		if header == nil {
			continue
		}
		if header.Size > companionUploadFileMaxSize {
			writeCompanionError(w, http.StatusRequestEntityTooLarge, "file_too_large", fmt.Sprintf("%s exceeds 64MB", header.Filename))
			return
		}
		attachment, err := companionSaveUploadedFile(header)
		if err != nil {
			writeCompanionError(w, http.StatusInternalServerError, "upload_failed", err.Error())
			return
		}
		attachments = append(attachments, attachment)
	}
	writeCompanionJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"attachments": attachments}})
}

func companionAttachmentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeCompanionError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/api/attachment/")
	path, err := companionAttachmentPathFromURL(rel)
	if err != nil {
		writeCompanionError(w, http.StatusNotFound, "not_found", "attachment not found")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, r, path)
}

func companionBuildAskData(ctx context.Context, req companionAskRequest, emit companionStreamEmitter) (map[string]any, int, string, string) {
	req.Attachments = companionTrustedAttachments(req.Attachments)
	req.History = companionTrustedHistory(req.History)
	prompt := companionBuildPromptFromContexts(req, nil, nil)
	cli := companionCLIMountInfo()
	handoff := companionHandoff(prompt, cli, req.Attachments)
	result, err := companionCPURunner(ctx, prompt, handoff, req, emit)
	if err != nil {
		return nil, http.StatusFailedDependency, "cpu_error", err.Error()
	}
	answer := strings.TrimSpace(result.Answer)
	if answer == "" {
		return nil, http.StatusFailedDependency, "cpu_empty_answer", "CPU returned an empty answer"
	}

	return compactMap(map[string]any{
		"answer":      answer,
		"handoff":     handoff,
		"cli":         companionCLIMountPayload(cli),
		"attachments": req.Attachments,
		"context":     companionContextMetaFromContexts(req, nil, nil, len(prompt.User)),
		"tool_calls":  []companionToolTrace{},
		"cpu":         result.Meta,
	}), http.StatusOK, "", ""
}

func companionHandoff(prompt companionPrompt, cli companionCLIMount, attachments []companionAttachment) map[string]any {
	return compactMap(map[string]any{
		"system":      prompt.System,
		"user":        prompt.User,
		"cli":         companionCLIMountPayload(cli),
		"attachments": attachments,
	})
}

func companionCLIMountPayload(cli companionCLIMount) map[string]any {
	return compactMap(map[string]any{
		"command":      cli.Command,
		"binary":       cli.Binary,
		"path_prepend": cli.PathPrepend,
		"env":          cli.Env,
	})
}

func companionRunCPU(ctx context.Context, prompt companionPrompt, handoff map[string]any, req companionAskRequest, emit companionStreamEmitter) (companionCPUResult, error) {
	binary := companionCPUBinary()
	if binary == "" {
		return companionCPUResult{}, fmt.Errorf("babata-cpu is not available")
	}
	stateDir, err := appStateDir()
	if err != nil {
		return companionCPUResult{}, err
	}
	logDir := filepath.Join(stateDir, "companion-cpu")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return companionCPUResult{}, err
	}
	stem := time.Now().Format("20060102-150405") + "-" + companionRandomID()
	label := "wechat-companion"
	systemFile := filepath.Join(logDir, stem+".system.md")
	promptFile := filepath.Join(logDir, stem+".prompt.md")
	metaFile := filepath.Join(logDir, stem+".meta.json")
	finalFile := filepath.Join(logDir, stem+".final.txt")
	if err := os.WriteFile(systemFile, []byte(prompt.System), 0o600); err != nil {
		return companionCPUResult{}, err
	}
	if err := os.WriteFile(promptFile, []byte(companionCPUUserPrompt(prompt, handoff)), 0o600); err != nil {
		return companionCPUResult{}, err
	}

	timeout := companionCPUTimeout()
	args := []string{
		"run",
		"--prompt-file", promptFile,
		"--system-file", systemFile,
		"--timeout", strconv.Itoa(int(timeout.Seconds())),
		"--cwd", companionCPUWorkDir(),
		"--log-dir", logDir,
		"--ts", stem,
		"--label", label,
		"--meta-file", metaFile,
		"--final-file", finalFile,
	}
	if policy := companionCPUPolicy(); policy != "" {
		args = append(args, "--policy", policy)
	}
	if primary := companionCPUPrimary(); primary != "" {
		args = append(args, "--primary", primary)
	}
	if fallback := companionCPUFallback(); fallback != "" {
		args = append(args, "--fallback", fallback)
	}
	if chain := companionCPUChain(); chain != "" {
		args = append(args, "--chain", chain)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout+companionCPUShutdownGrace)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Env = companionCPUChildEnv()
	cmd.Dir = companionCPUWorkDir()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return companionCPUResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return companionCPUResult{}, err
	}

	if err := cmd.Start(); err != nil {
		return companionCPUResult{}, err
	}

	state := newCompanionCPUStreamState()
	done := make(chan struct{})
	var wg sync.WaitGroup
	var outputMu sync.Mutex
	outputLines := []string{}
	collect := func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		outputMu.Lock()
		defer outputMu.Unlock()
		outputLines = append(outputLines, companionTraceText(line, companionCPULogTextMax))
		if len(outputLines) > 24 {
			outputLines = outputLines[len(outputLines)-24:]
		}
	}

	wg.Add(3)
	go func() {
		defer wg.Done()
		companionScanCPUOutput(stdout, emit, collect)
	}()
	go func() {
		defer wg.Done()
		companionScanCPUOutput(stderr, emit, collect)
	}()
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				companionPollCPUStreams(logDir, stem, label, state, emit, true)
				return
			case <-runCtx.Done():
				companionPollCPUStreams(logDir, stem, label, state, emit, true)
				return
			case <-ticker.C:
				companionPollCPUStreams(logDir, stem, label, state, emit, false)
			}
		}
	}()

	err = cmd.Wait()
	close(done)
	wg.Wait()
	if runCtx.Err() == context.DeadlineExceeded {
		return companionCPUResult{}, fmt.Errorf("CPU timed out after %s", timeout)
	}
	meta := companionReadCPUMeta(metaFile)
	answerBytes, readErr := os.ReadFile(finalFile)
	answer := strings.TrimSpace(string(answerBytes))
	if answer == "" && readErr != nil {
		collect(readErr.Error())
	}
	if err != nil {
		outputMu.Lock()
		tail := strings.Join(outputLines, "\n")
		outputMu.Unlock()
		if tail != "" {
			return companionCPUResult{Answer: answer, Meta: meta}, fmt.Errorf("CPU failed: %v\n%s", err, tail)
		}
		return companionCPUResult{Answer: answer, Meta: meta}, fmt.Errorf("CPU failed: %v", err)
	}
	return companionCPUResult{Answer: answer, Meta: meta}, nil
}

func companionCPUBinary() string {
	for _, path := range []string{envFirst("WECHAT_CLI_COMPANION_CPU_BIN", "BABATA_CPU_BIN")} {
		if companionExecutableFile(path) {
			return path
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		path := filepath.Join(home, "cc-workspace", "bin", "babata-cpu")
		if companionExecutableFile(path) {
			return path
		}
	}
	if path, err := exec.LookPath("babata-cpu"); err == nil && companionExecutableFile(path) {
		return path
	}
	return ""
}

func companionExecutableFile(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Mode()&0o111 != 0
}

func companionCPUTimeout() time.Duration {
	raw := strings.TrimSpace(envFirst("WECHAT_CLI_COMPANION_CPU_TIMEOUT", "BABATA_CPU_TIMEOUT"))
	if raw == "" {
		return companionCPUDefaultTimeout
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return companionCPUDefaultTimeout
}

func companionCPUPolicy() string {
	return strings.ToLower(strings.TrimSpace(envFirst("WECHAT_CLI_COMPANION_CPU_POLICY", "BABATA_CPU_POLICY")))
}

func companionCPUPrimary() string {
	return strings.ToLower(strings.TrimSpace(envFirst("WECHAT_CLI_COMPANION_CPU_PRIMARY", "BABATA_PRIMARY_CPU")))
}

func companionCPUFallback() string {
	return strings.ToLower(strings.TrimSpace(envFirst("WECHAT_CLI_COMPANION_CPU_FALLBACK", "BABATA_FALLBACK_CPU")))
}

func companionCPUChain() string {
	return strings.TrimSpace(envFirst("WECHAT_CLI_COMPANION_CPU_CHAIN", "BABATA_CPU_CHAIN"))
}

func companionCPUUserPrompt(prompt companionPrompt, handoff map[string]any) string {
	handoffJSON, _ := json.MarshalIndent(compactMap(map[string]any{
		"cli":         handoff["cli"],
		"attachments": handoff["attachments"],
	}), "", "  ")
	var b strings.Builder
	b.WriteString(prompt.User)
	b.WriteString("\n\n---\n\n")
	b.WriteString("执行边界：\n")
	b.WriteString("- 直接回答用户问题，不套固定回复模板，不要求用户改用固定问法。\n")
	b.WriteString("- 如果需要微信内容，自己调用本机 wechat-cli；会话解析、读取条数、分页和是否搜索都由你按问题判断。\n")
	b.WriteString("- 不要预设固定读取条数或固定窗口；如果一页不够，用 timeline/search-context/context 的分页能力继续读。\n")
	b.WriteString("- 只做本地读取和分析；不要发送微信消息，不要控制微信 UI。\n")
	b.WriteString("- 如果上下文不足，说明具体缺口和你已经尝试过的读取路径。\n")
	b.WriteString("- 最终回答只写给用户看的结论；工具过程会由 UI 单独展示。\n")
	b.WriteString("\n本机能力挂载：\n```json\n")
	b.Write(handoffJSON)
	b.WriteString("\n```\n")
	return b.String()
}

func companionCPUChildEnv() []string {
	next := companionCLIChildEnv(true)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "BABATA_") || key == "PROJECT_STATE_DIR" || key == "CLAUDE_BIN" || key == "CODEX_BIN" {
			next = upsertEnv(next, key, value)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		next = prependPathEnv(next, filepath.Join(home, "cc-workspace", "bin"))
		next = prependPathEnv(next, filepath.Join(home, ".npm-global", "bin"))
	}
	return next
}

func companionCPUWorkDir() string {
	if wd := strings.TrimSpace(envFirst("WECHAT_CLI_COMPANION_CPU_CWD", "BABATA_CPU_CWD")); wd != "" {
		if info, err := os.Stat(wd); err == nil && info.IsDir() {
			return wd
		}
	}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		return wd
	}
	return companionCLIWorkDir()
}

func companionReadCPUMeta(path string) map[string]any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil
	}
	delete(meta, "stream_file")
	return compactMap(meta)
}

type companionCPUStreamState struct {
	offsets      map[string]int64
	starts       map[string]time.Time
	tools        map[string]companionCPUCommandInfo
	textMessages int
}

func newCompanionCPUStreamState() *companionCPUStreamState {
	return &companionCPUStreamState{
		offsets: map[string]int64{},
		starts:  map[string]time.Time{},
		tools:   map[string]companionCPUCommandInfo{},
	}
}

func companionScanCPUOutput(r io.Reader, emit companionStreamEmitter, collect func(string)) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if collect != nil {
			collect(line)
		}
		if emit != nil && companionShouldShowCPULog(line) {
			emit("cpu_log", map[string]any{"text": companionTraceText(companionHumanCPULog(line), companionCPULogTextMax)})
		}
	}
}

func companionShouldShowCPULog(line string) bool {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "babata-cpu:") || strings.HasPrefix(line, "[babata-cpu]") {
		return true
	}
	lower := strings.ToLower(line)
	for _, marker := range []string{"fallback", "unavailable", "timeout", "failed", "error", "quota", "auth"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func companionHumanCPULog(line string) string {
	line = companionTraceText(line, companionCPULogTextMax)
	line = strings.ReplaceAll(line, "stream-json -> ", "stream -> ")
	return line
}

func companionPollCPUStreams(logDir, stem, label string, state *companionCPUStreamState, emit companionStreamEmitter, final bool) {
	if emit == nil || state == nil {
		return
	}
	pattern := filepath.Join(logDir, "*-stream-"+stem+"-"+label+"*.jsonl")
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) == 0 {
		return
	}
	sort.Strings(files)
	for _, path := range files {
		companionReadCPUStreamFile(path, state, emit, final)
	}
}

func companionReadCPUStreamFile(path string, state *companionCPUStreamState, emit companionStreamEmitter, final bool) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	offset := state.offsets[path]
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return
		}
	}
	reader := bufio.NewReader(f)
	pos := offset
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if err == io.EOF && !final && !strings.HasSuffix(line, "\n") {
				break
			}
			pos += int64(len(line))
			companionHandleCPUStreamLine(line, state, emit)
		}
		if err != nil {
			break
		}
	}
	state.offsets[path] = pos
}

func companionHandleCPUStreamLine(line string, state *companionCPUStreamState, emit companionStreamEmitter) {
	line = strings.TrimSpace(line)
	if line == "" || emit == nil {
		return
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return
	}
	typ := stringMapValue(event, "type")
	switch typ {
	case "assistant":
		companionHandleClaudeAssistantEvent(event, state, emit)
	case "user":
		companionHandleClaudeUserEvent(event, state, emit)
	case "item.started":
		companionHandleCodexItemEvent(event, state, emit, "running")
	case "item.completed":
		companionHandleCodexItemEvent(event, state, emit, "completed")
	}
}

func companionHandleClaudeAssistantEvent(event map[string]any, state *companionCPUStreamState, emit companionStreamEmitter) {
	message := mapAny(event["message"])
	for _, item := range mapSliceAny(message["content"]) {
		switch stringMapValue(item, "type") {
		case "text":
			companionEmitAgentText(state, emit, stringMapValue(item, "text"))
			continue
		case "tool_use":
			id := firstNonEmpty(stringMapValue(item, "id"), companionRandomID())
			name := firstNonEmpty(stringMapValue(item, "name"), "tool")
			input := mapAny(item["input"])
			info := companionCPUCommandInfoFromNameInput(name, input)
			state.starts[id] = time.Now()
			state.tools[id] = info
			emit("tool_start", compactMap(map[string]any{
				"id":      id,
				"tool":    info.Tool,
				"command": info.DisplayCommand,
				"status":  "running",
				"label":   info.Label,
				"summary": companionCPUCommandRunningSummary(info),
				"args":    info.Args,
			}))
		}
	}
}

func companionHandleClaudeUserEvent(event map[string]any, state *companionCPUStreamState, emit companionStreamEmitter) {
	message := mapAny(event["message"])
	for _, item := range mapSliceAny(message["content"]) {
		if stringMapValue(item, "type") != "tool_result" {
			continue
		}
		id := stringMapValue(item, "tool_use_id")
		status := "completed"
		if v, ok := item["is_error"].(bool); ok && v {
			status = "error"
		}
		duration := companionCPUStreamDurationMS(state, id)
		content := companionToolResultText(item["content"])
		info := companionCPUStreamTakeToolInfo(state, id)
		if info.Tool == "" {
			info = companionCPUCommandInfoFromNameInput("tool", nil)
		}
		emit("tool_result", companionBuildCPUCommandTrace(id, info, status, content, duration))
	}
}

func companionHandleCodexItemEvent(event map[string]any, state *companionCPUStreamState, emit companionStreamEmitter, fallbackStatus string) {
	item := mapAny(event["item"])
	itemType := stringMapValue(item, "type")
	if itemType == "" {
		return
	}
	if itemType == "agent_message" {
		if fallbackStatus == "completed" {
			companionEmitAgentText(state, emit, stringMapValue(item, "text"))
		}
		return
	}
	if !companionCodexItemLooksTool(itemType) {
		return
	}
	id := firstNonEmpty(stringMapValue(item, "id"), stringMapValue(item, "call_id"), companionRandomID())
	name := firstNonEmpty(stringMapValue(item, "name"), stringMapValue(item, "tool"), itemType)
	info := companionCPUCommandInfoFromCodexItem(name, item)
	status := firstNonEmpty(stringMapValue(item, "status"), fallbackStatus)
	if fallbackStatus == "running" {
		state.starts[id] = time.Now()
		state.tools[id] = info
		emit("tool_start", compactMap(map[string]any{
			"id":      id,
			"tool":    info.Tool,
			"command": info.DisplayCommand,
			"status":  "running",
			"label":   info.Label,
			"summary": companionCPUCommandRunningSummary(info),
			"args":    info.Args,
		}))
		return
	}
	output := firstNonEmpty(stringMapValue(item, "aggregated_output"), stringMapValue(item, "output"), stringMapValue(item, "text"), stringMapValue(item, "error"))
	if companionExitCodeNonZero(item["exit_code"]) {
		status = "error"
	}
	stored := companionCPUStreamTakeToolInfo(state, id)
	if stored.Tool != "" {
		info = companionMergeCPUCommandInfo(info, stored)
	}
	emit("tool_result", companionBuildCPUCommandTrace(id, info, status, output, companionCPUStreamDurationMS(state, id)))
}

func companionEmitAgentText(state *companionCPUStreamState, emit companionStreamEmitter, text string) {
	text = strings.TrimSpace(text)
	if text == "" || emit == nil {
		return
	}
	if state != nil && state.textMessages > 0 {
		text = "\n\n" + text
	}
	if state != nil {
		state.textMessages++
	}
	emit("assistant_delta", map[string]any{"text": text})
}

func companionExitCodeNonZero(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		x = strings.TrimSpace(x)
		return x != "" && x != "0"
	default:
		return false
	}
}

func companionCodexItemLooksTool(itemType string) bool {
	switch itemType {
	case "command_execution", "mcp_tool_call", "tool_call", "function_call", "exec_command":
		return true
	default:
		return strings.Contains(itemType, "tool") || strings.Contains(itemType, "command")
	}
}

func companionCodexItemArgs(item map[string]any) map[string]any {
	if args := mapAny(item["input"]); args != nil {
		return args
	}
	if args := mapAny(item["arguments"]); args != nil {
		return args
	}
	return item
}

func companionCPUCommandInfoFromCodexItem(name string, item map[string]any) companionCPUCommandInfo {
	input := companionCodexItemArgs(item)
	command := firstNonEmpty(
		stringMapValue(item, "command"),
		companionToolCommandFromInput(name, mapAny(item["input"])),
		companionToolCommandFromInput(name, input),
		name,
	)
	return companionCPUCommandInfoFromCommand(name, command, companionTraceCPUGenericArgs(input))
}

func companionCPUCommandInfoFromNameInput(name string, input map[string]any) companionCPUCommandInfo {
	command := companionToolCommandFromInput(name, input)
	return companionCPUCommandInfoFromCommand(name, command, companionTraceCPUGenericArgs(input))
}

func companionCPUCommandInfoFromCommand(name, command string, fallbackArgs map[string]any) companionCPUCommandInfo {
	if info, ok := companionWechatCPUCommandInfo(command); ok {
		return info
	}
	tool := firstNonEmpty(strings.TrimSpace(name), "tool")
	displayCommand := companionTraceText(firstNonEmpty(command, tool), 180)
	return companionCPUCommandInfo{
		Tool:           tool,
		Label:          companionCPUStreamToolLabel(name, map[string]any{"command": command}),
		DisplayCommand: displayCommand,
		Args:           fallbackArgs,
	}
}

func companionMergeCPUCommandInfo(next, prev companionCPUCommandInfo) companionCPUCommandInfo {
	if next.Tool == "" || next.Tool == "tool" || next.Tool == "command_execution" {
		next.Tool = prev.Tool
	}
	if next.Label == "" || next.Label == "运行命令" || next.Label == "工具调用" {
		next.Label = prev.Label
	}
	if next.DisplayCommand == "" || next.DisplayCommand == next.Tool {
		next.DisplayCommand = prev.DisplayCommand
	}
	if len(next.Args) == 0 {
		next.Args = prev.Args
	}
	next.KnownWechat = next.KnownWechat || prev.KnownWechat
	return next
}

func companionCPUStreamTakeToolInfo(state *companionCPUStreamState, id string) companionCPUCommandInfo {
	if state == nil || id == "" {
		return companionCPUCommandInfo{}
	}
	info, ok := state.tools[id]
	if ok {
		delete(state.tools, id)
	}
	return info
}

func companionTraceCPUGenericArgs(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	filtered := map[string]any{}
	for key, value := range input {
		switch key {
		case "aggregated_output", "output", "result", "text", "error":
			continue
		default:
			filtered[key] = value
		}
	}
	return companionTraceGenericArgs(filtered)
}

func companionWechatCPUCommandInfo(command string) (companionCPUCommandInfo, bool) {
	fields := companionWechatCommandFields(command)
	if len(fields) == 0 {
		return companionCPUCommandInfo{}, false
	}
	commandIndex := companionWechatCommandIndex(fields)
	if commandIndex < 0 {
		if companionWechatFieldsContainHelp(fields[1:]) {
			return companionWechatCPUInfoFromParts("help", "help", nil), true
		}
		return companionWechatCPUInfoFromParts("wechat-cli", "", fields[1:]), true
	}
	cmd := fields[commandIndex]
	args := fields[commandIndex+1:]
	if cmd == "call" || cmd == "call-json" {
		if len(args) == 0 {
			return companionWechatCPUInfoFromParts("wechat-cli", cmd, args), true
		}
		tool := companionNormalizeWechatTool(args[0])
		if tool == "" {
			tool = args[0]
		}
		info := companionWechatCPUInfoFromParts(tool, companionWechatDisplayCommandForTool(tool, args[0]), args[1:])
		if info.DisplayCommand == "wechat-cli "+tool {
			info.DisplayCommand = "wechat-cli " + cmd + " " + companionTraceText(tool, 80)
		}
		return info, true
	}
	tool := companionNormalizeWechatTool(cmd)
	return companionWechatCPUInfoFromParts(tool, companionWechatDisplayCommandForTool(tool, cmd), args), true
}

func companionWechatCommandFields(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	idx := companionLastWechatCLIIndex(command)
	if idx < 0 {
		return nil
	}
	fields := companionShellFieldsUntilPipe(command[idx:])
	if len(fields) == 0 {
		return nil
	}
	fields[0] = "wechat-cli"
	return fields
}

func companionLastWechatCLIIndex(s string) int {
	last := -1
	start := 0
	for {
		idx := strings.Index(s[start:], "wechat-cli")
		if idx < 0 {
			return last
		}
		abs := start + idx
		after := abs + len("wechat-cli")
		if after >= len(s) || strings.ContainsRune(" \t\r\n'\"|;)", rune(s[after])) {
			last = abs
		}
		start = abs + 1
	}
}

func companionShellFieldsUntilPipe(s string) []string {
	fields := []string{}
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if b.Len() == 0 {
			return
		}
		fields = append(fields, b.String())
		b.Reset()
	}
	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case '|':
			flush()
			return fields
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return fields
}

func companionWechatCommandIndex(fields []string) int {
	for i := 1; i < len(fields); i++ {
		if companionNormalizeWechatTool(fields[i]) != "" || fields[i] == "call" || fields[i] == "call-json" {
			return i
		}
	}
	for i := 1; i < len(fields); i++ {
		if strings.HasPrefix(fields[i], "-") {
			continue
		}
		return i
	}
	return -1
}

func companionWechatFieldsContainHelp(fields []string) bool {
	for _, field := range fields {
		if field == "-h" || field == "--help" || field == "help" {
			return true
		}
	}
	return false
}

func companionNormalizeWechatTool(cmd string) string {
	switch strings.TrimSpace(cmd) {
	case "status", "agent", "coverage", "workflows":
		return "read_os"
	case "sessions":
		return "sessions"
	case "resolve-chat", "resolve_chat":
		return "resolve_chat"
	case "contacts":
		return "contacts"
	case "history", "messages":
		return "messages"
	case "timeline", "chat-timeline", "chat_timeline", "conversation-view", "conversation_view":
		return "chat_timeline"
	case "context", "message-context", "message_context", "around":
		return "message_context"
	case "tail", "watch", "observe", "events":
		return "read_events"
	case "media", "media-resources", "media_resources", "attachments":
		return "media_resources"
	case "search":
		return "search"
	case "search-context", "search_context", "search-with-context", "search_with_context":
		return "search_with_context"
	case "members", "group-members", "group_members":
		return "group_members"
	case "tools":
		return "tools"
	case "tool-schema", "tool_schema":
		return "tool_schema"
	case "version", "--version":
		return "version"
	case "help", "--help", "-h":
		return "help"
	default:
		return ""
	}
}

func companionWechatDisplayCommandForTool(tool, cmd string) string {
	switch tool {
	case "read_os":
		switch cmd {
		case "agent", "coverage", "workflows":
			return cmd
		default:
			return "status"
		}
	case "chat_timeline":
		return "timeline"
	case "message_context":
		return "context"
	case "media_resources":
		return "media"
	case "search_with_context":
		return "search-context"
	case "group_members":
		return "members"
	case "resolve_chat":
		return "resolve-chat"
	case "tool_schema":
		return "tool-schema"
	case "messages", "sessions", "contacts", "search", "tools", "version", "help":
		return strings.ReplaceAll(tool, "_", "-")
	default:
		if cmd != "" {
			return companionTraceText(cmd, 60)
		}
		return companionTraceText(tool, 60)
	}
}

func companionWechatCPUInfoFromParts(tool, displayCommand string, args []string) companionCPUCommandInfo {
	flags := parseKVFlags(args)
	if v, ok := flags["in"]; ok && flags["chat"] == nil {
		flags["chat"] = v
		delete(flags, "in")
	}
	if pos := firstPositional(args); pos != "" {
		switch tool {
		case "messages", "chat_timeline", "message_context", "media_resources", "read_events", "group_members":
			if flags["chat"] == nil && flags["talker"] == nil && flags["chatroom_id"] == nil {
				flags["chat"] = pos
			}
		case "search", "search_with_context":
			if flags["keyword"] == nil {
				flags["keyword"] = pos
			}
		case "resolve_chat":
			if flags["query"] == nil && flags["chat"] == nil && flags["keyword"] == nil {
				flags["query"] = pos
			}
		case "contacts":
			if flags["keyword"] == nil {
				flags["keyword"] = pos
			}
		}
	}
	if tool == "" {
		tool = "wechat-cli"
	}
	if displayCommand == "" {
		displayCommand = companionWechatDisplayCommandForTool(tool, tool)
	}
	return companionCPUCommandInfo{
		Tool:           tool,
		Label:          companionToolLabel(tool),
		DisplayCommand: "wechat-cli " + displayCommand,
		Args:           companionTraceCPUCommandArgs(tool, flags),
		KnownWechat:    true,
	}
}

func companionTraceCPUCommandArgs(tool string, flags map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{
		"limit", "offset", "order", "display_order", "include_images", "include_media_paths",
		"keyword", "query", "type_filter", "type", "mode", "search_mode", "context_limit",
		"before_count", "after_count", "local_id", "server_id_str", "since_local_id",
		"after", "before", "from_me", "stats",
	} {
		if v, ok := flags[key]; ok && v != nil {
			switch key {
			case "keyword", "query":
				out[key] = companionSafeDisplayValue(v)
			default:
				out[key] = v
			}
		}
	}
	for _, key := range []string{"chat", "talker", "chatroom_id", "in"} {
		if companionTraceArgPresent(flags, key) {
			out["chat"] = "selected"
			break
		}
	}
	if tool == "version" || tool == "help" || tool == "tools" || tool == "tool_schema" {
		return compactMap(out)
	}
	return compactMap(out)
}

func companionSafeDisplayValue(v any) any {
	s := strings.TrimSpace(fmt.Sprint(v))
	if companionLooksPrivateChatIdentifier(s) {
		return "selected"
	}
	return companionTraceText(s, 80)
}

func companionLooksPrivateChatIdentifier(s string) bool {
	lower := strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(lower, "wxid_") ||
		strings.HasPrefix(lower, "gh_") ||
		strings.Contains(lower, "@chatroom") ||
		strings.Contains(lower, "@openim") ||
		strings.HasPrefix(lower, "chatroom_")
}

func companionCPUCommandRunningSummary(info companionCPUCommandInfo) string {
	if info.Label != "" && info.KnownWechat {
		return "正在" + info.Label
	}
	if info.DisplayCommand != "" {
		return companionTraceText(info.DisplayCommand, 220)
	}
	return "工具调用中"
}

func companionBuildCPUCommandTrace(id string, info companionCPUCommandInfo, status, output string, durationMS int64) map[string]any {
	status = companionNormalizeCPUStatus(status)
	output = strings.TrimSpace(output)
	if info.Tool == "" {
		info.Tool = "tool"
	}
	if info.DisplayCommand == "" {
		info.DisplayCommand = info.Tool
	}
	if info.Label == "" {
		info.Label = companionToolLabel(info.Tool)
	}
	duration := time.Duration(durationMS) * time.Millisecond
	if tool, data, errCode, err, ok := companionParseToolEnvelope(output); ok {
		if tool == "" {
			tool = info.Tool
		}
		trace := companionBuildToolTrace(id, tool, info.DisplayCommand, info.Args, data, errCode, err, duration)
		trace.Command = info.DisplayCommand
		trace.Args = info.Args
		trace.DurationMS = durationMS
		if info.Label != "" {
			trace.Label = info.Label
		}
		if status == "error" && trace.Status != "error" {
			trace.Status = "error"
			trace.Summary = companionTraceText(firstNonEmpty(output, "工具调用失败"), companionCPUToolOutputMax)
		}
		return companionToolTraceEventMap(trace)
	}
	if messages := companionParseJSONLineMessages(output); len(messages) > 0 {
		tool := info.Tool
		if tool == "" || tool == "tool" || tool == "command_execution" || tool == "wechat-cli" {
			tool = "chat_timeline"
		}
		trace := companionBuildToolTrace(id, tool, info.DisplayCommand, info.Args, map[string]any{
			"query":    map[string]any{"returned": len(messages)},
			"messages": messages,
		}, "", nil, duration)
		trace.Command = info.DisplayCommand
		trace.Args = info.Args
		trace.DurationMS = durationMS
		if info.Label != "" {
			trace.Label = info.Label
		}
		if status == "error" {
			trace.Status = "error"
		}
		return companionToolTraceEventMap(trace)
	}
	trace := companionToolTrace{
		ID:         id,
		Tool:       info.Tool,
		Command:    info.DisplayCommand,
		Status:     status,
		Label:      info.Label,
		Summary:    companionCPUFallbackSummary(info, status, output),
		Args:       info.Args,
		Result:     compactMap(map[string]any{"text": companionTraceText(output, companionCPUToolOutputMax)}),
		DurationMS: durationMS,
	}
	return companionToolTraceEventMap(trace)
}

func companionNormalizeCPUStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "in_progress", "started":
		return "running"
	case "error", "failed", "failure", "cancelled", "canceled":
		return "error"
	default:
		return "completed"
	}
}

func companionCPUFallbackSummary(info companionCPUCommandInfo, status, output string) string {
	if output != "" {
		if info.Tool == "version" {
			return "检查版本：" + companionTraceText(companionFirstOutputLine(output), 160)
		}
		return companionTraceText(output, companionCPUToolOutputMax)
	}
	if status == "error" {
		return "工具调用失败"
	}
	if info.KnownWechat && info.Label != "" {
		return info.Label + "完成"
	}
	return "工具调用完成"
}

func companionFirstOutputLine(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(output)
}

func companionParseToolEnvelope(output string) (string, any, string, error, bool) {
	output = strings.TrimSpace(output)
	if !strings.HasPrefix(output, "{") {
		return "", nil, "", nil, false
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(output), &env); err != nil {
		return "", nil, "", nil, false
	}
	_, hasOK := env["ok"]
	_, hasTool := env["tool"]
	_, hasName := env["name"]
	_, hasCommand := env["command"]
	_, hasData := env["data"]
	_, hasResult := env["result"]
	if !hasOK && !hasTool && !hasName && !hasCommand && !hasData && !hasResult {
		return "", nil, "", nil, false
	}
	tool := companionNormalizeWechatTool(firstNonEmpty(stringMapValue(env, "tool"), stringMapValue(env, "name"), stringMapValue(env, "command")))
	if tool == "" {
		tool = firstNonEmpty(stringMapValue(env, "tool"), stringMapValue(env, "name"))
	}
	data := env["data"]
	if data == nil {
		data = env["result"]
	}
	if data == nil {
		data = env
	}
	errCode := firstNonEmpty(stringMapValue(env, "code"), stringMapValue(env, "error_code"))
	var err error
	if ok, exists := env["ok"].(bool); exists && !ok {
		errText := firstNonEmpty(stringMapValue(env, "message"), stringMapValue(env, "error"), errCode, "wechat-cli returned ok=false")
		err = fmt.Errorf("%s", errText)
	}
	return tool, data, errCode, err, true
}

func companionParseJSONLineMessages(output string) []map[string]any {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	var arrayRows []map[string]any
	if strings.HasPrefix(output, "[") && json.Unmarshal([]byte(output), &arrayRows) == nil {
		return companionFilterMessageLikeRows(arrayRows)
	}
	messages := []map[string]any{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if msg := mapAny(row["message"]); msg != nil {
			messages = append(messages, msg)
			continue
		}
		if nested := mapSliceAny(row["messages"]); len(nested) > 0 {
			messages = append(messages, nested...)
			continue
		}
		if companionLooksMessageLike(row) {
			messages = append(messages, row)
		}
	}
	return messages
}

func companionFilterMessageLikeRows(rows []map[string]any) []map[string]any {
	out := []map[string]any{}
	for _, row := range rows {
		if companionLooksMessageLike(row) {
			out = append(out, row)
		}
	}
	return out
}

func companionLooksMessageLike(row map[string]any) bool {
	if len(row) == 0 {
		return false
	}
	if row["message"] != nil {
		return true
	}
	for _, key := range []string{"time", "time_iso", "sender", "sender_wxid", "kind", "text", "local_id", "server_id_str"} {
		if _, ok := row[key]; ok {
			return true
		}
	}
	return false
}

func companionToolTraceEventMap(trace companionToolTrace) map[string]any {
	return compactMap(map[string]any{
		"id":          trace.ID,
		"tool":        trace.Tool,
		"command":     trace.Command,
		"status":      trace.Status,
		"label":       trace.Label,
		"summary":     trace.Summary,
		"args":        trace.Args,
		"result":      trace.Result,
		"samples":     trace.Samples,
		"error":       trace.Error,
		"duration_ms": trace.DurationMS,
	})
}

func companionCPUStreamDurationMS(state *companionCPUStreamState, id string) int64 {
	if state == nil || id == "" {
		return 0
	}
	start, ok := state.starts[id]
	if !ok {
		return 0
	}
	delete(state.starts, id)
	return time.Since(start).Milliseconds()
}

func companionCPUStreamToolLabel(name string, input map[string]any) string {
	name = strings.TrimSpace(name)
	if strings.EqualFold(name, "Bash") || strings.EqualFold(name, "Shell") || strings.EqualFold(name, "exec_command") || strings.EqualFold(name, "command_execution") {
		return "运行命令"
	}
	if strings.Contains(strings.ToLower(name), "wechat") || strings.Contains(fmt.Sprint(input), "wechat-cli") {
		return "读取微信"
	}
	if name != "" {
		return name
	}
	return "工具调用"
}

func companionToolCommandFromInput(name string, input map[string]any) string {
	for _, key := range []string{"command", "cmd", "tool", "name"} {
		if v := strings.TrimSpace(stringMapValue(input, key)); v != "" {
			return companionTraceText(v, 180)
		}
	}
	return name
}

func companionToolSummaryFromInput(name string, input map[string]any) string {
	command := companionToolCommandFromInput(name, input)
	if command != "" && command != name {
		return companionTraceText(command, 220)
	}
	return companionTraceText(name, 120)
}

func companionTraceGenericArgs(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, value := range input {
		if key == "" {
			continue
		}
		switch v := value.(type) {
		case string:
			out[key] = companionTraceText(v, 300)
		case float64, bool, int, int64:
			out[key] = v
		case nil:
			continue
		default:
			out[key] = companionTraceText(fmt.Sprint(v), 300)
		}
	}
	return compactMap(out)
}

func companionToolResultText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		parts := []string{}
		for _, item := range x {
			if m := mapAny(item); m != nil {
				if text := stringMapValue(m, "text"); text != "" {
					parts = append(parts, text)
				}
				continue
			}
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func companionExecCLIJSON(ctx context.Context, args []string, strictReadOnly bool) (any, string, error) {
	binary := firstNonEmpty(companionCurrentCLIBinary(), appName)
	runCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Env = companionCLIChildEnv(strictReadOnly)
	cmd.Dir = companionCLIWorkDir()
	cmd.Stdin = nil
	output, err := cmd.CombinedOutput()
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, "timeout", fmt.Errorf("wechat-cli %s timed out", strings.Join(args, " "))
	}
	var envelope map[string]any
	if len(output) > 0 {
		if jsonErr := json.Unmarshal(output, &envelope); jsonErr != nil {
			if err != nil {
				return nil, "tool_error", fmt.Errorf("%v: %s", err, companionTraceText(string(output), 400))
			}
			return nil, "invalid_json", jsonErr
		}
	}
	if err != nil {
		if msg := companionEnvelopeErrorMessage(envelope); msg != "" {
			return nil, companionEnvelopeErrorCode(envelope), fmt.Errorf("%s", msg)
		}
		return nil, "tool_error", err
	}
	if ok, _ := envelope["ok"].(bool); !ok {
		msg := firstNonEmpty(companionEnvelopeErrorMessage(envelope), "wechat-cli returned ok=false")
		return nil, companionEnvelopeErrorCode(envelope), fmt.Errorf("%s", msg)
	}
	return mapAny(envelope["data"]), "", nil
}

func companionCLIChildEnv(strictReadOnly bool) []string {
	next := []string{}
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, "WECHAT_CLI_") || strings.HasPrefix(key, "WXKEY_") {
			next = upsertEnv(next, key, value)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		next = upsertEnv(next, "HOME", home)
		if user := strings.TrimSpace(os.Getenv("USER")); user == "" {
			user = filepath.Base(home)
			if user != "" && user != "." && user != string(filepath.Separator) {
				next = upsertEnv(next, "USER", user)
				next = upsertEnv(next, "LOGNAME", user)
			}
		}
	}
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		next = upsertEnv(next, "USER", user)
		next = upsertEnv(next, "LOGNAME", user)
	}
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/zsh"
	}
	next = upsertEnv(next, "SHELL", shell)
	tmpDir := strings.TrimSpace(os.Getenv("TMPDIR"))
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	if tmpDir != "" {
		next = upsertEnv(next, "TMPDIR", tmpDir)
	}
	lang := strings.TrimSpace(os.Getenv("LANG"))
	if lang == "" {
		lang = "en_US.UTF-8"
	}
	next = upsertEnv(next, "LANG", lang)
	next = upsertEnv(next, "PATH", companionDefaultCLIPath())
	next = companionCLIMountEnv(next)
	if strictReadOnly {
		next = upsertEnv(next, "WECHAT_CLI_STRICT_READ_ONLY", "1")
	}
	return next
}

func companionDefaultCLIPath() string {
	home, _ := os.UserHomeDir()
	dirs := []string{}
	if strings.TrimSpace(home) != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	seen := map[string]bool{}
	out := []string{}
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return strings.Join(out, string(os.PathListSeparator))
}

func companionCLIWorkDir() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return home
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return ""
}

func companionEnvelopeErrorMessage(envelope map[string]any) string {
	if envelope == nil {
		return ""
	}
	errObj := mapAny(envelope["error"])
	return stringMapValue(errObj, "message")
}

func companionEnvelopeErrorCode(envelope map[string]any) string {
	if envelope == nil {
		return "tool_error"
	}
	errObj := mapAny(envelope["error"])
	return firstNonEmpty(stringMapValue(errObj, "code"), "tool_error")
}

func companionCLIMountInfo() companionCLIMount {
	binary := companionCurrentCLIBinary()
	command := appName
	pathPrepend := ""
	env := map[string]any{}
	if binary != "" {
		command = appName
		pathPrepend = filepath.Dir(binary)
		env["WECHAT_CLI_BIN"] = binary
		env["WECHAT_CLI_COMPANION_BIN"] = binary
	}
	if pathPrepend != "" {
		env["PATH_PREPEND"] = pathPrepend
	}
	return companionCLIMount{
		Command:     command,
		Binary:      binary,
		PathPrepend: pathPrepend,
		Env:         compactMap(env),
	}
}

func companionCurrentCLIBinary() string {
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		return exe
	}
	if path, err := exec.LookPath(appName); err == nil {
		return path
	}
	return ""
}

func companionCLIMountEnv(env []string) []string {
	mount := companionCLIMountInfo()
	next := append([]string{}, env...)
	if mount.PathPrepend != "" {
		next = prependPathEnv(next, mount.PathPrepend)
	}
	if mount.Binary != "" {
		next = upsertEnv(next, "WECHAT_CLI_BIN", mount.Binary)
		next = upsertEnv(next, "WECHAT_CLI_COMPANION_BIN", mount.Binary)
	}
	return next
}

func companionAskChats(req companionAskRequest) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, chat := range append([]string{req.Chat}, req.Chats...) {
		chat = strings.TrimSpace(chat)
		if chat == "" || seen[chat] {
			continue
		}
		seen[chat] = true
		out = append(out, chat)
	}
	return out
}

func companionLoadTimeline(chat string, limit int) (any, string, error) {
	return companionLoadTimelineWithTrace(chat, limit, nil)
}

func companionLoadTimelineWithTrace(chat string, limit int, toolCalls *[]companionToolTrace) (any, string, error) {
	chat = strings.TrimSpace(chat)
	if chat == "" {
		return nil, "missing_required_argument", fmt.Errorf("chat is required")
	}
	flags := map[string]any{
		"chat":           chat,
		"limit":          companionBoundLimit(limit),
		"order":          "desc",
		"display_order":  "asc",
		"include_images": false,
	}
	return companionRunTracedTool("chat_timeline", flags, "companion timeline", toolCalls)
}

func companionRunTracedTool(name string, flags map[string]any, command string, toolCalls *[]companionToolTrace) (any, string, error) {
	start := time.Now()
	data, errCode, err := runToolResult(name, flags, command)
	if toolCalls != nil {
		id := fmt.Sprintf("tool-%d", len(*toolCalls)+1)
		*toolCalls = append(*toolCalls, companionBuildToolTrace(id, name, command, flags, data, errCode, err, time.Since(start)))
	}
	return data, errCode, err
}

func companionBuildToolTrace(id, name, command string, flags map[string]any, data any, errCode string, err error, duration time.Duration) companionToolTrace {
	trace := companionToolTrace{
		ID:         id,
		Tool:       name,
		Command:    command,
		Status:     "completed",
		Label:      companionToolLabel(name),
		Args:       companionTraceArgs(flags),
		DurationMS: duration.Milliseconds(),
	}
	if err != nil {
		trace.Status = "error"
		trace.Summary = firstNonEmpty(errCode, "tool_error") + ": " + companionTraceText(err.Error(), 180)
		trace.Error = companionTraceText(err.Error(), 400)
		return trace
	}
	switch name {
	case "read_os":
		companionFillStatusTrace(&trace, data)
	case "sessions":
		companionFillSessionsTrace(&trace, data)
	case "messages", "chat_timeline":
		companionFillTimelineTrace(&trace, data)
	case "message_context":
		companionFillMessageContextTrace(&trace, data)
	case "resolve_chat":
		companionFillResolveChatTrace(&trace, data)
	case "contacts":
		companionFillContactsTrace(&trace, data)
	case "search":
		companionFillSearchTrace(&trace, data)
	case "search_with_context":
		companionFillSearchWithContextTrace(&trace, data)
	case "media_resources":
		companionFillMediaResourcesTrace(&trace, data)
	case "group_members":
		companionFillGroupMembersTrace(&trace, data)
	case "read_events":
		companionFillReadEventsTrace(&trace, data)
	case "version":
		companionFillTextTrace(&trace, data, "检查版本")
	case "help", "tools", "tool_schema":
		companionFillTextTrace(&trace, data, "检查工具说明")
	default:
		trace.Summary = "完成 " + name
	}
	return trace
}

func companionToolLabel(name string) string {
	switch name {
	case "read_os":
		return "检查微信读取状态"
	case "sessions":
		return "查看最近会话"
	case "resolve_chat":
		return "解析会话"
	case "contacts":
		return "查找联系人"
	case "messages":
		return "读取消息记录"
	case "chat_timeline":
		return "读取聊天记录"
	case "message_context":
		return "读取消息上下文"
	case "read_events":
		return "观察新消息"
	case "media_resources":
		return "读取媒体资源"
	case "group_members":
		return "读取群成员"
	case "search":
		return "全文搜索"
	case "search_with_context":
		return "搜索上下文"
	case "version":
		return "检查版本"
	case "help":
		return "查看帮助"
	case "tools":
		return "查看工具列表"
	case "tool_schema":
		return "查看工具参数"
	default:
		return name
	}
}

func companionTraceArgs(flags map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"limit", "offset", "order", "display_order", "include_images", "include_media_paths", "keyword", "type_filter", "type", "mode", "context_limit", "before_count", "after_count", "local_id", "server_id_str"} {
		if v, ok := flags[key]; ok && v != nil {
			out[key] = v
		}
	}
	if companionTraceArgPresent(flags, "chat") ||
		companionTraceArgPresent(flags, "talker") ||
		companionTraceArgPresent(flags, "chatroom_id") {
		out["chat"] = "selected"
	}
	return compactMap(out)
}

func companionTraceArgPresent(flags map[string]any, key string) bool {
	v, ok := flags[key]
	if !ok || v == nil {
		return false
	}
	return strings.TrimSpace(fmt.Sprint(v)) != ""
}

func companionFillStatusTrace(trace *companionToolTrace, data any) {
	status := mapAny(mapAny(data)["status"])
	readiness := stringMapValue(status, "readiness")
	liveRead := false
	if v, ok := status["live_read_ok"].(bool); ok {
		liveRead = v
	}
	trace.Summary = "检查本地微信读取状态"
	if readiness != "" {
		trace.Summary += "：" + readiness
	}
	trace.Result = compactMap(map[string]any{
		"readiness":    readiness,
		"live_read_ok": liveRead,
		"capabilities": mapAny(status["capabilities"]),
	})
}

func companionFillSessionsTrace(trace *companionToolTrace, data any) {
	sessions := mapSliceAny(mapAny(data)["sessions"])
	names := []string{}
	for _, session := range sessions {
		name := firstNonEmpty(stringMapValue(session, "display_name"), stringMapValue(session, "summary"), stringMapValue(session, "username"))
		if name == "" {
			continue
		}
		names = append(names, companionTraceText(name, 36))
	}
	trace.Summary = fmt.Sprintf("查看最近活跃会话，找到 %d 个候选", len(sessions))
	trace.Result = compactMap(map[string]any{
		"session_count": len(sessions),
		"sessions":      names,
	})
}

func companionFillTimelineTrace(trace *companionToolTrace, data any) {
	timeline := mapAny(data)
	query := mapAny(timeline["query"])
	messages := mapSliceAny(timeline["messages"])
	displayName := firstNonEmpty(stringMapValue(query, "display_name"), stringMapValue(query, "chat"), "微信会话")
	media := companionTraceMediaCounts(messages)
	imageCount := intMapValue(media, "images")
	linkCount := intMapValue(media, "links")
	mediaParts := []string{}
	if imageCount > 0 {
		mediaParts = append(mediaParts, fmt.Sprintf("图片 %d", imageCount))
	}
	if linkCount > 0 {
		mediaParts = append(mediaParts, fmt.Sprintf("链接 %d", linkCount))
	}
	if files := intMapValue(media, "files"); files > 0 {
		mediaParts = append(mediaParts, fmt.Sprintf("文件 %d", files))
	}
	if voices := intMapValue(media, "voices"); voices > 0 {
		mediaParts = append(mediaParts, fmt.Sprintf("语音 %d", voices))
	}
	if videos := intMapValue(media, "videos"); videos > 0 {
		mediaParts = append(mediaParts, fmt.Sprintf("视频 %d", videos))
	}
	mediaSuffix := ""
	if len(mediaParts) > 0 {
		mediaSuffix = "，包含 " + strings.Join(mediaParts, " / ")
	}
	trace.Summary = fmt.Sprintf("查看「%s」最近 %d 条消息%s", companionTraceText(displayName, 42), len(messages), mediaSuffix)
	trace.Result = compactMap(map[string]any{
		"display_name":  companionTraceText(displayName, 80),
		"message_count": len(messages),
		"oldest_time":   stringMapValue(query, "oldest_time"),
		"newest_time":   stringMapValue(query, "newest_time"),
		"media":         media,
	})
	trace.Samples = companionTraceMessageSamples(messages, len(messages))
}

func companionFillMessageContextTrace(trace *companionToolTrace, data any) {
	ctx := mapAny(data)
	query := mapAny(ctx["query"])
	messages := mapSliceAny(ctx["messages"])
	displayName := firstNonEmpty(stringMapValue(query, "display_name"), stringMapValue(query, "chat"), "微信会话")
	trace.Summary = fmt.Sprintf("读取「%s」消息上下文，返回 %d 条", companionTraceText(displayName, 42), len(messages))
	trace.Result = compactMap(map[string]any{
		"display_name":  companionTraceText(displayName, 80),
		"message_count": len(messages),
		"media":         companionTraceMediaCounts(messages),
	})
	trace.Samples = companionTraceMessageSamples(messages, len(messages))
}

func companionFillResolveChatTrace(trace *companionToolTrace, data any) {
	res := mapAny(data)
	candidates := mapSliceAny(res["candidates"])
	names := []string{}
	for _, candidate := range candidates {
		name := firstNonEmpty(stringMapValue(candidate, "display_name"), stringMapValue(candidate, "remark"), stringMapValue(candidate, "nick_name"), stringMapValue(candidate, "username"))
		if name != "" {
			names = append(names, companionTraceText(name, 36))
		}
	}
	query := companionSafeDisplayValue(firstNonEmpty(stringMapValue(res, "query"), stringMapValue(res, "keyword")))
	trace.Summary = fmt.Sprintf("解析会话，找到 %d 个候选", len(candidates))
	trace.Result = compactMap(map[string]any{
		"query":         query,
		"session_count": len(candidates),
		"sessions":      names,
	})
}

func companionFillContactsTrace(trace *companionToolTrace, data any) {
	contacts := mapSliceAny(data)
	if len(contacts) == 0 {
		contacts = mapSliceAny(mapAny(data)["contacts"])
	}
	names := []string{}
	for _, contact := range contacts {
		name := firstNonEmpty(stringMapValue(contact, "display_name"), stringMapValue(contact, "remark"), stringMapValue(contact, "nick_name"), stringMapValue(contact, "username"))
		if name != "" {
			names = append(names, companionTraceText(name, 36))
		}
	}
	trace.Summary = fmt.Sprintf("查找联系人，返回 %d 个结果", len(contacts))
	trace.Result = compactMap(map[string]any{
		"contact_count": len(contacts),
		"contacts":      names,
	})
}

func companionFillSearchTrace(trace *companionToolTrace, data any) {
	rows := mapSliceAny(data)
	search := mapAny(data)
	if len(rows) == 0 {
		rows = mapSliceAny(search["messages"])
	}
	messages := companionRowsToMessageSamples(rows)
	trace.Summary = fmt.Sprintf("全文搜索，命中 %d 条", len(rows))
	trace.Result = compactMap(map[string]any{
		"keyword":       companionSafeDisplayValue(stringMapValue(mapAny(search["query"]), "keyword")),
		"hit_count":     len(rows),
		"message_count": len(rows),
	})
	trace.Samples = companionTraceMessageSamples(messages, len(messages))
}

func companionFillSearchWithContextTrace(trace *companionToolTrace, data any) {
	search := mapAny(data)
	query := mapAny(search["query"])
	hits := mapSliceAny(search["hits"])
	messages := []map[string]any{}
	contextCount := 0
	for _, hit := range hits {
		if msg := mapAny(hit["message"]); msg != nil {
			messages = append(messages, msg)
		}
		if ctx := mapAny(hit["context"]); ctx != nil {
			contextMessages := mapSliceAny(ctx["messages"])
			if len(contextMessages) > 0 {
				contextCount++
			}
		}
	}
	if n := intMapValue(query, "contexts_returned"); n > 0 {
		contextCount = n
	}
	keyword := companionSafeDisplayValue(stringMapValue(query, "keyword"))
	trace.Summary = fmt.Sprintf("搜索上下文，命中 %d 条，展开 %d 个上下文", len(hits), contextCount)
	trace.Result = compactMap(map[string]any{
		"keyword":       keyword,
		"hit_count":     len(hits),
		"context_count": contextCount,
		"message_count": len(messages),
	})
	trace.Samples = companionTraceMessageSamples(messages, len(messages))
}

func companionFillMediaResourcesTrace(trace *companionToolTrace, data any) {
	rows := mapSliceAny(data)
	if len(rows) == 0 {
		rows = mapSliceAny(mapAny(data)["media"])
	}
	resourceCount := 0
	for _, row := range rows {
		resourceCount += len(mapSliceAny(row["resources"]))
	}
	trace.Summary = fmt.Sprintf("读取媒体资源，匹配 %d 条消息 / %d 个资源", len(rows), resourceCount)
	trace.Result = compactMap(map[string]any{
		"message_count":  len(rows),
		"resource_count": resourceCount,
	})
	trace.Samples = companionTraceMessageSamples(companionRowsToMessageSamples(rows), len(rows))
}

func companionFillGroupMembersTrace(trace *companionToolTrace, data any) {
	members := mapSliceAny(data)
	if len(members) == 0 {
		members = mapSliceAny(mapAny(data)["members"])
	}
	names := []string{}
	for _, member := range members {
		name := firstNonEmpty(stringMapValue(member, "display_name"), stringMapValue(member, "remark"), stringMapValue(member, "nick_name"), stringMapValue(member, "username"))
		if name != "" {
			names = append(names, companionTraceText(name, 36))
		}
	}
	trace.Summary = fmt.Sprintf("读取群成员，返回 %d 个成员", len(members))
	trace.Result = compactMap(map[string]any{
		"member_count": len(members),
		"sessions":     names,
	})
}

func companionFillReadEventsTrace(trace *companionToolTrace, data any) {
	env := mapAny(data)
	events := mapSliceAny(env["events"])
	messages := []map[string]any{}
	for _, event := range events {
		if msg := mapAny(event["message"]); msg != nil {
			messages = append(messages, msg)
		}
	}
	trace.Summary = fmt.Sprintf("观察新消息事件，返回 %d 个事件", len(events))
	trace.Result = compactMap(map[string]any{
		"event_count":   len(events),
		"message_count": len(messages),
	})
	trace.Samples = companionTraceMessageSamples(messages, len(messages))
}

func companionFillTextTrace(trace *companionToolTrace, data any, prefix string) {
	text := companionTraceText(companionToolDataText(data), companionCPUToolOutputMax)
	if text == "" {
		trace.Summary = prefix + "完成"
		return
	}
	trace.Summary = prefix + "：" + companionFirstOutputLine(text)
	trace.Result = compactMap(map[string]any{"text": text})
}

func companionToolDataText(data any) string {
	switch x := data.(type) {
	case string:
		return x
	case map[string]any:
		if version := stringMapValue(x, "version"); version != "" {
			return strings.TrimSpace(firstNonEmpty(stringMapValue(x, "name"), "wechat-cli") + " " + version)
		}
		if commands := mapSliceAny(x["commands"]); len(commands) > 0 {
			return fmt.Sprintf("%d 个命令", len(commands))
		}
		return firstNonEmpty(stringMapValue(x, "text"), stringMapValue(x, "message"), stringMapValue(x, "output"))
	default:
		return strings.TrimSpace(fmt.Sprint(data))
	}
}

func companionRowsToMessageSamples(rows []map[string]any) []map[string]any {
	messages := []map[string]any{}
	for _, row := range rows {
		if msg := mapAny(row["message"]); msg != nil {
			messages = append(messages, msg)
			continue
		}
		messages = append(messages, row)
	}
	return messages
}

func companionTraceMediaCounts(messages []map[string]any) map[string]any {
	counts := map[string]any{}
	add := func(key string, n int) {
		if n <= 0 {
			return
		}
		counts[key] = intMapValue(counts, key) + n
	}
	for _, msg := range messages {
		kind := strings.ToLower(firstNonEmpty(stringMapValue(msg, "kind"), stringMapValue(msg, "kind_name")))
		imageRows := len(mapSliceAny(msg["images"]))
		if imageRows > 0 {
			add("images", imageRows)
		} else if strings.Contains(kind, "image") || strings.Contains(kind, "img") {
			add("images", 1)
		}
		add("videos", len(mapSliceAny(msg["videos"])))
		add("files", len(mapSliceAny(msg["files"])))
		if msg["link"] != nil {
			add("links", 1)
		}
		if msg["voice"] != nil {
			add("voices", 1)
		}
	}
	return compactMap(counts)
}

func companionTraceMessageSamples(messages []map[string]any, limit int) []map[string]any {
	if limit <= 0 || len(messages) == 0 {
		return nil
	}
	start := 0
	if len(messages) > limit {
		start = len(messages) - limit
	}
	out := []map[string]any{}
	for _, msg := range messages[start:] {
		text := strings.TrimSpace(stringMapValue(msg, "text"))
		if text == "" {
			text = companionMediaSummary(msg)
		}
		item := compactMap(map[string]any{
			"time":   firstNonEmpty(stringMapValue(msg, "time"), stringMapValue(msg, "time_iso")),
			"sender": companionTraceText(firstNonEmpty(stringMapValue(msg, "sender"), stringMapValue(msg, "sender_wxid"), "?"), 32),
			"kind":   firstNonEmpty(stringMapValue(msg, "kind"), "message"),
			"text":   companionTraceText(strings.ReplaceAll(text, "\n", " "), 86),
			"media":  companionTraceMessageMediaLabels(msg),
		})
		out = append(out, item)
	}
	return out
}

func companionTraceMessageMediaLabels(msg map[string]any) []string {
	labels := []string{}
	kind := strings.ToLower(firstNonEmpty(stringMapValue(msg, "kind"), stringMapValue(msg, "kind_name")))
	if n := len(mapSliceAny(msg["images"])); n > 0 {
		labels = append(labels, fmt.Sprintf("图片 x%d", n))
	} else if strings.Contains(kind, "image") || strings.Contains(kind, "img") {
		labels = append(labels, "图片")
	}
	if n := len(mapSliceAny(msg["videos"])); n > 0 {
		labels = append(labels, fmt.Sprintf("视频 x%d", n))
	}
	if n := len(mapSliceAny(msg["files"])); n > 0 {
		labels = append(labels, fmt.Sprintf("文件 x%d", n))
	}
	if msg["link"] != nil {
		labels = append(labels, "链接")
	}
	if msg["voice"] != nil {
		labels = append(labels, "语音")
	}
	return labels
}

func companionTraceText(s string, max int) string {
	s = strings.TrimSpace(s)
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home, "~")
	}
	return truncateRunes(s, max)
}

func intMapValue(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}

func companionSaveUploadedFile(header *multipart.FileHeader) (companionAttachment, error) {
	src, err := header.Open()
	if err != nil {
		return companionAttachment{}, err
	}
	defer src.Close()

	stateDir, err := appStateDir()
	if err != nil {
		return companionAttachment{}, err
	}
	now := time.Now()
	dir := filepath.Join(stateDir, "companion-uploads", now.Format("20060102"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return companionAttachment{}, err
	}
	id := companionRandomID()
	name := safeCacheID(filepath.Base(header.Filename))
	if name == "default" {
		name = "attachment"
	}
	dstPath := filepath.Join(dir, id+"-"+name)
	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return companionAttachment{}, err
	}
	defer dst.Close()
	limited := io.LimitReader(src, companionUploadFileMaxSize+1)
	n, err := io.Copy(dst, limited)
	if err != nil {
		return companionAttachment{}, err
	}
	if n > companionUploadFileMaxSize {
		_ = os.Remove(dstPath)
		return companionAttachment{}, fmt.Errorf("%s exceeds 64MB", header.Filename)
	}
	mimeType := strings.TrimSpace(header.Header.Get("Content-Type"))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	attachment := companionAttachment{
		ID:   id,
		Kind: companionAttachmentKind(name, mimeType),
		Name: name,
		MIME: mimeType,
		Size: n,
		Path: dstPath,
		URL:  "/api/attachment/" + now.Format("20060102") + "/" + url.PathEscape(filepath.Base(dstPath)),
	}
	if companionAttachmentLooksText(name, mimeType) {
		if preview, err := companionReadTextPreview(dstPath, companionAttachmentTextMax); err == nil {
			attachment.TextPreview = preview
		}
	}
	return attachment, nil
}

func companionAttachmentPathFromURL(rel string) (string, error) {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid attachment path")
	}
	day := safeCacheID(parts[0])
	name, err := url.PathUnescape(parts[1])
	if err != nil {
		return "", err
	}
	name = safeCacheID(filepath.Base(name))
	if day == "default" || name == "default" {
		return "", fmt.Errorf("invalid attachment path")
	}
	stateDir, err := appStateDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(stateDir, "companion-uploads")
	path := filepath.Join(root, day, name)
	cleanRoot := filepath.Clean(root) + string(os.PathSeparator)
	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath, cleanRoot) {
		return "", fmt.Errorf("invalid attachment path")
	}
	if info, err := os.Stat(cleanPath); err != nil || info.IsDir() {
		return "", fmt.Errorf("attachment not found")
	}
	return cleanPath, nil
}

func companionTrustedAttachments(items []companionAttachment) []companionAttachment {
	if len(items) == 0 {
		return nil
	}
	root, err := companionUploadRoot()
	if err != nil {
		return nil
	}
	cleanRoot := filepath.Clean(root) + string(os.PathSeparator)
	out := make([]companionAttachment, 0, len(items))
	for _, item := range items {
		path := filepath.Clean(strings.TrimSpace(item.Path))
		if path == "" || !strings.HasPrefix(path, cleanRoot) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		name := safeCacheID(filepath.Base(firstNonEmpty(item.Name, info.Name())))
		if name == "default" {
			name = info.Name()
		}
		mimeType := strings.TrimSpace(item.MIME)
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		next := companionAttachment{
			ID:   safeCacheID(item.ID),
			Kind: companionAttachmentKind(name, mimeType),
			Name: name,
			MIME: mimeType,
			Size: info.Size(),
			Path: path,
			URL:  item.URL,
		}
		if companionAttachmentLooksText(name, mimeType) {
			if preview, err := companionReadTextPreview(path, companionAttachmentTextMax); err == nil {
				next.TextPreview = preview
			}
		}
		out = append(out, next)
	}
	return out
}

func companionUploadRoot() (string, error) {
	stateDir, err := appStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "companion-uploads"), nil
}

func companionRandomID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func companionAttachmentKind(name, mimeType string) string {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case strings.HasPrefix(mimeType, "image/") || ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" || ext == ".heic":
		return "image"
	case companionAttachmentLooksText(name, mimeType):
		return "text"
	default:
		return "file"
	}
}

func companionAttachmentLooksText(name, mimeType string) bool {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	ext := strings.ToLower(filepath.Ext(name))
	if strings.HasPrefix(mimeType, "text/") || strings.Contains(mimeType, "json") || strings.Contains(mimeType, "xml") || strings.Contains(mimeType, "yaml") {
		return true
	}
	switch ext {
	case ".txt", ".md", ".markdown", ".json", ".csv", ".tsv", ".xml", ".yaml", ".yml", ".log", ".go", ".js", ".ts", ".tsx", ".jsx", ".py", ".rs", ".java", ".c", ".cpp", ".h", ".html", ".css", ".sql", ".sh", ".zsh":
		return true
	default:
		return false
	}
}

func companionReadTextPreview(path string, max int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) > max {
		data = data[:max]
	}
	text := strings.TrimSpace(string(data))
	text = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return r
		}
		if r < 32 {
			return -1
		}
		return r
	}, text)
	return text, nil
}

func companionBoundLimit(limit int) int {
	if limit <= 0 {
		return companionDefaultTimelineLimit
	}
	if limit > companionMaxTimelineLimit {
		return companionMaxTimelineLimit
	}
	return limit
}

func queryInt(r *http.Request, key string, def int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return def
	}
	return n
}

func readCompanionJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 2*1024*1024))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeCompanionJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeCompanionError(w http.ResponseWriter, status int, code, message string) {
	writeCompanionJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func companionWriteSSE(w io.Writer, event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		payload = []byte(`{"message":"encode error"}`)
	}
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
}

type companionPrompt struct {
	System string
	User   string
}

func companionBuildPrompt(req companionAskRequest, timeline map[string]any, messages []map[string]any) companionPrompt {
	return companionBuildPromptFromContexts(req, []companionChatContext{{
		Chat:        req.Chat,
		DisplayName: companionTimelineTitle(timeline, req.Chat),
		Timeline:    timeline,
		Messages:    messages,
	}}, nil)
}

func companionBuildPromptFromContexts(req companionAskRequest, contexts []companionChatContext, contextErrors []string) companionPrompt {
	mode := companionMode(req.Mode)
	chats := companionAskChats(req)
	explicitChats := len(chats) > 0
	system := strings.Join([]string{
		"你是用户的本地微信助手。",
		"本机已挂载 wechat-cli；需要微信内容时直接调用 CLI 读取。",
		"不要声称已读到未读取的微信内容，也不要声称已发送微信消息。",
		"用中文回答。",
	}, "\n")

	var b strings.Builder
	if q := strings.TrimSpace(req.Question); q != "" {
		b.WriteString("用户问题：")
		b.WriteString(q)
	} else {
		b.WriteString("用户问题：")
		b.WriteString(companionModeInstruction(mode))
	}
	if explicitChats {
		b.WriteString("\n\n用户通过 @ 提到的微信会话目标：")
		b.WriteString(strings.Join(chats, "、"))
	}
	if section := companionPromptAttachmentSection(req.Attachments); section != "" {
		b.WriteString(section)
	}
	if section := companionPromptHistorySection(req.History); section != "" {
		b.WriteString(section)
	}

	if len(contexts) == 0 {
		b.WriteString("\n\n当前没有预置微信聊天正文。")
		if len(contextErrors) > 0 {
			b.WriteString("\n会话读取提示：部分候选会话暂时不可读。")
		}
		return companionPrompt{System: system, User: b.String()}
	}

	if explicitChats {
		b.WriteString("\n\n用户显式指定了以下微信会话上下文。")
	} else {
		b.WriteString("\n\n以下是底层工具已读取到的微信上下文。")
	}

	for i, ctx := range contexts {
		query := mapAny(ctx.Timeline["query"])
		b.WriteString("\n\n会话 ")
		b.WriteString(fmt.Sprintf("%d", i+1))
		b.WriteString("：")
		b.WriteString(firstNonEmpty(ctx.DisplayName, stringMapValue(query, "display_name"), stringMapValue(query, "chat"), ctx.Chat))
		if newest := stringMapValue(query, "newest_time"); newest != "" {
			b.WriteString("\n最新消息时间：")
			b.WriteString(newest)
		}
		if oldest := stringMapValue(query, "oldest_time"); oldest != "" {
			b.WriteString("\n最早消息时间：")
			b.WriteString(oldest)
		}
		b.WriteString("\n聊天上下文片段：\n")
		for _, line := range companionRenderedLines(ctx.Messages, companionPromptMaxMessages) {
			if b.Len()+len(line)+1 > companionPromptMaxChars {
				b.WriteString("[上下文因长度限制截断]\n")
				return companionPrompt{System: system, User: b.String()}
			}
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	if len(contextErrors) > 0 {
		b.WriteString("\n\n读取提示：还有部分指定会话暂时不可读，回答时不要假装已读到它们。")
	}
	return companionPrompt{System: system, User: b.String()}
}

func companionTrustedHistory(items []companionHistory) []companionHistory {
	if len(items) == 0 {
		return nil
	}
	start := 0
	if len(items) > companionCPUHistoryMaxItems {
		start = len(items) - companionCPUHistoryMaxItems
	}
	out := make([]companionHistory, 0, len(items)-start)
	for _, item := range items[start:] {
		role := strings.ToLower(strings.TrimSpace(item.Role))
		if role != "user" && role != "assistant" {
			continue
		}
		text := strings.TrimSpace(item.Text)
		text = truncateRunes(text, companionCPUHistoryTextMax)
		targets := []string{}
		for _, target := range item.Targets {
			target = truncateRunes(strings.TrimSpace(target), 96)
			if target != "" {
				targets = append(targets, target)
			}
			if len(targets) >= 8 {
				break
			}
		}
		attachments := companionTrustedAttachments(item.Attachments)
		if text == "" && len(targets) == 0 && len(attachments) == 0 {
			continue
		}
		out = append(out, companionHistory{
			Role:        role,
			Text:        text,
			Targets:     targets,
			Attachments: attachments,
		})
	}
	return out
}

func companionPromptHistorySection(history []companionHistory) string {
	if len(history) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n本轮之前的浏览器聊天历史。它只用于理解追问指代，不代表已经读取了新的微信正文：")
	for _, item := range history {
		role := "助手"
		if item.Role == "user" {
			role = "用户"
		}
		b.WriteString("\n- ")
		b.WriteString(role)
		if len(item.Targets) > 0 {
			b.WriteString("（目标：")
			b.WriteString(strings.Join(item.Targets, "、"))
			b.WriteString("）")
		}
		if text := strings.TrimSpace(item.Text); text != "" {
			b.WriteString("：")
			b.WriteString(strings.ReplaceAll(text, "\n", " "))
		}
		if len(item.Attachments) > 0 {
			names := []string{}
			for _, attachment := range item.Attachments {
				names = append(names, firstNonEmpty(attachment.Name, attachment.Kind, "附件"))
			}
			b.WriteString("（附件：")
			b.WriteString(strings.Join(names, "、"))
			b.WriteString("）")
		}
	}
	return b.String()
}

func companionPromptAttachmentSection(attachments []companionAttachment) string {
	if len(attachments) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n用户本轮附加了文件/图片。需要时读取这些本机路径；不能仅凭文件名臆测。")
	for i, item := range attachments {
		if strings.TrimSpace(item.Path) == "" {
			continue
		}
		b.WriteString("\n附件 ")
		b.WriteString(fmt.Sprintf("%d", i+1))
		b.WriteString("：")
		b.WriteString(firstNonEmpty(item.Name, "attachment"))
		b.WriteString("\n- 类型：")
		b.WriteString(firstNonEmpty(item.Kind, "file"))
		if item.MIME != "" {
			b.WriteString(" / ")
			b.WriteString(item.MIME)
		}
		if item.Size > 0 {
			b.WriteString("\n- 大小：")
			b.WriteString(fmt.Sprintf("%d bytes", item.Size))
		}
		b.WriteString("\n- 本机路径：")
		b.WriteString(item.Path)
		if preview := strings.TrimSpace(item.TextPreview); preview != "" {
			b.WriteString("\n- 文本预览：\n")
			b.WriteString(truncateRunes(preview, companionAttachmentTextMax))
		}
	}
	return b.String()
}

func companionTimelineTitle(timeline map[string]any, fallback string) string {
	query := mapAny(timeline["query"])
	return firstNonEmpty(stringMapValue(query, "display_name"), stringMapValue(query, "chat"), fallback)
}

func companionMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case "summary", "value", "reply", "research", "custom":
		return strings.TrimSpace(mode)
	default:
		return "summary"
	}
}

func companionModeInstruction(mode string) string {
	switch companionMode(mode) {
	case "value":
		return "提炼这段聊天里的高价值信息、待办、机会、风险和需要继续跟进的人或主题。"
	case "reply":
		return "给用户生成 2 到 3 条可直接复制但不会自动发送的微信回复草稿；每条标明语气。"
	case "research":
		return "解释聊天里正在讨论的问题，区分已知事实、上下文推断和需要外部调研确认的点。"
	case "custom":
		return "回答用户补充问题；如果上下文不足，明确指出缺口。"
	default:
		return "概括最近这段聊天在聊什么，按主题归纳，并指出值得用户注意的信息。"
	}
}

func companionRenderedLines(messages []map[string]any, maxMessages int) []string {
	if maxMessages <= 0 || len(messages) <= maxMessages {
		return companionRenderMessageSlice(messages)
	}
	return companionRenderMessageSlice(messages[len(messages)-maxMessages:])
}

func companionRenderMessageSlice(messages []map[string]any) []string {
	lines := make([]string, 0, len(messages))
	for _, msg := range messages {
		lines = append(lines, companionRenderMessageLine(msg))
	}
	return lines
}

func companionRenderMessageLine(msg map[string]any) string {
	id := companionMessageID(msg)
	ts := firstNonEmpty(stringMapValue(msg, "time"), stringMapValue(msg, "time_iso"))
	sender := firstNonEmpty(stringMapValue(msg, "sender"), stringMapValue(msg, "sender_wxid"), "?")
	kind := firstNonEmpty(stringMapValue(msg, "kind"), "message")
	text := strings.TrimSpace(stringMapValue(msg, "text"))
	if text == "" {
		text = companionMediaSummary(msg)
	}
	text = truncateRunes(strings.ReplaceAll(text, "\n", " "), companionMessageTextMaxChars)
	selfTag := ""
	if v, ok := msg["is_from_me"].(bool); ok && v {
		selfTag = " [我]"
	}
	return fmt.Sprintf("[%s] %s%s %s <%s>: %s", id, ts, selfTag, sender, kind, text)
}

func companionMessageID(msg map[string]any) string {
	id := mapAny(msg["id"])
	if id != nil {
		if n := int64MapValue(id, "local_id"); n != 0 {
			return fmt.Sprintf("%d", n)
		}
		if s := stringMapValue(id, "server_id_str"); s != "" {
			return s
		}
	}
	return "?"
}

func companionMediaSummary(msg map[string]any) string {
	parts := []string{}
	for _, key := range []string{"images", "videos", "files"} {
		if rows := mapSliceAny(msg[key]); len(rows) > 0 {
			parts = append(parts, fmt.Sprintf("[%s x%d]", key, len(rows)))
		}
	}
	for _, key := range []string{"link", "music", "miniprogram", "forward_chat", "quote", "transfer", "red_packet", "location", "voice"} {
		if msg[key] != nil {
			parts = append(parts, "["+key+"]")
		}
	}
	if len(parts) == 0 {
		return "[非文本消息]"
	}
	return strings.Join(parts, " ")
}

func companionContextMeta(req companionAskRequest, timeline map[string]any, messages []map[string]any, promptChars int) map[string]any {
	return companionContextMetaFromContexts(req, []companionChatContext{{
		Chat:        req.Chat,
		DisplayName: companionTimelineTitle(timeline, req.Chat),
		Timeline:    timeline,
		Messages:    messages,
	}}, nil, promptChars)
}

func companionContextMetaFromContexts(req companionAskRequest, contexts []companionChatContext, contextErrors []string, promptChars int) map[string]any {
	source := "none"
	if len(companionAskChats(req)) > 0 {
		source = "explicit"
	}
	if len(contexts) == 0 {
		return compactMap(map[string]any{
			"chat":          req.Chat,
			"chat_count":    0,
			"message_count": 0,
			"source":        source,
			"errors":        contextErrors,
			"prompt_chars":  promptChars,
		})
	}
	totalMessages := 0
	chats := []map[string]any{}
	for _, ctx := range contexts {
		totalMessages += len(ctx.Messages)
		query := mapAny(ctx.Timeline["query"])
		chats = append(chats, compactMap(map[string]any{
			"chat":          firstNonEmpty(stringMapValue(query, "chat"), ctx.Chat),
			"talker":        stringMapValue(query, "talker"),
			"display_name":  firstNonEmpty(stringMapValue(query, "display_name"), ctx.DisplayName),
			"message_count": len(ctx.Messages),
			"oldest_time":   stringMapValue(query, "oldest_time"),
			"newest_time":   stringMapValue(query, "newest_time"),
		}))
	}
	first := contexts[0]
	query := mapAny(first.Timeline["query"])
	return compactMap(map[string]any{
		"chat":          firstNonEmpty(stringMapValue(query, "chat"), first.Chat),
		"talker":        stringMapValue(query, "talker"),
		"display_name":  firstNonEmpty(stringMapValue(query, "display_name"), first.DisplayName),
		"message_count": totalMessages,
		"chat_count":    len(contexts),
		"chats":         chats,
		"source":        source,
		"errors":        contextErrors,
		"prompt_chars":  promptChars,
	})
}

func prependPathEnv(env []string, dir string) []string {
	if strings.TrimSpace(dir) == "" {
		return env
	}
	prefix := "PATH="
	for i, item := range env {
		if strings.HasPrefix(item, prefix) {
			current := strings.TrimPrefix(item, prefix)
			if current == "" {
				env[i] = prefix + dir
			} else {
				env[i] = prefix + dir + string(os.PathListSeparator) + current
			}
			return env
		}
	}
	return append(env, prefix+dir)
}

const companionHTML = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>微信助手 V1</title>
<style>
:root {
  color-scheme: light;
  --bg: #f7f7f5;
  --panel: rgba(255,255,255,.72);
  --panel-solid: #ffffff;
  --line: rgba(0,0,0,.10);
  --line-strong: rgba(0,0,0,.18);
  --text: #1f1f1f;
  --muted: #7a7a7d;
  --soft: #f5f5f4;
  --green: #2fb866;
  --danger: #b84a4a;
  --radius: 10px;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
}
* { box-sizing: border-box; }
html, body { width: 100%; height: 100%; }
body {
  margin: 0;
  color: var(--text);
  background: linear-gradient(180deg, rgba(255,255,255,.96), rgba(246,246,244,.96));
  overflow: hidden;
}
button, input, textarea { font: inherit; }
button {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel-solid);
  color: var(--text);
  cursor: pointer;
}
button:hover { border-color: var(--line-strong); background: #fbfbfb; }
button:disabled { opacity: .55; cursor: default; }
input, textarea {
  width: 100%;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel-solid);
  color: var(--text);
  outline: none;
}
input:focus, textarea:focus { border-color: rgba(0,0,0,.22); box-shadow: 0 0 0 3px rgba(0,0,0,.045); }
input:disabled, textarea:disabled {
  color: #9b9da0;
  background: rgba(246,246,246,.84);
}
.shell {
  height: 100vh;
  display: grid;
  grid-template-columns: 188px minmax(0, 1fr);
  background: rgba(255,255,255,.62);
}
.shell.sidebar-collapsed {
  grid-template-columns: 0 minmax(0, 1fr);
}
.shell.sidebar-collapsed .sidebar {
  opacity: 0;
  pointer-events: none;
}
.sidebar {
  min-width: 0;
  border-right: 1px solid var(--line);
  background: rgba(247,247,246,.78);
  display: grid;
  grid-template-rows: 56px auto minmax(0, 1fr);
}
.sidebar-head {
  -webkit-app-region: drag;
  display: flex;
  align-items: center;
  padding: 10px 10px 6px;
}
.new-chat {
  -webkit-app-region: no-drag;
  width: 100%;
  height: 34px;
  border-radius: 10px;
  background: rgba(255,255,255,.74);
  color: #353936;
  font-size: 13px;
  text-align: left;
  padding: 0 10px;
}
.sidebar-section {
  padding: 8px 10px 6px;
  color: #969a9e;
  font-size: 11px;
}
.history-list {
  min-height: 0;
  overflow: auto;
  padding: 0 8px 12px;
}
.history-empty {
  padding: 10px 4px;
  color: #a3a7aa;
  font-size: 12px;
}
.history-item {
  width: 100%;
  border: 0;
  border-radius: 10px;
  background: transparent;
  color: #343836;
  display: grid;
  grid-template-columns: minmax(0, 1fr) 24px;
  gap: 4px;
  align-items: center;
  padding: 8px;
  text-align: left;
}
.history-item:hover {
  background: rgba(0,0,0,.045);
}
.history-item.active {
  background: rgba(0,0,0,.075);
}
.history-main {
  min-width: 0;
}
.history-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  font-weight: 620;
}
.history-meta {
  margin-top: 3px;
  color: #8f9498;
  font-size: 11px;
}
.history-delete {
  width: 24px;
  height: 24px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: #8f9498;
  opacity: 0;
}
.history-item:hover .history-delete,
.history-item.active .history-delete {
  opacity: 1;
}
.history-delete:hover {
  background: rgba(0,0,0,.08);
  color: #4a4f52;
}
.chat-pane {
  min-width: 0;
  min-height: 0;
  display: grid;
  grid-template-rows: 56px minmax(0, 1fr) auto;
}
.topbar {
  -webkit-app-region: drag;
  display: grid;
  grid-template-columns: 44px 1fr 44px;
  align-items: center;
  padding: 10px 14px 6px;
}
.title {
  text-align: center;
  font-size: 16px;
  font-weight: 700;
}
.sidebar-toggle {
  -webkit-app-region: no-drag;
  justify-self: start;
  width: 32px;
  height: 32px;
  display: grid;
  place-items: center;
  border: 0;
  background: transparent;
  color: #68706a;
  font-size: 18px;
}
.sidebar-toggle:hover {
  background: rgba(0,0,0,.04);
}
.menu {
  -webkit-app-region: no-drag;
  justify-self: end;
  width: 32px;
  height: 32px;
  display: grid;
  place-items: center;
  border: 0;
  background: transparent;
  color: #68706a;
}
.menu-lines,
.menu-lines::before,
.menu-lines::after {
  content: "";
  display: block;
  width: 18px;
  height: 2px;
  border-radius: 2px;
  background: currentColor;
}
.menu-lines::before { transform: translateY(-6px); }
.menu-lines::after { transform: translateY(4px); }
.content {
  min-height: 0;
  overflow: hidden;
}
.conversation {
  height: 100%;
  overflow: auto;
  padding: 16px 18px 20px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.empty-state {
  margin: auto;
  color: #9a9ca0;
  font-size: 15px;
}
.message {
  max-width: 90%;
  display: grid;
  gap: 5px;
}
.message.user {
  align-self: flex-end;
  justify-items: end;
}
.message.assistant {
  align-self: flex-start;
  justify-items: start;
}
.message.thinking {
  max-width: 100%;
}
.message-bubble {
  padding: 10px 13px;
  border-radius: 17px;
  overflow-wrap: anywhere;
  font-size: 14px;
  line-height: 1.65;
}
.plain-text {
  white-space: pre-wrap;
}
.message.user .message-bubble {
  background: #33c16f;
  color: white;
  white-space: pre-wrap;
}
.message.assistant .message-bubble {
  border: 1px solid var(--line);
  background: rgba(255,255,255,.74);
  color: var(--text);
}
.message.thinking .message-bubble {
  border: 0;
  background: transparent;
  padding: 0 4px;
}
.thinking-line {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  color: #8c9296;
  font-size: 13px;
}
.thinking-dots {
  display: inline-flex;
  gap: 3px;
}
.thinking-dots span {
  width: 4px;
  height: 4px;
  border-radius: 99px;
  background: currentColor;
  opacity: .35;
  animation: thinkingPulse 1.2s infinite ease-in-out;
}
.thinking-dots span:nth-child(2) { animation-delay: .16s; }
.thinking-dots span:nth-child(3) { animation-delay: .32s; }
@keyframes thinkingPulse {
  0%, 80%, 100% { opacity: .28; transform: translateY(0); }
  40% { opacity: .8; transform: translateY(-2px); }
}
.event-log {
  margin: 0 0 8px;
  color: #74797e;
  font-size: 12px;
  line-height: 1.5;
}
.event-log::before {
  content: "• ";
  color: #a0a4a8;
}
.markdown {
  display: grid;
  gap: 8px;
}
.markdown > * {
  margin: 0;
}
.markdown p {
  margin: 0;
}
.markdown h1,
.markdown h2,
.markdown h3,
.markdown h4,
.markdown h5,
.markdown h6 {
  margin: 4px 0 2px;
  font-size: 15px;
  line-height: 1.45;
  font-weight: 760;
}
.markdown ul,
.markdown ol {
  margin: 0;
  padding-left: 21px;
}
.markdown li {
  margin: 3px 0;
  padding-left: 1px;
}
.markdown blockquote {
  border-left: 3px solid rgba(0,0,0,.16);
  padding: 2px 0 2px 10px;
  color: #5f6368;
}
.markdown pre {
  max-width: 100%;
  margin: 2px 0;
  padding: 11px 12px;
  border: 1px solid rgba(0,0,0,.08);
  border-radius: 9px;
  background: #f6f6f5;
  overflow: auto;
  white-space: pre;
}
.markdown code {
  border-radius: 5px;
  background: rgba(0,0,0,.055);
  padding: 1px 4px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
  font-size: .92em;
}
.markdown pre code {
  display: block;
  background: transparent;
  padding: 0;
  border-radius: 0;
  font-size: 12.5px;
  line-height: 1.55;
}
.markdown table {
  width: 100%;
  border-collapse: collapse;
  display: block;
  overflow: auto;
  font-size: 13px;
}
.markdown th,
.markdown td {
  border: 1px solid rgba(0,0,0,.10);
  padding: 6px 8px;
  text-align: left;
  vertical-align: top;
}
.markdown th {
  background: rgba(0,0,0,.035);
  font-weight: 700;
}
.markdown a {
  color: #0a66c2;
  text-decoration: none;
}
.markdown a:hover {
  text-decoration: underline;
}
.markdown hr {
  width: 100%;
  border: 0;
  border-top: 1px solid var(--line);
}
.tool-trace {
  display: grid;
  gap: 7px;
  margin-bottom: 10px;
}
.tool-call {
  border: 1px solid rgba(0,0,0,.08);
  border-radius: 10px;
  background: rgba(248,248,247,.92);
  overflow: hidden;
}
.tool-call summary {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: start;
  gap: 8px;
  padding: 9px 10px;
  cursor: pointer;
  list-style: none;
}
.tool-call summary::-webkit-details-marker {
  display: none;
}
.tool-dot {
  width: 7px;
  height: 7px;
  border-radius: 99px;
  background: #34a853;
  margin-top: 6px;
}
.tool-call.running .tool-dot {
  background: #8b8f94;
}
.tool-call.error .tool-dot {
  background: var(--danger);
}
.tool-title {
  min-width: 0;
  color: #3c4043;
  font-size: 13px;
  font-weight: 690;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tool-heading {
  min-width: 0;
  display: grid;
  gap: 2px;
}
.tool-subtitle {
  min-width: 0;
  color: #73787d;
  font-size: 11px;
  line-height: 1.35;
  overflow: hidden;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow-wrap: anywhere;
}
.tool-duration {
  justify-self: end;
  margin-top: 1px;
  color: #8b8f94;
  font-size: 11px;
  white-space: nowrap;
}
.tool-body {
  display: grid;
  gap: 6px;
  padding: 0 10px 10px 25px;
  color: #5f6368;
  font-size: 12px;
  line-height: 1.48;
}
.tool-summary {
  color: #3f4347;
}
.tool-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
}
.tool-pill {
  border: 1px solid rgba(0,0,0,.075);
  border-radius: 999px;
  background: rgba(255,255,255,.76);
  padding: 2px 7px;
  color: #6a6e73;
}
.tool-samples {
  display: grid;
  gap: 4px;
}
.tool-sample {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tool-media {
  color: #407a50;
}
.tool-json {
  max-height: 280px;
  margin: 2px 0 0;
  padding: 9px 10px;
  border: 1px solid rgba(0,0,0,.07);
  border-radius: 8px;
  background: rgba(255,255,255,.7);
  color: #555b61;
  overflow: auto;
  white-space: pre;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
  font-size: 11px;
  line-height: 1.45;
}
.message.loading .message-bubble {
  color: var(--muted);
}
.message.error .message-bubble {
  color: var(--danger);
  border-color: rgba(204,45,45,.25);
  background: rgba(255,245,245,.82);
}
.message.notice .message-bubble {
  color: #74787c;
  border-color: rgba(0,0,0,.075);
  background: rgba(247,247,247,.76);
}
.message-attachments {
  display: grid;
  gap: 7px;
  margin-top: 8px;
}
.message-attachment {
  display: grid;
  grid-template-columns: 34px minmax(0, 1fr);
  align-items: center;
  gap: 8px;
  min-width: 0;
  border-radius: 12px;
  padding: 6px 8px;
  background: rgba(255,255,255,.24);
}
.message.assistant .message-attachment {
  border: 1px solid rgba(0,0,0,.07);
  background: rgba(247,247,246,.86);
}
.attachment-thumb,
.attachment-icon {
  width: 34px;
  height: 34px;
  border-radius: 8px;
}
.attachment-thumb {
  object-fit: cover;
  background: rgba(0,0,0,.08);
}
.attachment-icon {
  display: grid;
  place-items: center;
  background: rgba(255,255,255,.28);
  color: currentColor;
  font-size: 18px;
}
.message.assistant .attachment-icon {
  background: rgba(0,0,0,.045);
}
.attachment-name {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  font-weight: 650;
}
.attachment-meta {
  opacity: .74;
  font-size: 11px;
}
.message-time {
  padding: 0 4px;
  color: #92969a;
  font-size: 11px;
}
.ask-row {
  display: grid;
  grid-template-columns: 38px 1fr 38px;
  gap: 8px;
}
.ask-row.dragging textarea {
  border-color: rgba(51,193,111,.55);
  background: rgba(241,251,246,.92);
}
.ask-row input {
  height: 44px;
  padding: 0 14px;
  font-size: 15px;
  background: rgba(255,255,255,.88);
}
.ask-row textarea {
  min-height: 44px;
  max-height: 132px;
  padding: 11px 14px;
  resize: none;
  overflow-y: auto;
  font-size: 15px;
  line-height: 1.45;
  background: rgba(255,255,255,.88);
}
.send-button {
  height: 44px;
  border-radius: 99px;
  color: #808184;
  font-size: 21px;
  line-height: 1;
}
.attach-button {
  height: 44px;
  border-radius: 99px;
  color: #73777a;
  font-size: 22px;
  line-height: 1;
}
.attachment-input {
  display: none;
}
.attachment-bar {
  display: none;
  gap: 8px;
  margin-bottom: 7px;
  overflow-x: auto;
  padding-bottom: 2px;
}
.attachment-bar.open {
  display: flex;
}
.draft-attachment {
  display: grid;
  grid-template-columns: 30px minmax(0, 104px) 22px;
  align-items: center;
  gap: 7px;
  flex: 0 0 auto;
  border: 1px solid var(--line);
  border-radius: 12px;
  background: rgba(255,255,255,.78);
  padding: 5px 6px;
}
.draft-attachment img,
.draft-file-icon {
  width: 30px;
  height: 30px;
  border-radius: 8px;
}
.draft-attachment img {
  object-fit: cover;
  background: rgba(0,0,0,.05);
}
.draft-file-icon {
  display: grid;
  place-items: center;
  background: rgba(0,0,0,.045);
  color: #6b7074;
  font-size: 15px;
}
.draft-file-name {
  min-width: 0;
  color: #343836;
  font-size: 12px;
  font-weight: 650;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.draft-file-meta {
  color: var(--muted);
  font-size: 10px;
}
.draft-remove {
  width: 22px;
  height: 22px;
  border: 0;
  background: transparent;
  color: #8f9498;
  font-size: 16px;
}
.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  margin-top: 9px;
}
.chip {
  border: 1px solid var(--line);
  border-radius: 99px;
  background: rgba(255,255,255,.72);
  padding: 6px 9px;
  color: #58585c;
  font-size: 12px;
  max-width: 136px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.composer {
  position: relative;
  border-top: 1px solid var(--line);
  background: rgba(255,255,255,.82);
  padding: 10px 18px 12px;
}
.target-bar {
  display: none;
  align-items: center;
  gap: 8px;
  min-height: 28px;
  margin-bottom: 7px;
}
.target-bar.open {
  display: flex;
}
.target-label {
  color: #85888c;
  font-size: 12px;
}
.mention-popover {
  position: absolute;
  left: 18px;
  right: 18px;
  bottom: calc(100% - 2px);
  display: none;
  max-height: 254px;
  overflow: auto;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: rgba(255,255,255,.98);
  box-shadow: 0 18px 44px rgba(0,0,0,.16);
  z-index: 20;
}
.mention-popover.open { display: block; }
.mention-item {
  display: grid;
  gap: 4px;
  padding: 10px 12px;
  border-bottom: 1px solid rgba(0,0,0,.055);
  cursor: pointer;
}
.mention-item:last-child { border-bottom: 0; }
.mention-item.active,
.mention-item:hover {
  background: #f3f8f3;
}
.mention-name {
  color: #202322;
  font-size: 13px;
  font-weight: 690;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.mention-summary {
  color: var(--muted);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.menu-actions {
  display: grid;
  gap: 10px;
}
.menu-action {
  height: 44px;
  border: 1px solid var(--line);
  background: rgba(255,255,255,.7);
  color: #202322;
  text-align: left;
  padding: 0 12px;
}
.menu-status {
  margin-top: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--line);
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.connected {
  display: flex;
  align-items: center;
  gap: 6px;
  color: #6f7671;
  font-size: 12px;
}
.status-dot {
  width: 9px;
  height: 9px;
  border-radius: 99px;
  background: #2eb85c;
}
.status-dot.bad { background: var(--danger); }
.sheet {
  position: fixed;
  inset: 64px 18px 72px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: rgba(255,255,255,.98);
  box-shadow: 0 18px 44px rgba(0,0,0,.18);
  display: none;
  grid-template-rows: 50px minmax(0, 1fr);
  overflow: hidden;
  z-index: 10;
}
.menu-sheet {
  inset: 64px 18px auto;
  height: auto;
}
.sheet.open { display: grid; }
.sheet-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 14px;
  border-bottom: 1px solid var(--line);
  font-weight: 750;
}
.sheet-close {
  width: 32px;
  height: 32px;
  border: 0;
  background: transparent;
  color: #6f7671;
  font-size: 20px;
}
.sheet-body {
  overflow: auto;
  padding: 12px;
}
.session-summary {
  color: var(--muted);
  font-size: 12px;
}
.settings-grid {
  display: grid;
  gap: 12px;
}
.settings-field,
.settings-grid label {
  display: grid;
  gap: 6px;
  color: #68706a;
  font-size: 12px;
}
.settings-label {
  color: #68706a;
  font-size: 12px;
}
.mode-switch {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px;
  padding: 3px;
  border: 1px solid var(--line);
  border-radius: 12px;
  background: rgba(246,246,245,.86);
}
.mode-option {
  height: 42px;
  border: 0;
  border-radius: 9px;
  background: transparent;
  display: grid;
  align-content: center;
  gap: 2px;
  color: #6f7478;
  font-size: 13px;
}
.mode-option small {
  color: #9ba0a4;
  font-size: 10px;
}
.mode-option.active {
  background: white;
  color: #202322;
  box-shadow: 0 1px 5px rgba(0,0,0,.08);
}
.mode-option.active small {
  color: #68706a;
}
.settings-grid input,
.settings-grid select {
	height: 38px;
	padding: 0 10px;
}
.settings-grid select {
	appearance: none;
}
.input-with-action {
  display: grid;
  grid-template-columns: minmax(0, 1fr) 54px;
  gap: 6px;
}
.input-action {
  height: 38px;
  border-radius: 10px;
  color: #68706a;
  font-size: 12px;
}
.field-hint,
.settings-state {
  color: var(--muted);
  font-size: 11px;
  line-height: 1.45;
}
.test-row {
  display: grid;
  grid-template-columns: 112px minmax(0, 1fr);
  gap: 8px;
  align-items: center;
}
.test-button {
  height: 38px;
  border-radius: 10px;
  color: #343836;
  font-size: 13px;
}
.test-result {
  min-width: 0;
  color: var(--muted);
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.test-result.ok { color: #237847; }
.test-result.bad { color: var(--danger); }
.settings-note {
  color: var(--muted);
  font-size: 12px;
  line-height: 1.6;
}
.error {
  color: var(--danger);
}
@media (max-width: 420px) {
  .shell,
  .shell.sidebar-collapsed { grid-template-columns: 1fr; }
  .sidebar { display: none; }
  .content { padding: 8px 14px 14px; }
  .composer { padding-left: 12px; padding-right: 12px; }
  .bottom { padding-left: 10px; padding-right: 10px; }
}
</style>
</head>
<body>
<div id="appShell" class="shell">
  <aside class="sidebar" aria-label="历史会话">
    <div class="sidebar-head"><button id="newChatBtn" class="new-chat" type="button">新对话</button></div>
    <div class="sidebar-section">历史</div>
    <nav id="historyList" class="history-list"></nav>
  </aside>
  <div class="chat-pane">
    <header class="topbar">
      <button id="sidebarToggleBtn" class="sidebar-toggle" type="button" title="收起侧边栏" aria-label="收起侧边栏">‹</button>
      <div class="title">微信助手</div>
      <button id="menuBtn" class="menu" type="button" title="菜单" aria-label="打开菜单"><span class="menu-lines"></span></button>
    </header>

    <main id="conversation" class="content conversation" aria-live="polite"></main>

    <footer class="composer">
      <div id="mentionBox" class="mention-popover"></div>
      <div id="targetBar" class="target-bar"></div>
      <div id="attachmentBar" class="attachment-bar"></div>
      <form id="askForm" class="ask-row">
        <button id="attachBtn" class="attach-button" type="button" title="添加附件" aria-label="添加附件">+</button>
        <input id="attachmentInput" class="attachment-input" type="file" multiple>
        <textarea id="questionInput" placeholder="问问微信… 输入 @ 搜索会话" rows="1" autocomplete="off"></textarea>
        <button id="askBtn" class="send-button" type="submit" title="发送" aria-label="发送问题">↑</button>
      </form>
    </footer>
  </div>
</div>

<section id="menuSheet" class="sheet menu-sheet">
  <div class="sheet-head"><span>菜单</span><button class="sheet-close" type="button" aria-label="关闭菜单" data-close="menuSheet">×</button></div>
  <div class="sheet-body">
    <div class="menu-actions">
      <button id="menuSettingsBtn" class="menu-action" type="button">设置</button>
      <div class="menu-status">
        <span>连接</span>
        <div id="connectionState" class="connected"><span>已连接</span><span id="statusDot" class="status-dot"></span></div>
      </div>
    </div>
  </div>
</section>

<section id="settingsSheet" class="sheet">
  <div class="sheet-head"><span>设置</span><button class="sheet-close" type="button" aria-label="关闭设置" data-close="settingsSheet">×</button></div>
	<div class="sheet-body">
	    <div class="settings-grid">
	      <div class="settings-field">
	        <div class="settings-label">CLI 挂载</div>
	        <div id="settingsState" class="settings-state"></div>
	      </div>
	      <div class="settings-note">微信助手只把 wechat-cli、用户输入和附件本机路径交给后端 CPU；不选择模型、不保存模型密钥、不管理后端会话。</div>
	    </div>
	  </div>
	</section>

<script>
const state = {
  sessions: [],
  chatSessions: [],
  activeChatID: "",
  messages: [],
  messageSeq: 0,
  sessionError: false,
  asking: false,
  draftAttachments: [],
  sidebarCollapsed: false,
  mentions: [],
  mentionRange: null,
  mentionResults: [],
  mentionActive: 0
};
const el = (id) => document.getElementById(id);
const HISTORY_KEY = "wechat_assistant_chat_sessions_v4";
const ACTIVE_HISTORY_KEY = "wechat_assistant_active_chat_id_v4";
const LEGACY_HISTORY_KEYS = [
  "wechat_assistant_chat_sessions_v1",
  "wechat_assistant_active_chat_id_v1",
  "wechat_assistant_chat_sessions_v2",
  "wechat_assistant_active_chat_id_v2",
  "wechat_assistant_chat_sessions_v3",
  "wechat_assistant_active_chat_id_v3"
];
const SIDEBAR_COLLAPSED_KEY = "wechat_assistant_sidebar_collapsed_v1";
const COMPANION_TOKEN = "` + companionTokenPlaceholder + `";

async function api(path, options) {
  const nextOptions = withCompanionToken(options || {});
  const res = await fetch(path, nextOptions);
  const data = await res.json().catch(() => ({}));
  if (!data.ok) {
    const message = data.error && data.error.message ? data.error.message : "请求失败";
    throw new Error(message);
  }
  return data.data;
}

async function apiStream(path, payload, onEvent) {
  const res = await fetch(path, {
    method: "POST",
    headers: companionHeaders({"Content-Type": "application/json"}),
    body: JSON.stringify(payload)
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    const message = data.error && data.error.message ? data.error.message : "请求失败";
    throw new Error(message);
  }
  if (!res.body || !res.body.getReader) {
    throw new Error("stream unsupported");
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const chunk = await reader.read();
    if (chunk.done) break;
    buffer += decoder.decode(chunk.value, {stream: true});
    let index = buffer.indexOf("\n\n");
    while (index >= 0) {
      const frame = buffer.slice(0, index);
      buffer = buffer.slice(index + 2);
      handleSSEFrame(frame, onEvent);
      index = buffer.indexOf("\n\n");
    }
  }
  buffer += decoder.decode();
  if (buffer.trim()) {
    handleSSEFrame(buffer, onEvent);
  }
}

function withCompanionToken(options) {
  const next = Object.assign({}, options || {});
  next.headers = companionHeaders(next.headers || {});
  return next;
}

function companionHeaders(headers) {
  const out = new Headers(headers || {});
  out.set("X-Wechat-Companion-Token", COMPANION_TOKEN);
  return out;
}

function handleSSEFrame(frame, onEvent) {
  let eventName = "message";
  const dataLines = [];
  frame.split("\n").forEach((line) => {
    if (line.startsWith("event:")) eventName = line.slice(6).trim();
    if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
  });
  if (!dataLines.length) return;
  const data = JSON.parse(dataLines.join("\n"));
  onEvent(eventName, data);
}

function setConnected(ok, text) {
  el("connectionState").querySelector("span").textContent = text || connectionLabel();
  el("statusDot").className = "status-dot" + (ok ? "" : " bad");
}

function renderConnectionLabel() {
  el("connectionState").querySelector("span").textContent = connectionLabel();
}

function connectionLabel() {
  return "CLI 已挂载";
}

function setAsking(ok) {
  state.asking = ok;
  el("askBtn").textContent = ok ? "…" : "↑";
  updateAskAvailability();
}

function updateAskAvailability() {
  el("askBtn").disabled = state.asking;
  el("questionInput").disabled = false;
  el("questionInput").placeholder = "问问微信… 输入 @ 搜索会话";
}

function renderDraftAttachments() {
  const bar = el("attachmentBar");
  if (!bar) return;
  const items = Array.isArray(state.draftAttachments) ? state.draftAttachments : [];
  bar.classList.toggle("open", items.length > 0);
  bar.innerHTML = items.map((item) => {
    const thumb = item.kind === "image" && item.url
      ? '<img src="' + escapeHtml(attachmentURL(item.url)) + '" alt="">'
      : '<span class="draft-file-icon">' + escapeHtml(attachmentGlyph(item)) + '</span>';
    return '<div class="draft-attachment" data-id="' + escapeHtml(item.id || "") + '">' +
      thumb +
      '<div><div class="draft-file-name">' + escapeHtml(item.name || "附件") + '</div><div class="draft-file-meta">' + escapeHtml(formatBytes(item.size || 0)) + '</div></div>' +
      '<button class="draft-remove" type="button" aria-label="移除附件" data-remove-attachment="' + escapeHtml(item.id || "") + '">×</button>' +
      '</div>';
  }).join("");
  bar.querySelectorAll("[data-remove-attachment]").forEach((button) => {
    button.addEventListener("click", () => {
      state.draftAttachments = state.draftAttachments.filter((item) => item.id !== button.dataset.removeAttachment);
      renderDraftAttachments();
    });
  });
}

function attachmentGlyph(item) {
  const kind = String(item && item.kind || "");
  if (kind === "text") return "T";
  if (kind === "image") return "▧";
  return "□";
}

function attachmentURL(url) {
  const value = String(url || "");
  if (!value) return "";
  const sep = value.includes("?") ? "&" : "?";
  return value + sep + "token=" + encodeURIComponent(COMPANION_TOKEN);
}

function formatBytes(value) {
  const n = Number(value || 0);
  if (!Number.isFinite(n) || n <= 0) return "";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return Math.round(n / 1024) + " KB";
  return (n / 1024 / 1024).toFixed(n < 10 * 1024 * 1024 ? 1 : 0) + " MB";
}

async function addAttachmentFiles(files) {
  const list = Array.from(files || []).filter(Boolean);
  if (!list.length) return;
  const form = new FormData();
  list.forEach((file) => form.append("files", file, file.name || pastedFileName(file)));
  try {
    const data = await api("/api/upload", {method: "POST", body: form});
    const attachments = Array.isArray(data.attachments) ? data.attachments : [];
    state.draftAttachments = state.draftAttachments.concat(attachments).slice(-12);
    renderDraftAttachments();
  } catch (err) {
    addMessage("assistant", "附件添加失败：" + (err.message || "上传失败"), {error: true});
  }
}

function pastedFileName(file) {
  const type = String(file && file.type || "");
  const ext = type.includes("png") ? ".png" : type.includes("jpeg") ? ".jpg" : type.includes("webp") ? ".webp" : "";
  return "pasted-" + Date.now() + ext;
}

function resizeQuestionInput() {
  const input = el("questionInput");
  input.style.height = "auto";
  input.style.height = Math.min(input.scrollHeight, 132) + "px";
}

function addMessage(role, text, options) {
  const message = Object.assign({
    id: "m" + (++state.messageSeq),
    role: role,
    text: text || "",
    time: currentClock(),
    targets: [],
	    loading: false,
	    error: false,
	    tools: [],
	    events: []
	  }, options || {});
  state.messages.push(message);
  renderConversation();
  persistActiveConversation();
  return message.id;
}

function updateMessage(id, patch) {
  const message = state.messages.find((item) => item.id === id);
  if (!message) return;
  Object.assign(message, patch || {});
  if (!message.time) message.time = currentClock();
  renderConversation();
  persistActiveConversation();
}

function loadConversationHistory() {
  clearLegacyConversationHistory();
  state.chatSessions = sanitizeConversationHistorySessions(readJSON(localStorage.getItem(HISTORY_KEY), []));
  const activeID = localStorage.getItem(ACTIVE_HISTORY_KEY) || "";
  if (!state.chatSessions.length) {
    createConversation({});
  } else {
    activateConversation(activeID || state.chatSessions[0].id, {skipPersist: true});
    persistHistory();
  }
  renderHistoryList();
}

function sanitizeConversationHistorySessions(sessions) {
  return (Array.isArray(sessions) ? sessions : []).map((session) => {
    const next = Object.assign({}, session || {});
    next.messages = Array.isArray(next.messages) ? next.messages.map(sanitizeConversationMessage) : [];
    return next;
  });
}

function sanitizeConversationMessage(message) {
  const next = Object.assign({}, message || {});
  if (Array.isArray(next.events)) {
    next.events = next.events.filter((event) => {
      return !(event && event.type === "log" && isRoutineCPULogText(event.text));
    });
  }
  return next;
}

function isRoutineCPULogText(text) {
  const value = String(text || "").trim();
  if (!value) return false;
  const lower = value.toLowerCase();
  if (["fallback", "unavailable", "timeout", "failed", "error", "quota", "auth"].some((marker) => lower.includes(marker))) {
    return false;
  }
  return value.includes("CPU 已接管")
    || value.includes("CPU 启动")
    || value.includes("begin Codex output")
    || value.includes("begin Claude output")
    || value.includes("Codex headless home")
    || value.includes("Claude headless home")
    || value.includes("end Codex output")
    || value.includes("end Claude output");
}

function clearLegacyConversationHistory() {
  LEGACY_HISTORY_KEYS.forEach((key) => localStorage.removeItem(key));
}

function loadSidebarState() {
  state.sidebarCollapsed = localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "1";
  renderSidebarState();
}

function toggleSidebar() {
  state.sidebarCollapsed = !state.sidebarCollapsed;
  localStorage.setItem(SIDEBAR_COLLAPSED_KEY, state.sidebarCollapsed ? "1" : "0");
  renderSidebarState();
}

function renderSidebarState() {
  const collapsed = Boolean(state.sidebarCollapsed);
  el("appShell").classList.toggle("sidebar-collapsed", collapsed);
  el("sidebarToggleBtn").textContent = collapsed ? "›" : "‹";
  el("sidebarToggleBtn").title = collapsed ? "展开侧边栏" : "收起侧边栏";
  el("sidebarToggleBtn").setAttribute("aria-label", collapsed ? "展开侧边栏" : "收起侧边栏");
}

function readJSON(raw, fallback) {
  try {
    const parsed = JSON.parse(raw || "");
    return Array.isArray(parsed) ? parsed : fallback;
  } catch (_) {
    return fallback;
  }
}

function createConversation(options) {
  const now = Date.now();
  const item = {
    id: "c" + now.toString(36) + Math.random().toString(36).slice(2, 7),
    title: "新对话",
    createdAt: now,
    updatedAt: now,
    messages: []
  };
  state.chatSessions.unshift(item);
  activateConversation(item.id);
  return item;
}

function activateConversation(id, options) {
  const item = state.chatSessions.find((session) => session.id === id) || state.chatSessions[0];
  if (!item) return;
  state.activeChatID = item.id;
  state.messages = Array.isArray(item.messages) ? item.messages : [];
  state.messageSeq = maxMessageSeq(state.messages);
  state.mentions = [];
  closeMentionBox();
  renderTargets();
  renderConversation();
  renderHistoryList();
  if (!options || !options.skipPersist) persistHistory();
}

function deleteConversation(id) {
  const index = state.chatSessions.findIndex((session) => session.id === id);
  if (index < 0) return;
  state.chatSessions.splice(index, 1);
  if (!state.chatSessions.length) {
    createConversation({});
    return;
  }
  if (state.activeChatID === id) {
    const next = state.chatSessions[Math.max(0, index - 1)] || state.chatSessions[0];
    activateConversation(next.id);
  } else {
    persistHistory();
    renderHistoryList();
  }
}

function persistActiveConversation() {
  let item = state.chatSessions.find((session) => session.id === state.activeChatID);
  if (!item) {
    return;
  }
  item.messages = state.messages;
  item.updatedAt = Date.now();
  item.title = conversationTitle(item);
  persistHistory();
  renderHistoryList();
}

function persistHistory() {
  const compact = state.chatSessions
    .map((session) => Object.assign({}, session, {
      messages: Array.isArray(session.messages) ? session.messages.slice(-80) : []
    }))
    .sort((a, b) => Number(b.updatedAt || 0) - Number(a.updatedAt || 0))
    .slice(0, 60);
  state.chatSessions = compact;
  localStorage.setItem(HISTORY_KEY, JSON.stringify(compact));
  localStorage.setItem(ACTIVE_HISTORY_KEY, state.activeChatID || "");
}

function renderHistoryList() {
  const list = el("historyList");
  if (!list) return;
  if (!state.chatSessions.length) {
    list.innerHTML = '<div class="history-empty">暂无历史</div>';
    return;
  }
  list.innerHTML = state.chatSessions.map((session) => {
    const active = session.id === state.activeChatID ? " active" : "";
    return '<button class="history-item' + active + '" type="button" data-id="' + escapeHtml(session.id) + '">' +
      '<span class="history-main"><span class="history-title">' + escapeHtml(conversationTitle(session)) + '</span>' +
      '<span class="history-meta">' + escapeHtml(historyMeta(session)) + '</span></span>' +
      '<span class="history-delete" role="button" aria-label="删除" data-delete="' + escapeHtml(session.id) + '">×</span>' +
      '</button>';
  }).join("");
  list.querySelectorAll(".history-item").forEach((node) => {
    node.addEventListener("click", (event) => {
      const del = event.target.closest("[data-delete]");
      if (del) {
        event.stopPropagation();
        deleteConversation(del.dataset.delete);
        return;
      }
      activateConversation(node.dataset.id);
    });
  });
}

function conversationTitle(session) {
  const messages = Array.isArray(session.messages) ? session.messages : [];
  const firstUser = messages.find((message) => message && message.role === "user" && String(message.text || "").trim());
  return truncateText(firstUser ? firstUser.text : (session.title || "新对话"), 24);
}

function historyMeta(session) {
  const count = Array.isArray(session.messages) ? session.messages.length : 0;
  if (!count) return "空对话";
  return count + " 条 · " + relativeTime(session.updatedAt || session.createdAt);
}

function relativeTime(ts) {
  const value = Number(ts || 0);
  if (!value) return "刚刚";
  const diff = Math.max(0, Date.now() - value);
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return minutes + " 分钟前";
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return hours + " 小时前";
  const days = Math.floor(hours / 24);
  return days + " 天前";
}

function truncateText(text, limit) {
  const chars = Array.from(String(text || "").replace(/\s+/g, " ").trim());
  if (chars.length <= limit) return chars.join("") || "新对话";
  return chars.slice(0, limit).join("") + "…";
}

function maxMessageSeq(messages) {
  return (messages || []).reduce((max, message) => {
    const n = Number(String(message && message.id || "").replace(/^m/, ""));
    return Number.isFinite(n) ? Math.max(max, n) : max;
  }, 0);
}

function messageByID(id) {
  return state.messages.find((item) => item.id === id) || null;
}

function renderConversation() {
  const node = el("conversation");
  const stickToBottom = node.scrollHeight - node.scrollTop - node.clientHeight < 96;
  if (!state.messages.length) {
    node.innerHTML = '<div class="empty-state">问问微信</div>';
    return;
  }
  node.innerHTML = state.messages.map(renderMessage).join("");
  if (stickToBottom) {
    requestAnimationFrame(() => {
      node.scrollTop = node.scrollHeight;
    });
  }
}

function renderMessage(message) {
  const cls = ["message", message.role || "assistant"];
  if (message.loading) cls.push("loading");
  if (message.error) cls.push("error");
  if (message.notice) cls.push("notice");
  if (message.loading && !messageHasVisibleBody(message)) cls.push("thinking");
  const time = message.loading && !messageHasVisibleBody(message) ? "" : '<div class="message-time">' + escapeHtml(message.time || "") + '</div>';
  return '<article class="' + cls.join(" ") + '">' +
    '<div class="message-bubble">' + renderMessageBody(message) + '</div>' +
    time +
    '</article>';
}

function renderMessageBody(message) {
  if (message.loading && !messageHasVisibleBody(message)) {
    return renderThinkingLine();
  }
  const attachmentHTML = renderMessageAttachments(message.attachments);
  if (Array.isArray(message.events) && message.events.length) {
    const eventsHTML = renderMessageEvents(message);
    if (message.role === "assistant" && !message.loading && !message.error) {
      return eventsHTML + renderMarkdown(residualMessageText(message)) + attachmentHTML;
    }
    return eventsHTML + attachmentHTML;
  }
  const tools = renderToolCalls(message.tools);
  if (message.role === "assistant" && !message.loading && !message.error) {
    return tools + renderMarkdown(message.text) + attachmentHTML;
  }
  return tools + '<div class="plain-text">' + escapeHtml(message.text) + '</div>' + attachmentHTML;
}

function residualMessageText(message) {
  const text = String(message && message.text || "");
  const events = Array.isArray(message && message.events) ? message.events : [];
  const streamed = events
    .filter((event) => event && event.type === "text")
    .map((event) => String(event.text || ""))
    .join("");
  if (!streamed) return text;
  if (text.trim() === streamed.trim()) return "";
  if (text.startsWith(streamed)) return text.slice(streamed.length).trim();
  return text;
}

function messageHasVisibleBody(message) {
  if (String(message.text || "").trim()) return true;
  if (Array.isArray(message.attachments) && message.attachments.length) return true;
  if (Array.isArray(message.tools) && message.tools.length) return true;
  if (Array.isArray(message.events)) {
    return message.events.some((event) => {
      if (!event) return false;
      if (event.type === "tool") return true;
      return String(event.text || "").trim() !== "";
    });
  }
  return false;
}

function renderMessageAttachments(attachments) {
  if (!Array.isArray(attachments) || !attachments.length) return "";
  return '<div class="message-attachments">' + attachments.map((item) => {
    const media = item.kind === "image" && item.url
      ? '<img class="attachment-thumb" src="' + escapeHtml(attachmentURL(item.url)) + '" alt="">'
      : '<span class="attachment-icon">' + escapeHtml(attachmentGlyph(item)) + '</span>';
    return '<div class="message-attachment">' + media + '<div><div class="attachment-name">' + escapeHtml(item.name || "附件") + '</div><div class="attachment-meta">' + escapeHtml(formatBytes(item.size || 0) || item.mime || "") + '</div></div></div>';
  }).join("") + '</div>';
}

function renderThinkingLine() {
  return '<div class="thinking-line"><span>思考中</span><span class="thinking-dots" aria-hidden="true"><span></span><span></span><span></span></span></div>';
}

function renderMessageEvents(message) {
  return message.events.map((event) => {
    if (!event || event.type === "text") {
      const text = event && event.text ? event.text : "";
      return '<div class="event-text">' + renderMarkdown(text) + '</div>';
    }
    if (event.type === "log") {
      return '<div class="event-log">' + escapeHtml(event.text || "") + '</div>';
    }
    if (event.type === "tool") {
      return renderToolCall(event.call || {});
    }
    return "";
  }).join("");
}

function renderToolCalls(calls) {
  if (!Array.isArray(calls) || !calls.length) return "";
  return '<div class="tool-trace">' + calls.map(renderToolCall).join("") + '</div>';
}

function renderToolCall(call) {
  const status = String(call.status || "completed");
  const cls = "tool-call " + status;
  const title = String(call.label || call.tool || "wechat-cli");
  const duration = Number(call.duration_ms || 0);
  const durationText = duration > 0 ? duration + " ms" : "";
  const summary = String(call.summary || call.error || "");
  const openAttr = call.open || status === "running" ? " open" : "";
  const subtitle = toolCardSubtitle(call, summary);
  const statusText = toolCardStatus(status, durationText);
  return '<details class="' + escapeHtml(cls) + '"' + openAttr + '>' +
    '<summary><span class="tool-dot"></span><span class="tool-heading"><span class="tool-title">' + escapeHtml(title) + '</span>' +
    (subtitle ? '<span class="tool-subtitle">' + escapeHtml(subtitle) + '</span>' : "") +
    '</span><span class="tool-duration">' + escapeHtml(statusText) + '</span></summary>' +
    '<div class="tool-body">' +
    (summary ? '<div class="tool-summary">' + escapeHtml(summary) + '</div>' : "") +
    renderToolArgs(call.args || {}) +
    renderToolMeta(call.result || {}, call) +
    renderToolSamples(call.samples || []) +
    renderToolDetails(call) +
    '</div>' +
    '</details>';
}

function toolCardStatus(status, durationText) {
  if (status === "running") return "运行中";
  if (status === "error") return durationText ? "失败 · " + durationText : "失败";
  return durationText ? "完成 · " + durationText : "完成";
}

function toolCardSubtitle(call, summary) {
  const parts = [];
  const add = (value) => {
    const text = String(value || "").trim();
    if (!text) return;
    if (parts.includes(text)) return;
    parts.push(text);
  };
  const args = call && call.args && typeof call.args === "object" ? call.args : {};
  const result = call && call.result && typeof call.result === "object" ? call.result : {};
  const kind = toolCardKind(call);
  if (args.keyword) add("关键词：" + args.keyword);
  if (args.query) add("查询：" + args.query);
  if (result.display_name) {
    add("会话：" + result.display_name);
  } else if (args.chat === "selected") {
    add("会话：已选择");
  }
  if (args.limit && kind !== "contacts" && kind !== "resolve_chat") add("条数：" + args.limit);
  if (args.offset) add("偏移：" + args.offset);
  if (args.context_limit) add("上下文：" + args.context_limit);
  add(toolCardResultPhrase(kind, result, summary));
  if (Array.isArray(result.contacts) && result.contacts.length) add("候选：" + result.contacts.slice(0, 3).join("、"));
  if (Array.isArray(result.sessions) && result.sessions.length) add("候选：" + result.sessions.slice(0, 3).join("、"));
  const media = result.media || {};
  const mediaParts = [];
  if (media.images) mediaParts.push("图片 " + media.images);
  if (media.links) mediaParts.push("链接 " + media.links);
  if (media.files) mediaParts.push("文件 " + media.files);
  if (media.voices) mediaParts.push("语音 " + media.voices);
  if (media.videos) mediaParts.push("视频 " + media.videos);
  if (mediaParts.length) add(mediaParts.join(" / "));
  return parts.slice(0, 4).join(" · ");
}

function toolCardKind(call) {
  const tool = String(call && call.tool || "");
  const label = String(call && call.label || "");
  if (tool === "contacts" || label.includes("联系人")) return "contacts";
  if (tool === "resolve_chat" || label.includes("解析会话")) return "resolve_chat";
  if (tool === "sessions" || label.includes("最近会话")) return "sessions";
  if (tool === "chat_timeline" || tool === "messages" || label.includes("聊天记录") || label.includes("消息记录")) return "messages";
  if (tool === "message_context" || label.includes("上下文")) return "context";
  if (tool === "search" || tool === "search_with_context" || label.includes("搜索")) return "search";
  if (tool === "media_resources" || label.includes("媒体")) return "media";
  if (tool === "group_members" || label.includes("群成员")) return "members";
  return tool || "tool";
}

function toolCardResultPhrase(kind, result, summary) {
  const contactCount = Number(result.contact_count || (kind === "contacts" ? result.session_count : 0) || 0);
  const sessionCount = Number(result.session_count || 0);
  const messageCount = Number(result.message_count || 0);
  const hitCount = Number(result.hit_count || 0);
  const contextCount = Number(result.context_count || 0);
  const memberCount = Number(result.member_count || 0);
  const resourceCount = Number(result.resource_count || 0);
  if (kind === "contacts" && contactCount) return "返回 " + contactCount + " 个联系人";
  if (kind === "resolve_chat" && sessionCount) return "找到 " + sessionCount + " 个候选";
  if (kind === "sessions" && sessionCount) return "返回 " + sessionCount + " 个会话";
  if ((kind === "messages" || kind === "context") && messageCount) return "读取 " + messageCount + " 条消息";
  if (kind === "search" && hitCount) return contextCount ? "命中 " + hitCount + " 条，展开 " + contextCount + " 个上下文" : "命中 " + hitCount + " 条";
  if (kind === "media" && resourceCount) return "找到 " + resourceCount + " 个资源";
  if (kind === "members" && memberCount) return "返回 " + memberCount + " 个成员";
  return summary;
}

function renderToolArgs(args) {
  if (!args || typeof args !== "object") return "";
  const labels = {
    chat: "会话",
    query: "查询",
    keyword: "关键词",
    limit: "条数",
    offset: "偏移",
    order: "查询顺序",
    display_order: "展示顺序",
    type_filter: "类型",
    type: "类型",
    mode: "模式",
    search_mode: "搜索模式",
    context_limit: "上下文",
    before_count: "前文",
    after_count: "后文",
    local_id: "消息",
    server_id_str: "服务端ID",
    since_local_id: "游标",
    after: "开始",
    before: "结束",
    from_me: "只看自己",
    include_images: "图片",
    include_media_paths: "媒体路径"
  };
  const order = ["chat", "query", "keyword", "limit", "offset", "context_limit", "before_count", "after_count", "type_filter", "type", "mode", "search_mode", "display_order", "order", "local_id", "server_id_str", "since_local_id", "after", "before", "from_me", "include_images", "include_media_paths"];
  const pills = [];
  order.forEach((key) => {
    if (args[key] === undefined || args[key] === null || args[key] === "") return;
    let value = args[key];
    if (key === "chat" && value === "selected") value = "已选择";
    if (typeof value === "boolean") value = value ? "是" : "否";
    pills.push((labels[key] || key) + "：" + String(value));
  });
  if (!pills.length) return "";
  return '<div class="tool-meta tool-args">' + pills.map((pill) => '<span class="tool-pill">' + escapeHtml(pill) + '</span>').join("") + '</div>';
}

function renderToolMeta(result, call) {
  const pills = [];
  const kind = toolCardKind(call || {});
  if (result.display_name) pills.push("会话：" + result.display_name);
  if (result.contact_count) pills.push("联系人 " + result.contact_count);
  if (result.session_count) pills.push((kind === "contacts" ? "联系人 " : "会话 ") + result.session_count);
  if (result.message_count) pills.push("消息 " + result.message_count);
  if (result.hit_count) pills.push("命中 " + result.hit_count);
  if (result.context_count) pills.push("上下文 " + result.context_count);
  if (result.member_count) pills.push("成员 " + result.member_count);
  if (result.resource_count) pills.push("资源 " + result.resource_count);
  if (result.event_count) pills.push("事件 " + result.event_count);
  if (result.keyword) pills.push("关键词：" + result.keyword);
  if (result.query) pills.push("查询：" + result.query);
  if (result.oldest_time && result.newest_time) pills.push(result.oldest_time + " → " + result.newest_time);
  const media = result.media || {};
  if (media.images) pills.push("图片 " + media.images);
  if (media.links) pills.push("链接 " + media.links);
  if (media.files) pills.push("文件 " + media.files);
  if (media.voices) pills.push("语音 " + media.voices);
  if (media.videos) pills.push("视频 " + media.videos);
  if (Array.isArray(result.sessions) && result.sessions.length) {
    pills.push("候选：" + result.sessions.slice(0, 3).join("、"));
  }
  if (Array.isArray(result.contacts) && result.contacts.length) {
    pills.push("联系人：" + result.contacts.slice(0, 3).join("、"));
  }
  if (!pills.length) return "";
  return '<div class="tool-meta">' + pills.map((pill) => '<span class="tool-pill">' + escapeHtml(pill) + '</span>').join("") + '</div>';
}

function renderToolSamples(samples) {
  if (!Array.isArray(samples) || !samples.length) return "";
  return '<div class="tool-samples">' + samples.map((sample) => {
    const media = Array.isArray(sample.media) && sample.media.length
      ? ' <span class="tool-media">' + escapeHtml(sample.media.join(" / ")) + '</span>'
      : "";
    const sender = sample.sender ? sample.sender + "：" : "";
    const time = sample.time ? sample.time + " " : "";
    const text = sample.text || sample.kind || "";
    return '<div class="tool-sample">' + escapeHtml(time + sender + text) + media + '</div>';
  }).join("") + '</div>';
}

function renderToolDetails(call) {
  const detail = {
    id: call.id || "",
    tool: call.tool || "",
    command: call.command || "",
    display_command: call.display_command || "",
    status: call.status || "",
    args: call.args || {},
    result: call.result || {},
    samples: Array.isArray(call.samples) ? call.samples : [],
    error: call.error || ""
  };
  return '<pre class="tool-json"><code>' + escapeHtml(JSON.stringify(detail, null, 2)) + '</code></pre>';
}

function appendAssistantDelta(messageID, text) {
  if (!text) return;
  const message = messageByID(messageID);
  if (!message) return;
  if (!Array.isArray(message.events)) message.events = [];
  const last = message.events[message.events.length - 1];
  if (last && last.type === "text") {
    last.text += text;
  } else {
    message.events.push({type: "text", text: text});
  }
  message.text = (message.text || "") + text;
  message.loading = true;
  message.time = currentClock();
  renderConversation();
}

function appendProcessEvent(messageID, text) {
  const value = String(text || "").trim();
  if (!value) return;
  const message = messageByID(messageID);
  if (!message) return;
  if (!Array.isArray(message.events)) message.events = [];
  const last = message.events[message.events.length - 1];
  if (last && last.type === "log" && last.text === value) {
    return;
  }
  message.events.push({type: "log", text: value});
  message.loading = true;
  message.time = currentClock();
  renderConversation();
}

function appendToolStart(messageID, data) {
  const message = messageByID(messageID);
  if (!message) return;
  if (!Array.isArray(message.events)) message.events = [];
  const call = normalizeStreamTool(data, "running");
  call.open = true;
  message.events.push({type: "tool", call: call});
  message.tools = message.events.filter((event) => event.type === "tool").map((event) => event.call);
  message.loading = true;
  message.time = currentClock();
  renderConversation();
}

function appendCompletedTools(messageID, calls) {
  const message = messageByID(messageID);
  if (!message || !Array.isArray(calls) || !calls.length) return;
  if (!Array.isArray(message.events)) message.events = [];
  calls.forEach((call) => {
    const next = Object.assign({}, call || {}, {open: false});
    message.events.push({type: "tool", call: next});
  });
  message.tools = message.events.filter((event) => event.type === "tool").map((event) => event.call);
  message.time = currentClock();
  renderConversation();
}

function applyToolResult(messageID, data) {
  const message = messageByID(messageID);
  if (!message) return;
  if (!Array.isArray(message.events)) message.events = [];
  const id = String(data.id || data.trace_id || "");
  let target = null;
  for (let i = message.events.length - 1; i >= 0; i--) {
    const event = message.events[i];
    if (!event || event.type !== "tool") continue;
    const call = event.call || {};
    if ((id && call.id === id) || (!id && call.status === "running")) {
      target = call;
      break;
    }
  }
  if (!target) {
    target = normalizeStreamTool(data, String(data.status || "completed"));
    message.events.push({type: "tool", call: target});
  }
  Object.assign(target, normalizeStreamTool(data, String(data.status || "completed")));
  target.open = false;
  message.tools = message.events.filter((event) => event.type === "tool").map((event) => event.call);
  message.time = currentClock();
  renderConversation();
}

function normalizeStreamTool(data, fallbackStatus) {
  const result = data && data.result && typeof data.result === "object" ? data.result : {};
  const args = data && data.args !== undefined ? data.args : {};
  return {
    id: String(data && (data.id || data.trace_id) || ""),
    tool: String(data && (data.tool || data.name || data.command) || "tool"),
    command: String(data && (data.command || data.tool || data.name) || "tool"),
    display_command: String(data && (data.display_command || data.command || data.tool || data.name) || "tool"),
    status: String(data && data.status || fallbackStatus || "completed"),
    label: String(data && data.label || data && data.tool || data && data.name || "工具调用"),
    summary: String(data && data.summary || data && data.error || result.text || ""),
    args: args,
    result: result,
    samples: Array.isArray(data && data.samples) ? data.samples : [],
    error: String(data && data.error || ""),
    duration_ms: Number(data && data.duration_ms || 0)
  };
}

function renderTargetChips(targets) {
  return targets.slice(0, 6).map((s) => {
    return '<span class="chip">' + escapeHtml(sessionName(s)) + '</span>';
  }).join("");
}

const MD_CODE = String.fromCharCode(96);

function renderMarkdown(source) {
  const text = String(source == null ? "" : source).replace(/\r\n?/g, "\n").trim();
  if (!text) return "";
  const lines = text.split("\n");
  const blocks = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    const trimmed = line.trim();
    if (!trimmed) {
      i++;
      continue;
    }
    const fence = markdownFence(trimmed);
    if (fence) {
      const code = [];
      i++;
      while (i < lines.length && !lines[i].trim().startsWith(fence.marker)) {
        code.push(lines[i]);
        i++;
      }
      if (i < lines.length) i++;
      const lang = fence.lang ? ' data-lang="' + escapeHtml(fence.lang) + '"' : "";
      blocks.push('<pre><code' + lang + '>' + escapeHtml(code.join("\n")) + '</code></pre>');
      continue;
    }
    if (isTableStart(lines, i)) {
      const table = renderMarkdownTable(lines, i);
      blocks.push(table.html);
      i = table.next;
      continue;
    }
    const heading = /^(#{1,6})\s+(.+)$/.exec(trimmed);
    if (heading) {
      const level = heading[1].length;
      blocks.push('<h' + level + '>' + renderInline(heading[2]) + '</h' + level + '>');
      i++;
      continue;
    }
    if (/^(-{3,}|\*{3,}|_{3,})$/.test(trimmed)) {
      blocks.push("<hr>");
      i++;
      continue;
    }
    if (/^>\s?/.test(trimmed)) {
      const quote = [];
      while (i < lines.length && /^>\s?/.test(lines[i].trim())) {
        quote.push(lines[i].trim().replace(/^>\s?/, ""));
        i++;
      }
      blocks.push('<blockquote>' + renderMarkdown(quote.join("\n")) + '</blockquote>');
      continue;
    }
    if (listItemMatch(line)) {
      const list = renderMarkdownList(lines, i);
      blocks.push(list.html);
      i = list.next;
      continue;
    }
    const paragraph = [];
    while (i < lines.length && lines[i].trim() && !isMarkdownBlockStart(lines[i], lines[i + 1] || "")) {
      paragraph.push(lines[i].trim());
      i++;
    }
    if (paragraph.length) {
      blocks.push("<p>" + renderInline(paragraph.join("\n")) + "</p>");
      continue;
    }
    blocks.push("<p>" + renderInline(trimmed) + "</p>");
    i++;
  }
  return '<div class="markdown">' + blocks.join("") + '</div>';
}

function markdownFence(line) {
  const markerA = MD_CODE + MD_CODE + MD_CODE;
  const markers = [markerA, "~~~"];
  for (let i = 0; i < markers.length; i++) {
    if (line.startsWith(markers[i])) {
      const lang = line.slice(markers[i].length).trim().split(/\s+/)[0] || "";
      return { marker: markers[i], lang: lang };
    }
  }
  return null;
}

function isMarkdownBlockStart(line, nextLine) {
  const trimmed = String(line || "").trim();
  if (!trimmed) return true;
  if (markdownFence(trimmed)) return true;
  if (/^(#{1,6})\s+/.test(trimmed)) return true;
  if (/^>\s?/.test(trimmed)) return true;
  if (/^(-{3,}|\*{3,}|_{3,})$/.test(trimmed)) return true;
  if (listItemMatch(line)) return true;
  if (isTableStart([line, nextLine || ""], 0)) return true;
  return false;
}

function listItemMatch(line) {
  const unordered = /^(\s*)[-*+]\s+(.+)$/.exec(String(line || ""));
  if (unordered) return { type: "ul", text: unordered[2] };
  const ordered = /^(\s*)\d+[.)]\s+(.+)$/.exec(String(line || ""));
  if (ordered) return { type: "ol", text: ordered[2] };
  return null;
}

function renderMarkdownList(lines, start) {
  const first = listItemMatch(lines[start]);
  const tag = first.type;
  const parts = ["<" + tag + ">"];
  let i = start;
  while (i < lines.length) {
    const item = listItemMatch(lines[i]);
    if (!item || item.type !== tag) break;
    const body = [item.text];
    i++;
    while (i < lines.length && lines[i].trim() && !listItemMatch(lines[i]) && !isMarkdownBlockStart(lines[i], lines[i + 1] || "")) {
      body.push(lines[i].trim());
      i++;
    }
    parts.push("<li>" + renderInline(body.join("\n")) + "</li>");
  }
  parts.push("</" + tag + ">");
  return { html: parts.join(""), next: i };
}

function isTableStart(lines, index) {
  const row = String(lines[index] || "");
  const divider = String(lines[index + 1] || "");
  return row.includes("|") && isTableDivider(divider);
}

function isTableDivider(line) {
  return /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$/.test(String(line || ""));
}

function splitTableRow(line) {
  let row = String(line || "").trim();
  row = row.replace(/^\|/, "").replace(/\|$/, "");
  return row.split("|").map((cell) => cell.trim());
}

function tableAligns(line) {
  return splitTableRow(line).map((cell) => {
    const left = cell.startsWith(":");
    const right = cell.endsWith(":");
    if (left && right) return "center";
    if (right) return "right";
    return "";
  });
}

function tableCell(tag, text, align) {
  const attr = align ? ' style="text-align:' + align + '"' : "";
  return "<" + tag + attr + ">" + renderInline(text) + "</" + tag + ">";
}

function renderMarkdownTable(lines, start) {
  const headers = splitTableRow(lines[start]);
  const aligns = tableAligns(lines[start + 1]);
  const width = headers.length;
  const out = ['<table><thead><tr>'];
  headers.forEach((cell, index) => out.push(tableCell("th", cell, aligns[index] || "")));
  out.push("</tr></thead><tbody>");
  let i = start + 2;
  while (i < lines.length && lines[i].trim() && lines[i].includes("|")) {
    const cells = splitTableRow(lines[i]);
    out.push("<tr>");
    for (let c = 0; c < width; c++) {
      out.push(tableCell("td", cells[c] || "", aligns[c] || ""));
    }
    out.push("</tr>");
    i++;
  }
  out.push("</tbody></table>");
  return { html: out.join(""), next: i };
}

function renderInline(source) {
  let text = String(source == null ? "" : source);
  const codeSlots = [];
  text = stashInlineCode(text, codeSlots);
  const linkSlots = [];
  text = text.replace(/\[([^\]\n]+)\]\(([^)\s]+)\)/g, (match, label, href) => {
    const safeHref = sanitizeMarkdownURL(href);
    if (!safeHref) return match;
    const token = "\u0000LINK" + linkSlots.length + "\u0000";
    linkSlots.push({ label: label, href: safeHref });
    return token;
  });
  let html = escapeHtml(text);
  html = html.replace(/~~([^~]+)~~/g, "<del>$1</del>");
  html = html.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  html = html.replace(/__([^_]+)__/g, "<strong>$1</strong>");
  html = html.replace(/(^|[^*])\*([^*\n]+)\*/g, "$1<em>$2</em>");
  html = html.replace(/(^|[^_])_([^_\n]+)_/g, "$1<em>$2</em>");
  html = html.replace(/\n/g, "<br>");
  linkSlots.forEach((link, index) => {
    const token = "\u0000LINK" + index + "\u0000";
    html = html.split(token).join('<a href="' + escapeHtml(link.href) + '" target="_blank" rel="noreferrer">' + escapeHtml(link.label) + '</a>');
  });
  codeSlots.forEach((code, index) => {
    const token = "\u0000CODE" + index + "\u0000";
    html = html.split(token).join("<code>" + escapeHtml(code) + "</code>");
  });
  return html;
}

function stashInlineCode(text, slots) {
  let out = "";
  let i = 0;
  while (i < text.length) {
    if (text[i] !== MD_CODE) {
      out += text[i];
      i++;
      continue;
    }
    const end = text.indexOf(MD_CODE, i + 1);
    if (end < 0) {
      out += text[i];
      i++;
      continue;
    }
    const token = "\u0000CODE" + slots.length + "\u0000";
    slots.push(text.slice(i + 1, end));
    out += token;
    i = end + 1;
  }
  return out;
}

function sanitizeMarkdownURL(href) {
  const value = String(href || "").trim();
  if (/^(https?:|mailto:)/i.test(value)) return value;
  return "";
}

async function loadStatus() {
  try {
    const data = await api("/api/status");
    if (data.wechat_error) {
      setConnected(false, "异常");
    } else if (!state.sessionError) {
      setConnected(true, connectionLabel());
    }
    if (data.cli && data.cli.binary) {
      el("settingsState").textContent = data.cli.binary;
    }
  } catch (err) {
    setConnected(false, "异常");
  }
}

async function loadSessions() {
  try {
    const data = await api("/api/sessions?limit=80");
    state.sessions = data.sessions || [];
    state.sessionError = false;
    setConnected(true, connectionLabel());
    renderTargets();
  } catch (err) {
    state.sessionError = true;
    setConnected(false, "异常");
    if (!state.messages.length) {
      addMessage("assistant", "会话读取失败：" + err.message, {error: true});
    }
  }
}

async function runAsk() {
  if (state.asking) return;
  const input = el("questionInput");
  const question = input.value.trim();
  const attachments = Array.isArray(state.draftAttachments) ? state.draftAttachments.slice() : [];
  if (!question && !attachments.length) {
    input.focus();
    return;
  }
  if (!state.activeChatID) {
    createConversation({});
  }
  const targets = activeTargetSessions();
  const chats = targets.map((s) => s.username).filter(Boolean);
  const history = companionRequestHistory();
  addMessage("user", question || "（附件）", {targets: targets, attachments: attachments});
	  const pendingID = addMessage("assistant", "", {loading: true, events: []});
  input.value = "";
  resizeQuestionInput();
  state.draftAttachments = [];
  state.mentions = [];
  closeMentionBox();
  renderTargets();
  renderDraftAttachments();
  input.focus();
  setAsking(true);
  const askPayload = {
    chat: chats[0] || "",
    chats: chats,
    mode: "custom",
    question: question,
    attachments: attachments,
    history: history
  };
  try {
	    if (window.ReadableStream) {
	      let finalData = null;
	      await apiStream("/api/ask-stream", askPayload, (eventName, eventData) => {
        if (eventName === "status" || eventName === "cpu_log") {
          appendProcessEvent(pendingID, eventData.message || eventData.text || "");
        }
	        if (eventName === "tool_calls") {
	          appendCompletedTools(pendingID, Array.isArray(eventData.tool_calls) ? eventData.tool_calls : []);
	        }
	        if (eventName === "assistant_delta") {
	          appendAssistantDelta(pendingID, String(eventData.text || ""));
	        }
	        if (eventName === "tool_start") {
	          appendToolStart(pendingID, eventData || {});
	        }
	        if (eventName === "tool_result") {
	          applyToolResult(pendingID, eventData || {});
	        }
        if (eventName === "answer") {
	          finalData = eventData || {};
        }
        if (eventName === "error") {
          throw new Error(eventData.message || "请求失败");
        }
      });
      finishAskMessage(pendingID, finalData || {});
    } else {
      const data = await api("/api/ask", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify(askPayload)
      });
      finishAskMessage(pendingID, data);
    }
  } catch (err) {
    updateMessage(pendingID, {text: err.message, loading: false, error: true, time: currentClock()});
  } finally {
    setAsking(false);
  }
}

function companionRequestHistory() {
  return state.messages.slice(-24).map((message) => {
    const role = message && message.role === "user" ? "user" : "assistant";
    const targets = Array.isArray(message && message.targets)
      ? message.targets.map((target) => sessionName(target)).filter(Boolean).slice(0, 8)
      : [];
    const attachments = Array.isArray(message && message.attachments)
      ? message.attachments.map(companionRequestAttachment).filter(Boolean).slice(0, 12)
      : [];
    const text = truncateRequestText(message && message.text || "", 3000);
    return {role: role, text: text, targets: targets, attachments: attachments};
  }).filter((item) => item.text || item.targets.length || item.attachments.length);
}

function companionRequestAttachment(item) {
  if (!item) return null;
  return {
    id: String(item.id || ""),
    kind: String(item.kind || ""),
    name: String(item.name || ""),
    mime: String(item.mime || ""),
    size: Number(item.size || 0),
    path: String(item.path || ""),
    url: String(item.url || ""),
    text_preview: truncateRequestText(item.text_preview || "", 1200)
  };
}

function truncateRequestText(text, limit) {
  const chars = Array.from(String(text || "").trim());
  if (chars.length <= limit) return chars.join("");
  return chars.slice(0, limit).join("");
}

function finishAskMessage(pendingID, data) {
	const current = messageByID(pendingID);
	const streamedText = current ? String(current.text || "") : "";
	const nextText = streamedText || data.answer || "";
	const nextEvents = current && Array.isArray(current.events) && current.events.length
	  ? collapseRunningToolEvents(current.events)
	  : (Array.isArray(data.tool_calls) ? data.tool_calls.map((call) => ({type: "tool", call: Object.assign({}, call || {}, {open: false})})) : []);
  updateMessage(pendingID, {
    text: nextText,
    tools: current && Array.isArray(current.tools) && current.tools.length ? current.tools : (Array.isArray(data.tool_calls) ? data.tool_calls : []),
    events: nextEvents,
    loading: false,
    error: false,
    notice: false,
    time: currentClock()
  });
}

function collapseRunningToolEvents(events) {
  return events.map((event) => {
    if (!event || event.type !== "tool" || !event.call) return event;
    return Object.assign({}, event, {call: Object.assign({}, event.call, {open: false})});
  });
}

function renderTargets() {
  const targets = activeTargetSessions();
  if (!targets.length) {
    el("targetBar").classList.remove("open");
    el("targetBar").innerHTML = "";
    return;
  }
  el("targetBar").classList.add("open");
  el("targetBar").innerHTML = '<span class="target-label">目标</span><div class="chips">' + renderTargetChips(targets) + '</div>';
}

function activeTargetSessions() {
  syncMentionTargets();
  const byUsername = {};
  const out = [];
  function add(session) {
    if (!session || !session.username || byUsername[session.username]) return;
    byUsername[session.username] = true;
    out.push(session);
  }
  state.mentions.forEach(add);
  return out;
}

function syncMentionTargets() {
  const input = el("questionInput");
  if (!input) return;
  const text = input.value || "";
  state.mentions = state.mentions.filter((session) => text.includes("@" + sessionName(session)));
}

function sessionName(session) {
  return session ? (session.display_name || session.username || "(unknown)") : "";
}

function normalizeMentionText(text) {
  return String(text || "").toLowerCase().replace(/\s+/g, "");
}

const PINYIN_INITIAL_OVERRIDES = {
  "郑": "z",
  "宇": "y",
  "格": "g"
};

const PINYIN_INITIAL_BOUNDS = [
  ["a", "阿"], ["b", "八"], ["c", "嚓"], ["d", "哒"], ["e", "饿"],
  ["f", "发"], ["g", "旮"], ["h", "哈"], ["j", "击"], ["k", "咔"],
  ["l", "垃"], ["m", "妈"], ["n", "拿"], ["o", "哦"], ["p", "啪"],
  ["q", "七"], ["r", "然"], ["s", "撒"], ["t", "他"], ["w", "挖"],
  ["x", "夕"], ["y", "丫"], ["z", "匝"]
];

const PINYIN_COLLATOR = typeof Intl !== "undefined" && Intl.Collator
  ? new Intl.Collator("zh-Hans-u-co-pinyin")
  : null;

function isCJKChar(ch) {
  return /[\u3400-\u9fff]/.test(ch);
}

function pinyinInitial(ch) {
  if (!isCJKChar(ch)) return normalizeMentionText(ch);
  if (PINYIN_INITIAL_OVERRIDES[ch]) return PINYIN_INITIAL_OVERRIDES[ch];
  if (!PINYIN_COLLATOR) return "";
  let initial = "";
  PINYIN_INITIAL_BOUNDS.forEach(([letter, sample]) => {
    if (PINYIN_COLLATOR.compare(ch, sample) >= 0) initial = letter;
  });
  return initial;
}

function pinyinInitials(text) {
  return Array.from(String(text || "")).map(pinyinInitial).join("");
}

function mentionRange(value, caret) {
  const prefix = value.slice(0, caret);
  const at = prefix.lastIndexOf("@");
  if (at < 0) return null;
  const query = prefix.slice(at + 1);
  if (/[\n\r\t ，,。；;：:]/.test(query)) return null;
  return { start: at, end: caret, query: query };
}

function scoreSession(session, rawQuery, index) {
  const query = normalizeMentionText(rawQuery);
  const name = normalizeMentionText(sessionName(session));
  const username = normalizeMentionText(session.username || "");
  const summary = normalizeMentionText(session.summary || "");
  const nameInitials = normalizeMentionText(pinyinInitials(sessionName(session)));
  const summaryInitials = normalizeMentionText(pinyinInitials(session.summary || ""));
  const recency = Math.max(0, 80 - index) / 1000;
  if (!query) return 20 + recency;
  let score = 0;
  if (name === query) score = 100;
  else if (nameInitials === query) score = 92;
  else if (name.startsWith(query)) score = 80;
  else if (nameInitials.startsWith(query)) score = 74;
  else if (name.includes(query)) score = 60;
  else if (nameInitials.includes(query)) score = 50;
  else if (username.startsWith(query)) score = 42;
  else if (username.includes(query)) score = 34;
  else if (summaryInitials.includes(query)) score = 24;
  else if (summary.includes(query)) score = 18;
  return score ? score + recency : 0;
}

function mentionMatches(query) {
  return state.sessions
    .map((session, index) => ({ session, index, score: scoreSession(session, query, index) }))
    .filter((row) => row.score > 0)
    .sort((a, b) => b.score - a.score || a.index - b.index)
    .slice(0, 7)
    .map((row) => row.session);
}

function updateMentionBox() {
  const input = el("questionInput");
  const range = mentionRange(input.value, input.selectionStart || 0);
  state.mentionRange = range;
  if (!range) {
    closeMentionBox();
    return;
  }
  state.mentionResults = mentionMatches(range.query);
  state.mentionActive = Math.min(state.mentionActive, Math.max(0, state.mentionResults.length - 1));
  renderMentionBox();
}

function renderMentionBox() {
  const box = el("mentionBox");
  if (!state.mentionRange || !state.mentionResults.length) {
    closeMentionBox();
    return;
  }
  box.classList.add("open");
  box.innerHTML = state.mentionResults.map((session, index) => {
    const active = index === state.mentionActive ? " active" : "";
    return '<div class="mention-item' + active + '" data-index="' + index + '">' +
      '<div class="mention-name">' + escapeHtml(sessionName(session)) + '</div>' +
      '<div class="mention-summary">' + escapeHtml(session.summary || session.username || "") + '</div>' +
      '</div>';
  }).join("");
  document.querySelectorAll(".mention-item").forEach((node) => {
    node.addEventListener("mousedown", (event) => {
      event.preventDefault();
      insertMention(Number(node.dataset.index));
    });
  });
}

function closeMentionBox() {
  el("mentionBox").classList.remove("open");
  el("mentionBox").innerHTML = "";
  state.mentionRange = null;
  state.mentionResults = [];
  state.mentionActive = 0;
}

function insertMention(index) {
  const session = state.mentionResults[index];
  const range = state.mentionRange;
  if (!session || !range) return;
  const input = el("questionInput");
  const name = sessionName(session);
  const next = input.value.slice(0, range.start) + "@" + name + " " + input.value.slice(range.end);
  const caret = range.start + name.length + 2;
  input.value = next;
  input.focus();
  input.setSelectionRange(caret, caret);
  resizeQuestionInput();
  state.mentions = state.mentions.filter((item) => item.username !== session.username);
  state.mentions.push(session);
  closeMentionBox();
  renderTargets();
}

function openSheet(id) {
  closeAllSheets();
  el(id).classList.add("open");
}

function closeAllSheets() {
  document.querySelectorAll(".sheet").forEach((sheet) => sheet.classList.remove("open"));
  closeMentionBox();
}

function currentClock() {
  const d = new Date();
  return String(d.getHours()).padStart(2, "0") + ":" + String(d.getMinutes()).padStart(2, "0");
}

function escapeHtml(s) {
  return String(s == null ? "" : s)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

el("askForm").addEventListener("submit", (event) => {
  event.preventDefault();
  runAsk();
});
el("questionInput").addEventListener("input", () => {
  resizeQuestionInput();
  updateMentionBox();
  renderTargets();
});
el("questionInput").addEventListener("click", updateMentionBox);
el("questionInput").addEventListener("paste", (event) => {
  const files = [];
  const items = event.clipboardData && event.clipboardData.items ? Array.from(event.clipboardData.items) : [];
  items.forEach((item) => {
    if (item.kind === "file") {
      const file = item.getAsFile();
      if (file) files.push(file);
    }
  });
  if (files.length) {
    event.preventDefault();
    addAttachmentFiles(files);
  }
});
el("questionInput").addEventListener("keydown", (event) => {
  if (state.mentionRange && state.mentionResults.length) {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      state.mentionActive = (state.mentionActive + 1) % state.mentionResults.length;
      renderMentionBox();
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      state.mentionActive = (state.mentionActive + state.mentionResults.length - 1) % state.mentionResults.length;
      renderMentionBox();
      return;
    }
    if (event.key === "Tab" || event.key === "Enter") {
      event.preventDefault();
      insertMention(state.mentionActive);
      resizeQuestionInput();
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      closeMentionBox();
      return;
    }
  }
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
    event.preventDefault();
    runAsk();
  }
});
el("attachBtn").addEventListener("click", () => el("attachmentInput").click());
el("attachmentInput").addEventListener("change", () => {
  addAttachmentFiles(el("attachmentInput").files);
  el("attachmentInput").value = "";
});
el("askForm").addEventListener("dragover", (event) => {
  if (event.dataTransfer && event.dataTransfer.types && Array.from(event.dataTransfer.types).includes("Files")) {
    event.preventDefault();
    el("askForm").classList.add("dragging");
  }
});
el("askForm").addEventListener("dragleave", () => {
  el("askForm").classList.remove("dragging");
});
el("askForm").addEventListener("drop", (event) => {
  const files = event.dataTransfer ? event.dataTransfer.files : null;
  if (files && files.length) {
    event.preventDefault();
    el("askForm").classList.remove("dragging");
    addAttachmentFiles(files);
  }
});
el("menuBtn").addEventListener("click", () => openSheet("menuSheet"));
el("sidebarToggleBtn").addEventListener("click", toggleSidebar);
el("newChatBtn").addEventListener("click", () => {
  if (state.asking) return;
  createConversation({});
  el("questionInput").focus();
});
el("menuSettingsBtn").addEventListener("click", () => {
  closeAllSheets();
  openSheet("settingsSheet");
});
document.querySelectorAll("[data-close]").forEach((button) => {
  button.addEventListener("click", () => el(button.dataset.close).classList.remove("open"));
});
document.addEventListener("keydown", (event) => {
  if (event.key === "Escape") closeAllSheets();
});

loadSidebarState();
loadConversationHistory();
renderConversation();
renderHistoryList();
renderTargets();
updateAskAvailability();
resizeQuestionInput();
loadStatus();
loadSessions();
</script>
</body>
</html>
`
