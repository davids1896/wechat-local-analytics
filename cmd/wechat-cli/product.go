package main

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	appName       = "wechat-cli"
	legacyAppName = "wx-mcp"
	appVersion    = "1.6.20"

	stateDirName       = ".wechat-cli"
	legacyStateDirName = ".wx-mcp"
)

func envFirst(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}

func envBoolAny(names ...string) bool {
	for _, name := range names {
		if envBool(name) {
			return true
		}
	}
	return false
}

func strictReadOnlyMode() bool {
	if strings.TrimSpace(os.Getenv("WECHAT_CLI_STRICT_READ_ONLY")) != "" {
		return envBool("WECHAT_CLI_STRICT_READ_ONLY")
	}
	return envBool("WX_MCP_STRICT_READ_ONLY")
}

func appStateDir() (string, error) {
	if p := envFirst("WECHAT_CLI_STATE_DIR", "WX_MCP_STATE_DIR"); p != "" {
		return filepath.Clean(p), nil
	}
	home, err := wechatCLIHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, stateDirName), nil
}

func legacyStateDir() (string, error) {
	home, err := wechatCLIHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, legacyStateDirName), nil
}
