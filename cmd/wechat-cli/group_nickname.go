package main

import (
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/r266-tech/wechat-cli/internal/wcdb"
)

const (
	quoteMessageLocalType           int64 = (57 << 32) | 49
	quoteNicknameScanLimit                = 500
	roomQueryBatchSize                    = 200
	maxRoomDataBytes                      = 8 << 20
	maxRoomDataUserBytes                  = 16 << 10
	maxRoomDataMembers                    = 10000
	maxIdentityUsernameBytes              = 512
	maxGroupNicknameBytes                 = 2048
	maxQuoteMessageContentBytes           = 2 << 20
	maxGroupNicknameRoomsPerRequest       = 32
	maxQuoteNicknameRowsPerRequest        = 4000
)

// parseRoomDataNicknames decodes the useful subset of WeChat's RoomData
// protobuf stored in chat_room.ext_buffer:
//
//	message RoomData {
//	  repeated RoomDataUser users = 1;
//	}
//	message RoomDataUser {
//	  string userName = 1;
//	  optional string displayName = 2;
//	}
//
// It intentionally uses a small wire-format reader instead of pulling in a
// protobuf runtime for two string fields. Malformed or unknown fields are
// ignored best-effort and never make message reads fail.
func parseRoomDataNicknames(blob []byte) map[string]string {
	out := make(map[string]string)
	if len(blob) == 0 || len(blob) > maxRoomDataBytes {
		return out
	}
	for len(blob) > 0 {
		field, wire, value, rest, ok := consumeProtoField(blob)
		if !ok {
			break
		}
		blob = rest
		if field != 1 || wire != 2 {
			continue
		}
		if len(value) > maxRoomDataUserBytes {
			continue
		}
		username, displayName := parseRoomDataUser(value)
		if username != "" {
			out[username] = displayName
			if len(out) >= maxRoomDataMembers {
				break
			}
		}
	}
	return out
}

func parseRoomDataUser(blob []byte) (string, string) {
	var username, displayName string
	for len(blob) > 0 {
		field, wire, value, rest, ok := consumeProtoField(blob)
		if !ok {
			break
		}
		blob = rest
		if wire != 2 || !utf8.Valid(value) {
			continue
		}
		switch field {
		case 1:
			if len(value) <= maxIdentityUsernameBytes {
				username = strings.TrimSpace(string(value))
			}
		case 2:
			if len(value) <= maxGroupNicknameBytes {
				displayName = strings.TrimSpace(string(value))
			}
		}
	}
	return username, displayName
}

func consumeProtoField(blob []byte) (field int, wire int, value, rest []byte, ok bool) {
	key, keyLen := binary.Uvarint(blob)
	if keyLen <= 0 || key == 0 {
		return 0, 0, nil, nil, false
	}
	blob = blob[keyLen:]
	field = int(key >> 3)
	wire = int(key & 7)
	switch wire {
	case 0:
		_, n := binary.Uvarint(blob)
		if n <= 0 {
			return 0, 0, nil, nil, false
		}
		return field, wire, nil, blob[n:], true
	case 1:
		if len(blob) < 8 {
			return 0, 0, nil, nil, false
		}
		return field, wire, nil, blob[8:], true
	case 2:
		size, n := binary.Uvarint(blob)
		if n <= 0 {
			return 0, 0, nil, nil, false
		}
		blob = blob[n:]
		if size > uint64(len(blob)) {
			return 0, 0, nil, nil, false
		}
		end := int(size)
		return field, wire, blob[:end], blob[end:], true
	case 5:
		if len(blob) < 4 {
			return 0, 0, nil, nil, false
		}
		return field, wire, nil, blob[4:], true
	default:
		return 0, 0, nil, nil, false
	}
}

type groupNicknameCandidate struct {
	name       string
	createTime int64
	sortSeq    int64
	localID    int64
}

type groupNicknameCandidates map[string]map[string]groupNicknameCandidate

type quoteNicknameXML struct {
	AppMsg struct {
		ReferMsg struct {
			ChatUsr     string `xml:"chatusr"`
			DisplayName string `xml:"displayname"`
			FromUsr     string `xml:"fromusr"`
		} `xml:"refermsg"`
	} `xml:"appmsg"`
}

// quoteGroupNicknameCandidates extracts the identity evidence embedded in
// quoted-message XML. WeChat 4.x commonly stores it as:
// refermsg.fromusr=room@chatroom, refermsg.chatusr=member, displayname=群昵称.
// The carrying row's talker must exactly match fromusr; looser orientations
// are rejected so a quoted message cannot assign a nickname to the wrong room.
func quoteGroupNicknameCandidates(rows []wcdb.Row) groupNicknameCandidates {
	out := make(groupNicknameCandidates)
	for _, row := range rows {
		parsed, _ := row["message_content_parsed"].(map[string]any)
		refer, _ := parsed["refermsg"].(map[string]any)
		if refer == nil {
			continue
		}
		displayName := strings.TrimSpace(stringMapValue(refer, "displayname"))
		if displayName == "" || len(displayName) > maxGroupNicknameBytes || !utf8.ValidString(displayName) {
			continue
		}
		currentRoom := strings.TrimSpace(rowString(row, "talker"))
		fromUser := strings.TrimSpace(stringMapValue(refer, "fromusr"))
		chatUser := strings.TrimSpace(stringMapValue(refer, "chatusr"))
		if !strings.HasSuffix(currentRoom, "@chatroom") ||
			fromUser != currentRoom ||
			chatUser == "" ||
			len(currentRoom) > maxIdentityUsernameBytes ||
			len(chatUser) > maxIdentityUsernameBytes ||
			strings.HasSuffix(chatUser, "@chatroom") {
			continue
		}
		candidate := groupNicknameCandidate{
			name:       displayName,
			createTime: rowInt64(row, "create_time"),
			sortSeq:    rowInt64(row, "sort_seq"),
			localID:    rowInt64(row, "local_id"),
		}
		if out[currentRoom] == nil {
			out[currentRoom] = make(map[string]groupNicknameCandidate)
		}
		previous, exists := out[currentRoom][chatUser]
		if !exists || newerGroupNicknameCandidate(candidate, previous) {
			out[currentRoom][chatUser] = candidate
		}
	}
	return out
}

// quoteGroupNicknameCandidatesFromContent parses only the three refermsg
// identity strings. It avoids the full recursive message parser for the
// bounded historical fallback scan.
func quoteGroupNicknameCandidatesFromContent(rows []wcdb.Row) groupNicknameCandidates {
	identityRows := make([]wcdb.Row, 0, len(rows))
	for _, row := range rows {
		content := rowString(row, "message_content")
		if len(content) == 0 || len(content) > maxQuoteMessageContentBytes {
			continue
		}
		var parsed quoteNicknameXML
		if err := xml.Unmarshal([]byte(stripMsgPrefix(content)), &parsed); err != nil {
			continue
		}
		refer := parsed.AppMsg.ReferMsg
		if refer.ChatUsr == "" && refer.DisplayName == "" && refer.FromUsr == "" {
			continue
		}
		identityRow := wcdb.Row{
			"talker":      rowString(row, "talker"),
			"create_time": rowInt64(row, "create_time"),
			"sort_seq":    rowInt64(row, "sort_seq"),
			"local_id":    rowInt64(row, "local_id"),
			"message_content_parsed": map[string]any{
				"refermsg": map[string]any{
					"chatusr":     refer.ChatUsr,
					"displayname": refer.DisplayName,
					"fromusr":     refer.FromUsr,
				},
			},
		}
		identityRows = append(identityRows, identityRow)
	}
	return quoteGroupNicknameCandidates(identityRows)
}

func newerGroupNicknameCandidate(candidate, previous groupNicknameCandidate) bool {
	if candidate.createTime != previous.createTime {
		return candidate.createTime > previous.createTime
	}
	if candidate.sortSeq != previous.sortSeq {
		return candidate.sortSeq > previous.sortSeq
	}
	return candidate.localID > previous.localID
}

func candidateNames(candidates groupNicknameCandidates) map[string]map[string]string {
	out := make(map[string]map[string]string)
	for room, members := range candidates {
		for member, candidate := range members {
			if candidate.name == "" {
				continue
			}
			if out[room] == nil {
				out[room] = make(map[string]string)
			}
			out[room][member] = candidate.name
		}
	}
	return out
}

func mergeNicknameCandidates(dst, src groupNicknameCandidates) {
	for room, members := range src {
		if dst[room] == nil {
			dst[room] = make(map[string]groupNicknameCandidate)
		}
		for member, candidate := range members {
			previous, exists := dst[room][member]
			if !exists || newerGroupNicknameCandidate(candidate, previous) {
				dst[room][member] = candidate
			}
		}
	}
}

func messageGroupMembers(rows []wcdb.Row) map[string]map[string]bool {
	out := make(map[string]map[string]bool)
	for _, row := range rows {
		room := strings.TrimSpace(rowString(row, "talker"))
		member := strings.TrimSpace(rowString(row, "sender_wxid"))
		if !strings.HasSuffix(room, "@chatroom") ||
			member == "" ||
			len(room) > maxIdentityUsernameBytes ||
			len(member) > maxIdentityUsernameBytes ||
			strings.HasSuffix(member, "@chatroom") {
			continue
		}
		if out[room] == nil && len(out) >= maxGroupNicknameRoomsPerRequest {
			continue
		}
		if out[room] == nil {
			out[room] = make(map[string]bool)
		}
		out[room][member] = true
	}
	return out
}

func membersAbsentFromRoomData(
	needed map[string]map[string]bool,
	roomData map[string]map[string]string,
) map[string]map[string]bool {
	out := make(map[string]map[string]bool)
	for room, members := range needed {
		for member := range members {
			if _, knownInCurrentRoster := roomData[room][member]; knownInCurrentRoster {
				continue
			}
			if out[room] == nil {
				out[room] = make(map[string]bool)
			}
			out[room][member] = true
		}
	}
	return out
}

func mergeGroupNicknameMaps(dst, src map[string]map[string]string, overwrite bool) {
	for room, members := range src {
		if dst[room] == nil {
			dst[room] = make(map[string]string)
		}
		for member, name := range members {
			if name == "" || (!overwrite && dst[room][member] != "") {
				continue
			}
			dst[room][member] = name
		}
	}
}

func copyGroupNicknameMap(src map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string)
	mergeGroupNicknameMaps(out, src, true)
	return out
}

func mergeFallbackNicknameMaps(dst, src, roomData map[string]map[string]string) {
	for room, members := range src {
		for member, name := range members {
			// Current roster presence is authoritative even when displayName is
			// empty; otherwise an older quote could resurrect a cleared card.
			if _, knownInCurrentRoster := roomData[room][member]; knownInCurrentRoster {
				continue
			}
			if name == "" || dst[room][member] != "" {
				continue
			}
			if dst[room] == nil {
				dst[room] = make(map[string]string)
			}
			dst[room][member] = name
		}
	}
}

// Current RoomData wins because a member may change their room nickname after
// older quoted messages have captured a previous value.
func groupNicknameNamesFromSources(rows []wcdb.Row, roomData, historical map[string]map[string]string) map[string]map[string]string {
	names := copyGroupNicknameMap(roomData)
	mergeFallbackNicknameMaps(names, historical, roomData)
	mergeFallbackNicknameMaps(names, candidateNames(quoteGroupNicknameCandidates(rows)), roomData)
	return names
}

// applyGroupNicknames preserves the contact-level display string separately
// before making the room-specific nickname the primary sender display.
func applyGroupNicknames(rows []wcdb.Row, names map[string]map[string]string) {
	for _, row := range rows {
		room := rowString(row, "talker")
		member := rowString(row, "sender_wxid")
		if !strings.HasSuffix(room, "@chatroom") || member == "" {
			continue
		}
		if contactDisplay := rowString(row, "sender_display_name"); contactDisplay != "" &&
			contactDisplay != member &&
			contactDisplay != rowString(row, "sender_group_nickname") {
			row["sender_contact_display"] = contactDisplay
		}
		if groupNickname := strings.TrimSpace(names[room][member]); groupNickname != "" {
			row["sender_group_nickname"] = groupNickname
			row["sender_display_name"] = groupNickname
		}
	}
}

func addOptionalGroupIdentity(out map[string]any, row wcdb.Row) {
	if groupNickname := rowString(row, "sender_group_nickname"); groupNickname != "" {
		out["sender_group_nickname"] = groupNickname
	}
	if contactDisplay := rowString(row, "sender_contact_display"); contactDisplay != "" {
		out["sender_contact_display"] = contactDisplay
	}
}

func attachGroupNicknames(
	rows []wcdb.Row,
	roomDataLookup func(map[string]map[string]bool) map[string]map[string]string,
	quoteLookup func(map[string]map[string]bool) map[string]map[string]string,
) {
	needed := messageGroupMembers(rows)
	if len(needed) == 0 {
		return
	}

	roomData := roomDataLookup(needed)

	// If contact.db is unavailable or unreadable, or the member is absent from
	// current RoomData, scan recent quote rows from only the affected room(s).
	// This remains a strict read-only, bounded fallback.
	missing := membersAbsentFromRoomData(needed, roomData)
	var historical map[string]map[string]string
	if len(missing) > 0 {
		historical = quoteLookup(missing)
	}
	names := groupNicknameNamesFromSources(rows, roomData, historical)
	applyGroupNicknames(rows, names)
}

func (s *server) attachMessageDisplayNames(rows []wcdb.Row) {
	if len(rows) == 0 {
		return
	}
	s.attachDisplayNames(rows,
		[2]string{"talker", "talker_display_name"},
		[2]string{"sender_wxid", "sender_display_name"})
	attachGroupNicknames(rows, s.lookupRoomDataNicknames, s.lookupQuoteGroupNicknames)
}

func (s *server) lookupRoomDataNicknames(needed map[string]map[string]bool) map[string]map[string]string {
	if len(needed) == 0 {
		return nil
	}
	db, err := s.openDB("contact", "contact.db")
	if err != nil {
		return nil
	}
	defer db.Close()

	rooms := make([]string, 0, len(needed))
	for room := range needed {
		rooms = append(rooms, room)
	}
	sort.Strings(rooms)
	if len(rooms) > maxGroupNicknameRoomsPerRequest {
		rooms = rooms[:maxGroupNicknameRoomsPerRequest]
	}
	out := make(map[string]map[string]string)
	for start := 0; start < len(rooms); start += roomQueryBatchSize {
		end := start + roomQueryBatchSize
		if end > len(rooms) {
			end = len(rooms)
		}
		batch := rooms[start:end]
		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, room := range batch {
			placeholders[i] = "?"
			args[i] = room
		}
		rows, queryErr := db.Query(fmt.Sprintf(
			`SELECT username, ext_buffer FROM chat_room WHERE username IN (%s)`,
			strings.Join(placeholders, ",")), args...)
		if queryErr != nil {
			return out
		}
		for _, row := range rows {
			room := rowString(row, "username")
			for member, name := range parseRoomDataNicknames(rowBytes(row, "ext_buffer")) {
				if !needed[room][member] {
					continue
				}
				if out[room] == nil {
					out[room] = make(map[string]string)
				}
				out[room][member] = name
			}
		}
	}
	return out
}

func (s *server) lookupQuoteGroupNicknames(needed map[string]map[string]bool) map[string]map[string]string {
	if len(needed) == 0 {
		return nil
	}
	allCandidates := make(groupNicknameCandidates)
	rooms := make([]string, 0, len(needed))
	for room := range needed {
		rooms = append(rooms, room)
	}
	sort.Strings(rooms)
	if len(rooms) > maxGroupNicknameRoomsPerRequest {
		rooms = rooms[:maxGroupNicknameRoomsPerRequest]
	}
	remainingRows := maxQuoteNicknameRowsPerRequest
	for _, room := range rooms {
		if remainingRows <= 0 {
			break
		}
		tableName := "Msg_" + talkerHash(room)
		shards, err := s.findMsgDBs(tableName)
		if err != nil {
			continue
		}
		for _, shard := range shards {
			if remainingRows <= 0 {
				break
			}
			queryLimit := minInt(quoteNicknameScanLimit, remainingRows)
			rows, queryErr := shard.DB.Query(fmt.Sprintf(
				`SELECT local_id, create_time, sort_seq, local_type, message_content
				FROM %s WHERE local_type = ?
				ORDER BY sort_seq DESC, local_id DESC LIMIT ?`,
				quoteIdent(tableName)), quoteMessageLocalType, queryLimit)
			if queryErr != nil {
				continue
			}
			remainingRows -= len(rows)
			boundedRows := rows[:0]
			for _, row := range rows {
				if size := len(rowBytes(row, "message_content")); size == 0 || size > maxQuoteMessageContentBytes {
					continue
				}
				boundedRows = append(boundedRows, row)
			}
			boundedRows = decodeFields(boundedRows, "message_content")
			decodedRows := boundedRows[:0]
			for _, row := range boundedRows {
				if content := rowString(row, "message_content"); len(content) == 0 || len(content) > maxQuoteMessageContentBytes {
					continue
				}
				row["talker"] = room
				decodedRows = append(decodedRows, row)
			}
			mergeNicknameCandidates(allCandidates, quoteGroupNicknameCandidatesFromContent(decodedRows))
		}
		closeMsgDBs(shards)
	}

	names := candidateNames(allCandidates)
	for room, members := range names {
		for member := range members {
			if !needed[room][member] {
				delete(members, member)
			}
		}
		if len(members) == 0 {
			delete(names, room)
		}
	}
	return names
}
