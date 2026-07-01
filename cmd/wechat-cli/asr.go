package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type asrSetupOptions struct {
	DryRun            bool
	Force             bool
	SkipModelDownload bool
	Python            string
	Venv              string
	Model             string
	Language          string
	Device            string
	ComputeType       string
}

func runASRCLI(args []string, opts cliOptions) {
	subcommand := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "--") {
		subcommand = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(args[0])), "-", "_")
		args = args[1:]
	}
	switch subcommand {
	case "", "status", "doctor":
		writeCLISuccess("asr", "asr status", asrStatusData(), opts)
	case "setup", "install":
		setupOpts, err := parseASRSetupOptions(args)
		if err != nil {
			exitCLIError(opts, 2, "invalid_argument", err.Error(), "asr", "asr setup")
		}
		data, err := asrSetup(setupOpts)
		if err != nil {
			exitCLIError(opts, 1, "asr_setup_failed", err.Error(), "asr", "asr setup")
		}
		writeCLISuccess("asr", "asr setup", data, opts)
	default:
		exitCLIError(opts, 2, "unknown_argument", fmt.Sprintf("unknown asr subcommand %q", subcommand), "asr", "asr")
	}
}

func parseASRSetupOptions(args []string) (asrSetupOptions, error) {
	out := asrSetupOptions{
		Model:       defaultFasterWhisperModel,
		Language:    "zh",
		Device:      "cpu",
		ComputeType: "int8",
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			return out, fmt.Errorf("unexpected positional argument %q", a)
		}
		raw := strings.TrimPrefix(a, "--")
		key, val, hasValue := strings.Cut(raw, "=")
		key = strings.ReplaceAll(key, "-", "_")
		boolValue := func(defaultValue bool) (bool, error) {
			if hasValue {
				return parseBoolValue(key, val)
			}
			if i+1 < len(args) && isBoolLiteral(args[i+1]) {
				i++
				return parseBoolValue(key, args[i])
			}
			return defaultValue, nil
		}
		stringValue := func() (string, error) {
			if hasValue {
				return strings.TrimSpace(val), nil
			}
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", fmt.Errorf("--%s requires a value", strings.ReplaceAll(key, "_", "-"))
			}
			i++
			return strings.TrimSpace(args[i]), nil
		}
		switch key {
		case "dry_run":
			v, err := boolValue(true)
			if err != nil {
				return out, err
			}
			out.DryRun = v
		case "force":
			v, err := boolValue(true)
			if err != nil {
				return out, err
			}
			out.Force = v
		case "skip_model_download", "no_model_download":
			v, err := boolValue(true)
			if err != nil {
				return out, err
			}
			out.SkipModelDownload = v
		case "python":
			v, err := stringValue()
			if err != nil {
				return out, err
			}
			out.Python = v
		case "venv":
			v, err := stringValue()
			if err != nil {
				return out, err
			}
			out.Venv = v
		case "model":
			v, err := stringValue()
			if err != nil {
				return out, err
			}
			if v != "" {
				out.Model = v
			}
		case "language", "lang":
			v, err := stringValue()
			if err != nil {
				return out, err
			}
			if v != "" {
				out.Language = v
			}
		case "device":
			v, err := stringValue()
			if err != nil {
				return out, err
			}
			if v != "" {
				out.Device = v
			}
		case "compute_type", "compute":
			v, err := stringValue()
			if err != nil {
				return out, err
			}
			if v != "" {
				out.ComputeType = v
			}
		default:
			return out, fmt.Errorf("unknown argument --%s", strings.ReplaceAll(key, "_", "-"))
		}
	}
	return out, nil
}

func asrStatusData() map[string]any {
	venv, _ := defaultASRVenvDir()
	venvPython := asrVenvPythonPath(venv)
	customCmd := envFirst("WECHAT_CLI_VOICE_TRANSCRIBE_CMD", "WX_MCP_VOICE_TRANSCRIBE_CMD")
	fasterPython := findConfiguredPythonWithModule("faster_whisper")
	whisperCLI := commandPath(firstNonEmpty(envFirst("WECHAT_CLI_WHISPER_CLI", "WX_MCP_WHISPER_CLI"), "whisper-cli"))
	whisperModel := findWhisperModel()
	silkDecoder := findSILKDecoder()
	pysilkPython := findConfiguredPythonWithModule("pysilk")

	asrReady := customCmd != "" || fasterPython != "" || (whisperCLI != "" && whisperModel != "")
	silkReady := silkDecoder != "" || pysilkPython != ""
	warnings := []string{}
	if !asrReady {
		warnings = append(warnings, "local_asr_not_configured")
	}
	if !silkReady {
		warnings = append(warnings, "silk_decoder_missing_for_wechat_voice")
	}

	next := ""
	if !asrReady {
		next = appName + " asr setup"
	} else if !silkReady {
		next = appName + " asr setup, or install a WeChat SILK v3 decoder named silk_v3_decoder on PATH."
	} else {
		next = "Voice ASR is ready for cached readable WeChat voice audio."
	}

	return compactMap(map[string]any{
		"ready":                 asrReady,
		"wechat_voice_ready":    asrReady && silkReady,
		"default_engine":        "faster-whisper",
		"default_model":         defaultFasterWhisperModel,
		"default_language":      "zh",
		"state_venv":            venv,
		"state_venv_python":     venvPython,
		"custom_transcriber":    customCmd,
		"faster_whisper_python": fasterPython,
		"whisper_cli":           whisperCLI,
		"whisper_model":         whisperModel,
		"silk_decoder":          silkDecoder,
		"pysilk_python":         pysilkPython,
		"warnings":              warnings,
		"next_action":           next,
		"setup_command":         appName + " asr setup --model " + defaultFasterWhisperModel,
	})
}

func asrReadyBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func asrSetup(opts asrSetupOptions) (map[string]any, error) {
	if strictReadOnlyMode() {
		return nil, fmt.Errorf("ASR setup writes local support files; disable --strict-read-only and retry")
	}
	if opts.Model == "" {
		opts.Model = defaultFasterWhisperModel
	}
	if opts.Language == "" {
		opts.Language = "zh"
	}
	if opts.Device == "" {
		opts.Device = "cpu"
	}
	if opts.ComputeType == "" {
		opts.ComputeType = "int8"
	}
	venv := strings.TrimSpace(opts.Venv)
	if venv == "" {
		var err error
		venv, err = defaultASRVenvDir()
		if err != nil {
			return nil, err
		}
	}
	python := strings.TrimSpace(opts.Python)
	if python == "" {
		python = findPythonForASRSetup()
	}
	if python == "" {
		return nil, fmt.Errorf("python3 or python is required to create the ASR environment")
	}

	venvPython := asrVenvPythonPath(venv)
	actions := []string{
		"create ASR virtualenv at " + venv,
		"upgrade pip in ASR virtualenv",
		"install or upgrade faster-whisper and silk-python in ASR virtualenv",
	}
	if !opts.SkipModelDownload {
		actions = append(actions, "preload faster-whisper model "+opts.Model)
	}
	if opts.DryRun {
		return compactMap(map[string]any{
			"dry_run":             true,
			"ready":               false,
			"venv":                venv,
			"python":              python,
			"venv_python":         venvPython,
			"model":               opts.Model,
			"language":            opts.Language,
			"device":              opts.Device,
			"compute_type":        opts.ComputeType,
			"skip_model_download": opts.SkipModelDownload,
			"actions":             actions,
			"next_action":         appName + " asr setup --model " + opts.Model,
		}), nil
	}

	if opts.Force {
		_ = os.RemoveAll(venv)
	}
	if err := os.MkdirAll(filepath.Dir(venv), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(venvPython); err != nil {
		if out, err := runASRSetupCommand(python, "-m", "venv", venv); err != nil {
			return nil, fmt.Errorf("create ASR virtualenv failed: %w: %s", err, trimCommandOutput(out))
		}
	}
	if out, err := runASRSetupCommand(venvPython, "-m", "pip", "install", "--upgrade", "pip"); err != nil {
		return nil, fmt.Errorf("upgrade ASR pip failed: %w: %s", err, trimCommandOutput(out))
	}
	if out, err := runASRSetupCommand(venvPython, "-m", "pip", "install", "--upgrade", "faster-whisper", "silk-python"); err != nil {
		return nil, fmt.Errorf("install faster-whisper/silk-python failed: %w: %s", err, trimCommandOutput(out))
	}
	if !opts.SkipModelDownload {
		env := append(os.Environ(),
			"WECHAT_CLI_FASTER_WHISPER_DEVICE="+opts.Device,
			"WECHAT_CLI_FASTER_WHISPER_COMPUTE_TYPE="+opts.ComputeType,
		)
		if out, err := runASRSetupCommandEnv(env, venvPython, "-c", fasterWhisperWarmupPythonScript, opts.Model, opts.Language); err != nil {
			return nil, fmt.Errorf("preload faster-whisper model failed: %w: %s", err, trimCommandOutput(out))
		}
	}

	status := asrStatusData()
	return compactMap(map[string]any{
		"dry_run":             false,
		"ready":               status["ready"],
		"wechat_voice_ready":  status["wechat_voice_ready"],
		"venv":                venv,
		"venv_python":         venvPython,
		"model":               opts.Model,
		"language":            opts.Language,
		"device":              opts.Device,
		"compute_type":        opts.ComputeType,
		"skip_model_download": opts.SkipModelDownload,
		"actions":             actions,
		"warnings":            status["warnings"],
		"next_action":         status["next_action"],
	}), nil
}

const fasterWhisperWarmupPythonScript = `
import os
import sys
from faster_whisper import WhisperModel

model_name = sys.argv[1]
device = os.environ.get("WECHAT_CLI_FASTER_WHISPER_DEVICE") or "cpu"
compute_type = os.environ.get("WECHAT_CLI_FASTER_WHISPER_COMPUTE_TYPE") or "int8"
WhisperModel(model_name, device=device, compute_type=compute_type)
print("ready")
`

func defaultASRVenvDir() (string, error) {
	if p := envFirst("WECHAT_CLI_ASR_VENV", "WX_MCP_ASR_VENV"); p != "" {
		return filepath.Clean(p), nil
	}
	stateDir, err := appStateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "asr-venv"), nil
}

func defaultASRPythonCandidates() []string {
	venv, err := defaultASRVenvDir()
	if err != nil || venv == "" {
		return nil
	}
	return []string{asrVenvPythonPath(venv)}
}

func asrVenvPythonPath(venv string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venv, "Scripts", "python.exe")
	}
	return filepath.Join(venv, "bin", "python3")
}

func findPythonForASRSetup() string {
	if p := envFirst("WECHAT_CLI_ASR_SETUP_PYTHON", "WECHAT_CLI_ASR_PYTHON", "WX_MCP_ASR_SETUP_PYTHON", "WX_MCP_ASR_PYTHON"); p != "" {
		return p
	}
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

func findPysilkPython() string {
	return findPythonWithModule("pysilk")
}

func findConfiguredPythonWithModule(module string) string {
	candidates := configuredASRPythonCandidates()
	seen := map[string]bool{}
	for _, p := range candidates {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if pythonHasModule(p, module) {
			return p
		}
	}
	return ""
}

func configuredASRPythonCandidates() []string {
	var candidates []string
	if p := envFirst("WECHAT_CLI_ASR_PYTHON", "WECHAT_CLI_FASTER_WHISPER_PYTHON", "WX_MCP_ASR_PYTHON", "WX_MCP_FASTER_WHISPER_PYTHON"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates, defaultASRPythonCandidates()...)
	return candidates
}

func findPythonWithModule(module string) string {
	candidates := configuredASRPythonCandidates()
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	seen := map[string]bool{}
	for _, p := range candidates {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if pythonHasModule(p, module) {
			return p
		}
	}
	return ""
}

func pythonHasModule(python, module string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), asrProbeTimeout())
	defer cancel()
	return exec.CommandContext(ctx, python, "-c", "import "+module).Run() == nil
}

func commandPath(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

const pysilkDecodePythonScript = `
import sys
import pysilk

with open(sys.argv[1], "rb") as silk, open(sys.argv[2], "wb") as pcm:
    pysilk.decode(silk, pcm, 24000)
`

func (s *server) decodeSILKVoiceToWAVWithPysilk(path, python string) (string, error) {
	base := strings.TrimSuffix(path, filepath.Ext(path))
	wavPath := base + ".wav"
	if st, err := os.Stat(wavPath); err == nil && !st.IsDir() && st.Size() > 44 {
		return wavPath, nil
	}
	pcmPath := base + ".pcm"
	ctx, cancel := context.WithTimeout(context.Background(), voiceCommandTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", pysilkDecodePythonScript, path, pcmPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(pcmPath)
		return "", fmt.Errorf("pysilk decode failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	pcm, err := os.ReadFile(pcmPath)
	_ = os.Remove(pcmPath)
	if err != nil {
		return "", err
	}
	if err := writePCM16LEMonoWAV(wavPath, pcm, 24000); err != nil {
		return "", err
	}
	return wavPath, nil
}

func runASRSetupCommand(name string, args ...string) ([]byte, error) {
	return runASRSetupCommandEnv(os.Environ(), name, args...)
}

func runASRSetupCommandEnv(env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), asrSetupTimeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

func asrSetupTimeout() time.Duration {
	if raw := envFirst("WECHAT_CLI_ASR_SETUP_TIMEOUT_SECONDS", "WX_MCP_ASR_SETUP_TIMEOUT_SECONDS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 30 * time.Minute
}

func asrProbeTimeout() time.Duration {
	if raw := envFirst("WECHAT_CLI_ASR_PROBE_TIMEOUT_SECONDS", "WX_MCP_ASR_PROBE_TIMEOUT_SECONDS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 5 * time.Second
}

func trimCommandOutput(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 2000 {
		return s[:2000] + "...[truncated]"
	}
	return s
}
