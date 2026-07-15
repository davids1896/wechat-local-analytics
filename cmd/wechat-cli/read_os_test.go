package main

import (
	"testing"

	"github.com/r266-tech/wechat-cli/internal/config"
)

func TestReadOSStatusHidesAccountIdentifierUnlessDebug(t *testing.T) {
	s := &server{cfg: &config.Config{
		Wxid:   "wxid_private_account",
		DBRoot: t.TempDir(),
		Keys:   map[string]string{"salt": "key"},
	}}

	status := s.readOSStatus(false)
	account, _ := status["account"].(map[string]any)
	if account["wxid"] != nil {
		t.Fatalf("default status exposed wxid: %#v", account)
	}
	if account["identity_configured"] != true {
		t.Fatalf("identity_configured = %#v, want true", account["identity_configured"])
	}

	debugStatus := s.readOSStatus(true)
	debug, _ := debugStatus["account_debug"].(map[string]any)
	if debug["wxid"] != "wxid_private_account" {
		t.Fatalf("debug wxid = %#v", debug["wxid"])
	}
}

func TestReadOSFullChatWorkflowUsesStableCursor(t *testing.T) {
	for _, workflow := range readOSWorkflows() {
		if workflow["name"] != "page_full_chat" {
			continue
		}
		commands, _ := workflow["commands"].([]string)
		if len(commands) != 2 || commands[1] != `repeat with --before-message <data.query.cursor.next_before_message> while data.query.has_more` {
			t.Fatalf("page_full_chat commands = %#v", commands)
		}
		return
	}
	t.Fatal("page_full_chat workflow not found")
}
