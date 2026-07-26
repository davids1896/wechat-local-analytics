package main

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

func testProtoVarint(field int, value uint64) []byte {
	out := binary.AppendUvarint(nil, uint64(field<<3))
	return binary.AppendUvarint(out, value)
}

func testProtoBytes(field int, value []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(field<<3|2))
	out = binary.AppendUvarint(out, uint64(len(value)))
	return append(out, value...)
}

func testRoomDataUser(username, displayName string) []byte {
	out := testProtoBytes(1, []byte(username))
	if displayName != "" {
		out = append(out, testProtoBytes(2, []byte(displayName))...)
	}
	return append(out, testProtoVarint(3, 1)...)
}

func testQuoteRow(room, member, displayName string, createTime, sortSeq, localID int64) wcdb.Row {
	return wcdb.Row{
		"talker":      room,
		"local_id":    localID,
		"create_time": createTime,
		"sort_seq":    sortSeq,
		"message_content_parsed": map[string]any{
			"refermsg": map[string]any{
				"fromusr":     room,
				"chatusr":     member,
				"displayname": displayName,
			},
		},
	}
}

func TestParseRoomDataNicknames(t *testing.T) {
	var blob []byte
	blob = append(blob, testProtoVarint(5, 500)...)
	blob = append(blob, testProtoBytes(1, testRoomDataUser("wxid_member_a", "Group Card A"))...)
	blob = append(blob, testProtoBytes(1, testRoomDataUser("wxid_member_b", "Group Card B"))...)
	blob = append(blob, testProtoBytes(1, testRoomDataUser("wxid_without_room_name", ""))...)

	got := parseRoomDataNicknames(blob)
	if got["wxid_member_a"] != "Group Card A" || got["wxid_member_b"] != "Group Card B" {
		t.Fatalf("RoomData nicknames = %#v", got)
	}
	if displayName, ok := got["wxid_without_room_name"]; !ok || displayName != "" {
		t.Fatalf("member without a group nickname should retain empty roster presence: %#v", got)
	}
}

func TestParseRoomDataNicknamesBoundsAndMalformedData(t *testing.T) {
	valid := testProtoBytes(1, testRoomDataUser("wxid_member_a", "Group Card A"))
	malformedTail := append(append([]byte(nil), valid...), 0x0a, 0xff)
	if got := parseRoomDataNicknames(malformedTail); got["wxid_member_a"] != "Group Card A" {
		t.Fatalf("valid prefix was lost after malformed tail: %#v", got)
	}

	invalidUTF8 := testProtoBytes(1, append(testProtoBytes(1, []byte("wxid_member_a")), testProtoBytes(2, []byte{0xff, 0xfe})...))
	if got := parseRoomDataNicknames(invalidUTF8); len(got) != 1 || got["wxid_member_a"] != "" {
		t.Fatalf("invalid UTF-8 nickname should retain only member presence: %#v", got)
	}

	if got := parseRoomDataNicknames(make([]byte, maxRoomDataBytes+1)); len(got) != 0 {
		t.Fatalf("oversized RoomData should be rejected: %#v", got)
	}
	oversizedUser := testProtoBytes(1, make([]byte, maxRoomDataUserBytes+1))
	if got := parseRoomDataNicknames(oversizedUser); len(got) != 0 {
		t.Fatalf("oversized RoomDataUser should be rejected: %#v", got)
	}
	oversizedUsername := testProtoBytes(1, testRoomDataUser(strings.Repeat("u", maxIdentityUsernameBytes+1), "Group Card A"))
	if got := parseRoomDataNicknames(oversizedUsername); len(got) != 0 {
		t.Fatalf("oversized username should be rejected: %#v", got)
	}
	oversizedNickname := testProtoBytes(1, testRoomDataUser("wxid_member_a", strings.Repeat("n", maxGroupNicknameBytes+1)))
	if got := parseRoomDataNicknames(oversizedNickname); len(got) != 1 || got["wxid_member_a"] != "" {
		t.Fatalf("oversized nickname should retain only member presence: %#v", got)
	}
}

func TestQuoteGroupNicknameCandidatesUsesCarryingRowOrder(t *testing.T) {
	oldCarrier := testQuoteRow("room_a@chatroom", "wxid_member_a", "Group Card Old", 100, 1000, 10)
	oldCarrier["message_content_parsed"].(map[string]any)["refermsg"].(map[string]any)["createtime"] = int64(9999)
	newCarrier := testQuoteRow("room_a@chatroom", "wxid_member_a", "Group Card New", 200, 10, 20)
	newCarrier["message_content_parsed"].(map[string]any)["refermsg"].(map[string]any)["createtime"] = int64(1)
	newerSortSeq := testQuoteRow("room_a@chatroom", "wxid_member_b", "Group Card B New", 300, 20, 30)
	olderSortSeq := testQuoteRow("room_a@chatroom", "wxid_member_b", "Group Card B Old", 300, 10, 40)

	got := candidateNames(quoteGroupNicknameCandidates([]wcdb.Row{oldCarrier, newCarrier, newerSortSeq, olderSortSeq}))
	if got["room_a@chatroom"]["wxid_member_a"] != "Group Card New" {
		t.Fatalf("refer.createtime incorrectly overrode carrying row time: %#v", got)
	}
	if got["room_a@chatroom"]["wxid_member_b"] != "Group Card B New" {
		t.Fatalf("sort_seq tie-break was not applied: %#v", got)
	}
}

func TestQuoteGroupNicknameCandidatesRejectsAmbiguousIdentityChains(t *testing.T) {
	valid := testQuoteRow("room_a@chatroom", "wxid_member_a", "Group Card A", 100, 1, 1)
	wrongRoom := testQuoteRow("room_a@chatroom", "wxid_member_b", "Wrong Room", 100, 1, 2)
	wrongRoom["message_content_parsed"].(map[string]any)["refermsg"].(map[string]any)["fromusr"] = "room_b@chatroom"
	privateFromUser := testQuoteRow("room_a@chatroom", "wxid_member_c", "Private From", 100, 1, 3)
	privateFromUser["message_content_parsed"].(map[string]any)["refermsg"].(map[string]any)["fromusr"] = "wxid_private"
	memberIsRoom := testQuoteRow("room_a@chatroom", "room_b@chatroom", "Room Member", 100, 1, 4)
	inverse := testQuoteRow("room_a@chatroom", "room_a@chatroom", "Inverse", 100, 1, 5)
	inverseRefer := inverse["message_content_parsed"].(map[string]any)["refermsg"].(map[string]any)
	inverseRefer["fromusr"] = "wxid_member_d"
	inverseRefer["chatusr"] = "room_a@chatroom"
	tooLongMember := testQuoteRow("room_a@chatroom", strings.Repeat("m", maxIdentityUsernameBytes+1), "Too Long Member", 100, 1, 6)
	tooLongNickname := testQuoteRow("room_a@chatroom", "wxid_member_e", strings.Repeat("n", maxGroupNicknameBytes+1), 100, 1, 7)

	got := candidateNames(quoteGroupNicknameCandidates([]wcdb.Row{
		valid, wrongRoom, privateFromUser, memberIsRoom, inverse, tooLongMember, tooLongNickname,
	}))
	if len(got) != 1 || len(got["room_a@chatroom"]) != 1 || got["room_a@chatroom"]["wxid_member_a"] != "Group Card A" {
		t.Fatalf("ambiguous identity evidence was accepted: %#v", got)
	}
}

func TestQuoteGroupNicknameCandidatesFromContentUsesTinyXMLParser(t *testing.T) {
	content := `wxid_wrapper:` + "\n" + `<msg><appmsg><title>Reply</title><refermsg><fromusr>room_a@chatroom</fromusr><chatusr>wxid_member_a</chatusr><displayname>Group Card A</displayname><content>` +
		strings.Repeat("ignored", 50) + `</content></refermsg></appmsg></msg>`
	rows := []wcdb.Row{{
		"talker":          "room_a@chatroom",
		"local_id":        int64(11),
		"create_time":     int64(100),
		"sort_seq":        int64(10),
		"message_content": content,
	}}
	got := candidateNames(quoteGroupNicknameCandidatesFromContent(rows))
	if got["room_a@chatroom"]["wxid_member_a"] != "Group Card A" {
		t.Fatalf("tiny XML parser did not extract strict refermsg identity: %#v", got)
	}

	rows[0]["message_content"] = strings.Repeat("x", maxQuoteMessageContentBytes+1)
	if got := quoteGroupNicknameCandidatesFromContent(rows); len(got) != 0 {
		t.Fatalf("oversized quote content should be rejected: %#v", got)
	}
}

func TestGroupNicknameSourcePrecedenceAndHistoricalFallback(t *testing.T) {
	quoteRow := testQuoteRow("room_a@chatroom", "wxid_member_a", "Group Card Quote", 100, 1, 1)
	roomData := map[string]map[string]string{
		"room_a@chatroom": {"wxid_member_a": "Group Card Roster"},
	}
	historical := map[string]map[string]string{
		"room_a@chatroom": {
			"wxid_member_a": "Group Card Historical",
			"wxid_member_b": "Group Card B Historical",
		},
	}
	got := groupNicknameNamesFromSources([]wcdb.Row{quoteRow}, roomData, historical)
	if got["room_a@chatroom"]["wxid_member_a"] != "Group Card Roster" {
		t.Fatalf("RoomData did not outrank quote evidence: %#v", got)
	}

	newerHistorical := map[string]map[string]string{
		"room_a@chatroom": {"wxid_member_a": "Group Card Newer Scan"},
	}
	gotWithoutRoomData := groupNicknameNamesFromSources([]wcdb.Row{quoteRow}, nil, newerHistorical)
	if gotWithoutRoomData["room_a@chatroom"]["wxid_member_a"] != "Group Card Newer Scan" {
		t.Fatalf("newer historical scan did not outrank the request row quote: %#v", gotWithoutRoomData)
	}

	plainRow := wcdb.Row{
		"talker":              "room_a@chatroom",
		"sender_wxid":         "wxid_member_b",
		"sender_display_name": "Contact B",
	}
	namesAfterContactFailure := groupNicknameNamesFromSources([]wcdb.Row{plainRow}, nil, historical)
	applyGroupNicknames([]wcdb.Row{plainRow}, namesAfterContactFailure)
	if rowString(plainRow, "sender_display_name") != "Group Card B Historical" ||
		rowString(plainRow, "sender_group_nickname") != "Group Card B Historical" ||
		rowString(plainRow, "sender_contact_display") != "Contact B" {
		t.Fatalf("historical fallback after missing contact data failed: %#v", plainRow)
	}
}

func TestApplyGroupNicknamesPreservesUnresolvedFallbacks(t *testing.T) {
	raw := wcdb.Row{
		"talker":              "room_a@chatroom",
		"sender_wxid":         "wxid_member_a",
		"sender_display_name": "wxid_member_a",
	}
	contact := wcdb.Row{
		"talker":              "room_a@chatroom",
		"sender_wxid":         "wxid_member_b",
		"sender_display_name": "Contact B",
	}
	private := wcdb.Row{
		"talker":              "wxid_private",
		"sender_wxid":         "wxid_private",
		"sender_display_name": "Private Contact",
	}
	applyGroupNicknames([]wcdb.Row{raw, contact, private}, nil)
	if rowString(raw, "sender_display_name") != "wxid_member_a" || rowString(raw, "sender_contact_display") != "" {
		t.Fatalf("raw wxid fallback was mislabeled as contact display: %#v", raw)
	}
	rawAgent := agentMessage(raw)
	if _, ok := rawAgent["sender_group_nickname"]; ok {
		t.Fatalf("unresolved group agent output gained a group nickname: %#v", rawAgent)
	}
	rawLite := liteMessages([]wcdb.Row{copyRow(raw)}, "lite")[0]
	if _, ok := rawLite["sender_group_nickname"]; ok {
		t.Fatalf("unresolved group lite output gained a group nickname: %#v", rawLite)
	}
	rawSearch := cliSearchMessageRow(map[string]any(raw), map[string]any{})
	if _, ok := rawSearch["sender_group_nickname"]; ok {
		t.Fatalf("unresolved group search output gained a group nickname: %#v", rawSearch)
	}
	rawMedia := map[string]any{}
	addOptionalGroupIdentity(rawMedia, raw)
	if len(rawMedia) != 0 {
		t.Fatalf("unresolved group media identity gained optional fields: %#v", rawMedia)
	}
	if rowString(contact, "sender_display_name") != "Contact B" || rowString(contact, "sender_contact_display") != "Contact B" {
		t.Fatalf("unresolved contact display was not preserved: %#v", contact)
	}
	if _, ok := private["sender_group_nickname"]; ok {
		t.Fatalf("private row gained group fields: %#v", private)
	}
	privateAgent := agentMessage(private)
	if _, ok := privateAgent["sender_group_nickname"]; ok {
		t.Fatalf("private agent output gained a group nickname: %#v", privateAgent)
	}
	if _, ok := privateAgent["sender_contact_display"]; ok {
		t.Fatalf("private agent output gained a group contact display: %#v", privateAgent)
	}
}

func TestAttachGroupNicknamesFallsBackAfterRoomDataFailure(t *testing.T) {
	row := wcdb.Row{
		"talker":              "room_a@chatroom",
		"sender_wxid":         "wxid_member_a",
		"sender_display_name": "Contact A",
	}
	quoteCalled := false
	attachGroupNicknames(
		[]wcdb.Row{row},
		func(needed map[string]map[string]bool) map[string]map[string]string {
			if !needed["room_a@chatroom"]["wxid_member_a"] {
				t.Fatalf("RoomData lookup received the wrong identity set: %#v", needed)
			}
			return nil
		},
		func(missing map[string]map[string]bool) map[string]map[string]string {
			quoteCalled = true
			if !missing["room_a@chatroom"]["wxid_member_a"] {
				t.Fatalf("quote lookup received the wrong missing identity set: %#v", missing)
			}
			return map[string]map[string]string{
				"room_a@chatroom": {"wxid_member_a": "Group Card A"},
			}
		},
	)
	if !quoteCalled {
		t.Fatal("quote fallback was not called after RoomData lookup failed")
	}
	if rowString(row, "sender_display_name") != "Group Card A" ||
		rowString(row, "sender_group_nickname") != "Group Card A" ||
		rowString(row, "sender_contact_display") != "Contact A" {
		t.Fatalf("quote fallback did not preserve both identity levels: %#v", row)
	}
}

func TestAttachGroupNicknamesScansBeforeUsingRequestRowQuote(t *testing.T) {
	row := testQuoteRow("room_a@chatroom", "wxid_member_a", "Group Card Old", 100, 1, 1)
	row["sender_wxid"] = "wxid_member_a"
	row["sender_display_name"] = "Contact A"
	quoteCalled := false
	attachGroupNicknames(
		[]wcdb.Row{row},
		func(map[string]map[string]bool) map[string]map[string]string {
			return nil
		},
		func(missing map[string]map[string]bool) map[string]map[string]string {
			quoteCalled = true
			if !missing["room_a@chatroom"]["wxid_member_a"] {
				t.Fatalf("quote lookup received the wrong missing identity set: %#v", missing)
			}
			return map[string]map[string]string{
				"room_a@chatroom": {"wxid_member_a": "Group Card New"},
			}
		},
	)
	if !quoteCalled {
		t.Fatal("request row quote incorrectly prevented the historical scan")
	}
	if rowString(row, "sender_group_nickname") != "Group Card New" ||
		rowString(row, "sender_display_name") != "Group Card New" ||
		rowString(row, "sender_contact_display") != "Contact A" {
		t.Fatalf("historical scan did not outrank the request row quote: %#v", row)
	}
}

func TestAttachGroupNicknamesSkipsFallbackWhenRoomDataResolvesMember(t *testing.T) {
	row := wcdb.Row{
		"talker":              "room_a@chatroom",
		"sender_wxid":         "wxid_member_a",
		"sender_display_name": "Contact A",
	}
	attachGroupNicknames(
		[]wcdb.Row{row},
		func(map[string]map[string]bool) map[string]map[string]string {
			return map[string]map[string]string{
				"room_a@chatroom": {"wxid_member_a": "Current Group Card"},
			}
		},
		func(map[string]map[string]bool) map[string]map[string]string {
			t.Fatal("quote fallback should not run when RoomData resolves the member")
			return nil
		},
	)
	if rowString(row, "sender_group_nickname") != "Current Group Card" ||
		rowString(row, "sender_contact_display") != "Contact A" {
		t.Fatalf("RoomData identity was not applied: %#v", row)
	}
}

func TestAttachGroupNicknamesDoesNotResurrectClearedRoomNickname(t *testing.T) {
	row := testQuoteRow("room_a@chatroom", "wxid_member_a", "Old Group Card", 100, 1, 1)
	row["sender_wxid"] = "wxid_member_a"
	row["sender_display_name"] = "Contact A"
	attachGroupNicknames(
		[]wcdb.Row{row},
		func(map[string]map[string]bool) map[string]map[string]string {
			return map[string]map[string]string{
				"room_a@chatroom": {"wxid_member_a": ""},
			}
		},
		func(map[string]map[string]bool) map[string]map[string]string {
			t.Fatal("quote fallback should not run for a member present in current RoomData")
			return nil
		},
	)
	if rowString(row, "sender_group_nickname") != "" ||
		rowString(row, "sender_display_name") != "Contact A" ||
		rowString(row, "sender_contact_display") != "Contact A" {
		t.Fatalf("cleared room nickname was resurrected: %#v", row)
	}
}

func TestGroupNicknameFieldsSurviveAgentLiteAndSearchViews(t *testing.T) {
	row := wcdb.Row{
		"talker":                 "room_a@chatroom",
		"talker_display_name":    "Synthetic Room",
		"chat_type":              "group",
		"local_id":               int64(1),
		"create_time":            int64(1776330000),
		"create_time_human":      "2026-04-14 21:00:00",
		"sender_wxid":            "wxid_member_a",
		"sender_display_name":    "Group Card A",
		"sender_group_nickname":  "Group Card A",
		"sender_contact_display": "Contact A",
		"kind_name":              "text",
		"content_summary":        "Synthetic message",
		"is_from_me":             false,
	}

	agent := agentMessage(copyRow(row))
	if agent["sender"] != "Group Card A" ||
		agent["sender_group_nickname"] != "Group Card A" ||
		agent["sender_contact_display"] != "Contact A" {
		t.Fatalf("agent view lost group identity fields: %#v", agent)
	}

	lite := liteMessages([]wcdb.Row{copyRow(row)}, "lite")[0]
	if lite["sender_group_nickname"] != "Group Card A" || lite["sender_contact_display"] != "Contact A" {
		t.Fatalf("lite view lost group identity fields: %#v", lite)
	}

	search := cliSearchMessageRow(map[string]any(row), map[string]any{})
	if search["sender"] != "Group Card A" ||
		search["sender_group_nickname"] != "Group Card A" ||
		search["sender_contact_display"] != "Contact A" {
		t.Fatalf("search view lost group identity fields: %#v", search)
	}
}
