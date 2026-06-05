package main

// arg helpers
type props = map[string]any

func strProp(desc string) any  { return map[string]any{"type": "string", "description": desc} }
func intProp(desc string) any  { return map[string]any{"type": "integer", "description": desc} }
func boolProp(desc string) any { return map[string]any{"type": "boolean", "description": desc} }
func intPropBounds(desc string, minimum, maximum int64) any {
	p := map[string]any{"type": "integer", "description": desc}
	if minimum >= 0 {
		p["minimum"] = minimum
	}
	if maximum > 0 {
		p["maximum"] = maximum
	}
	return p
}
func enumStrProp(desc string, values ...string) any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

func jsonSchema(properties props, required []string) any {
	s := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func listedToolDefs() []toolDef {
	out := make([]toolDef, len(toolDefs))
	copy(out, toolDefs)
	return out
}

func listedToolDefsForProfile(profile string) ([]toolDef, bool) {
	if profile == "" {
		profile = "assistant"
	}
	if profile != "assistant" && profile != "maintenance" && profile != "all" {
		return nil, false
	}
	if profile == "all" {
		return listedToolDefs(), true
	}
	out := []toolDef{}
	for _, td := range toolDefs {
		if toolInProfile(td.Name, profile) {
			out = append(out, displayToolDef(td))
		}
	}
	return out, true
}

func displayToolDef(td toolDef) toolDef {
	out := toolDef{
		Name:        td.Name,
		Description: displayToolDescription(td),
		InputSchema: cloneSchemaValue(td.InputSchema),
	}
	hideInputProperties(out.InputSchema, hiddenDisplayProps(td.Name))
	return out
}

func displayToolDescription(td toolDef) string {
	if desc, ok := conciseToolDescriptions[td.Name]; ok {
		return desc
	}
	return td.Description
}

var conciseToolDescriptions = map[string]string{
	"read_os":             "Agent 入口: 返回能力矩阵、推荐工作流、质量验收和本机 readiness; 不读取大量聊天正文。",
	"messages":            "底层单会话消息读取。普通 agent 优先用 chat_timeline; 需要底层过滤或 raw view 时再用。",
	"chat_timeline":       "普通读聊天首选入口: 解析 chat, live 读取消息, 返回 query/freshness/messages, 默认按聊天顺序展示最近窗口。",
	"message_context":     "以 local_id/server_id 为锚点展开前后文; 返回与 timeline 同形的 messages, 附 context_role。",
	"read_events":         "只读增量观察: chat 模式返回 message events, 无 chat 时返回 session/unread events; cursor 可原样续传。",
	"media_resources":     "按 chat/local_id/server_id 定位图片、视频、文件等本机资源; 默认只暴露 agent 可直接读取的 path 或 concise warning。",
	"search":              "跨会话或单会话关键词搜索; 走微信 live FTS, 返回可继续 context 的 talker/local_id。",
	"search_with_context": "关键词搜索并展开命中上下文; 适合直接回答“这件事前后发生了什么”。",
	"export_messages":     "显式本地文件导出; strict read-only 下禁用。普通读取优先 timeline/context。",
	"cache_refresh":       "显式刷新 contacts/sessions metadata cache; strict read-only 下禁用。",
	"cache_rebuild":       "显式重建 metadata cache; strict read-only 下禁用。",
}

func cloneSchemaValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = cloneSchemaValue(v)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = cloneSchemaValue(v)
		}
		return out
	case []string:
		out := make([]string, len(x))
		copy(out, x)
		return out
	default:
		return v
	}
}

func hideInputProperties(schema any, names []string) {
	if len(names) == 0 {
		return
	}
	m, ok := schema.(map[string]any)
	if !ok {
		return
	}
	props, ok := m["properties"].(map[string]any)
	if !ok {
		return
	}
	for _, name := range names {
		delete(props, name)
	}
}

func hiddenDisplayProps(tool string) []string {
	common := []string{"debug", "kind_name", "base_kind"}
	out := append([]string{}, common...)
	out = append(out, hiddenByTool[tool]...)
	return out
}

var messageCursorAliasProps = []string{
	"since_time", "since", "since_message",
	"before_message_local_id", "after_message_local_id",
	"before_local_id", "after_local_id",
	"before_message_id", "after_message_id",
	"before_message_server_id", "after_message_server_id",
	"before_message_server_id_str", "after_message_server_id_str",
}

var hiddenByTool = map[string][]string{
	"read_os": {"include_status"},
	"resolve_chat": {
		"chat", "keyword",
	},
	"messages": append(append([]string{}, messageCursorAliasProps...),
		"view", "fields", "include_images"),
	"chat_timeline": append(append([]string{}, messageCursorAliasProps...),
		"include_images"),
	"message_context": {
		"message_local_id", "around_local_id",
		"message_server_id", "message_server_id_str",
		"around_server_id", "around_server_id_str",
		"before_messages", "after_messages",
	},
	"read_events": {
		"since", "after", "jsonl", "follow", "poll_interval",
	},
	"media_resources": {
		"message_server_id", "message_server_id_str",
		"resource_family", "resource_type_raw",
	},
	"search": {
		"search_mode",
	},
	"search_with_context": {
		"search_mode", "before_messages", "after_messages",
	},
	"sessions": {
		"type_filter",
	},
	"contacts": {
		"groups_only", "friends_only",
	},
	"group_members": {
		"stats",
	},
	"export_messages": {
		"kind_name", "base_kind",
	},
}

func toolInProfile(name, profile string) bool {
	switch profile {
	case "assistant":
		switch name {
		case "read_os", "sessions", "resolve_chat", "contacts", "messages", "chat_timeline",
			"message_context", "read_events", "media_resources", "group_members", "unread",
			"stats", "favorites", "red_packets", "transfers", "sns_feed", "sns_search",
			"sns_notifications", "search", "search_with_context", "chatroom_announcements",
			"forward_history":
			return true
		}
	case "maintenance":
		switch name {
		case "cache_status", "cache_refresh", "cache_rebuild", "export_messages", "schema", "sql":
			return true
		}
	}
	return false
}

var toolDefs = []toolDef{
	{
		Name: "read_os",
		Description: "WeChat Read OS 总入口: 返回只读能力地图、覆盖率矩阵、推荐工作流、质量验收命令和本机读取状态. " +
			"这是 agent 进入微信本地读取环境的第一入口; 不读取大量聊天正文, 不触发 key setup, 不修改微信数据. " +
			"mode=overview 默认返回 status/entrypoints/workflows/coverage/quality_gates; coverage 只返回类型覆盖率; workflows 只返回入口和读法; status 只返回本机 readiness.",
		InputSchema: jsonSchema(props{
			"mode":           enumStrProp("overview (默认) / coverage / workflows / status", "overview", "coverage", "workflows", "status"),
			"include_status": boolProp("overview 时是否包含本机状态 (默认 true)"),
			"include_debug":  boolProp("是否暴露 db_root/config_path/wcdb_path 等本机诊断路径 (默认 false)"),
			"debug":          boolProp("include_debug 的别名"),
		}, nil),
	},
	{
		Name: "sessions",
		Description: "聊天会话列表, 按 sort_timestamp DESC. " +
			"字段: username / display_name / chat_type (private/group/official_account/folded/bot/...) / unread_count / summary (末条预览) / " +
			"sort_timestamp (含置顶调整, 用于排序) / last_timestamp (最新消息实际时间, 多数情况两者相等) / " +
			"last_sender_wxid / last_sender_display_name / " +
			"last_msg_type (base_kind raw int) / last_msg_sub_type (subtype raw int) / " +
			"last_msg_kind_name (resolved: text/image/voice/card/video/sticker/location/voip/system, " +
			"app 子类 link/file/music/solitaire/quote/transfer/red_packet/miniprogram/forward_chat/announcement/pat/channel_video). " +
			"type_filter 支持 all/private(friend)/group/official_account(official)/folded/bot, 可逗号分隔. keyword 匹配 username / summary / " +
			"display_name / nick_name / remark / alias (大小写无关, 空格无关).",
		InputSchema: jsonSchema(props{
			"limit":       intProp("返回条数 (默认 50)"),
			"type_filter": strProp("all (默认) / private / group / official_account / folded / bot, 可逗号分隔"),
			"keyword":     strProp("模糊搜索"),
		}, nil),
	},
	{
		Name: "resolve_chat",
		Description: "把昵称/备注/alias/群名/微信号解析成 wechat-cli 可用的 username/talker. " +
			"当 agent 只知道人名或群名时先调这个; 返回 candidates 按精确匹配和最近会话排序.",
		InputSchema: jsonSchema(props{
			"query":       strProp("要解析的人名/群名/微信号"),
			"chat":        strProp("query 的别名"),
			"keyword":     strProp("query 的别名"),
			"type_filter": strProp("可选: private / group / official_account / folded / bot, 可逗号分隔"),
			"limit":       intProp("候选数量 (默认 10)"),
		}, nil),
	},
	{
		Name: "contacts",
		Description: "搜索微信联系人或群. 不传 keyword 则列出全部. " +
			"字段: username / display_name (remark > nick_name > username) / nick_name / " +
			"remark (omitempty) / alias (omitempty, 微信号) / description (omitempty, 个性签名/群简介) / " +
			"type (friend/group/official_account/corp_im/clawbot/stranger/other, 由 username 规则推导) / chat_type / " +
			"is_verified (bool, 公众号/服务号/认证账号).",
		InputSchema: jsonSchema(props{
			"keyword":      strProp("模糊搜索 (匹配 wxid/昵称/备注/alias/拼音首字母)"),
			"limit":        intProp("返回条数 (默认 50)"),
			"groups_only":  boolProp("仅返回群"),
			"friends_only": boolProp("仅返回好友 (排除群和公众号)"),
		}, nil),
	},
	{
		Name: "messages",
		Description: "会话消息, 默认直接读取实时微信消息 DB, 不缓存聊天正文. talker 可传 wxid/xxx@chatroom; chat 可传昵称/备注/群名让 wechat-cli 用 metadata cache 自动解析. " +
			"view=agent 返回给 agent 直接消费的 query/freshness/messages envelope; query 含 returned/limit/offset/has_more/next_offset, 用于可靠分页爬全量. messages[] 是低噪声 timeline 行: id(local_id/server_id_str/talker) / time / create_time(unix秒) / time_iso / sender / sender_wxid / is_from_me / kind / text / warnings, " +
			"并为非文本消息提供 display-ready 结构: images / videos / files / link / music / miniprogram / forward_chat / quote / transfer / red_packet / location / card / voice / video / sticker / solitaire / announcement / pat. " +
			"默认遵循微信 UI 可见语义: 图片/视频/文件给 agent 可直接读取的本机 path, 语音默认优先用 faster-whisper large-v3 返回本地 ASR transcript, raw SILK、不可读 .dat、CDN/aeskey、协议码和 raw XML 下沉到 debug/full/media_resources; 引用消息会扁平到 quote 并复用原消息可见 payload; 合并转发 item 使用 source_id 统一关联原消息, 媒体无法解析时给明确 warnings; 链接直接给 title/url/source/thumb_url. " +
			"fields=lite (默认) 返回: local_id / server_id / server_id_str / create_time / create_time_human / " +
			"talker / talker_display_name / chat_type / sender_wxid / sender_display_name / is_from_me / base_kind / kind_name / content_summary " +
			"/ id / display / display-ready 非文本结构 / warnings (群聊已剥 'wxid:\\n' 前缀). " +
			"正常 agent 查询不需要 fields=full; 默认隐藏 media_resources/media_read_hints/CDN/aeskey/.dat 解码细节. 维护者诊断时才传 include_debug=true/debug=true 或 fields=full. 可传 include_media_paths=false 跳过媒体路径补齐. " +
			"若消息 XML 或引用消息(refermsg)里的真实图片 md5 能匹配本机 temp 里的 PNG/JPG 副本, media_read_hints 会优先给 direct_readable_local_paths 供 agent 直接读图; 引用图片带 source=message_refermsg / message_role=referenced_message. " +
			"图片 .dat 会 best-effort 解码到 ~/.wechat-cli/media-cache 并返回 decoded_media_local_paths / decoded_local_paths; 微信 V4 图片缺 image_key 或 image_key 失效时会先自动跑 wxkey image-key 刷新并重试, 仍失败才在 agent view 给 concise warning, debug/full 返回 decode_status=needs_image_key 和刷新诊断. " +
			"fields=full 是调试兼容接口, 额外返回: subtype / message_content (raw 文本/XML) / " +
			"message_content_parsed (图/表情/app/语音 XML 结构化, 引用递归 depth=5). " +
			"forward_chat (subtype=19) 的 parsed 额外含 forward_items[] (每条: datatype/sourcename/sourcetime/datatitle/datadesc/datafmt/fullmd5/datasize/src_msg_localid); " +
			"datatype 1=text/2=image/3=voice/4=video/5=link/6=location/8=file/17=nested-forward/18=miniprogram (文本走 datadesc, 文件走 datatitle+fullmd5; 嵌套走 nested_items[] 递归 depth=5; agent view 直接递归输出 items). " +
			"base_kind: 1=text/3=image/34=voice/42=card/43=video/47=sticker/48=location/49=app/50=voip/10000=system. " +
			"kind_name 在 base_kind=49 时按 subtype 细化: 5=link/6=file/19=forward_chat/33,36=miniprogram/" +
			"53=solitaire/57=quote/87=announcement/2000=transfer/2001=red_packet/62=pat/51=channel_video/3,76=music. " +
			"after/before 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"talker":                       strProp("会话对象 (wxid 或 xxx@chatroom)"),
			"chat":                         strProp("会话显示名/备注/alias/群名; talker 为空时自动解析"),
			"limit":                        intProp("返回条数 (默认 50)"),
			"offset":                       intProp("跳过条数 (默认 0)"),
			"after":                        strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"since_time":                   strProp("after 的小助手别名: 从这个本地时间之后读"),
			"since":                        strProp("since_time 的别名"),
			"before":                       strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before_message":               intProp("锚点 local_id; 返回该消息之前更旧的消息"),
			"after_message":                intProp("锚点 local_id; 返回该消息之后更新的消息"),
			"since_local_id":               intProp("after_message 的小助手别名: 从这个 local_id 之后读"),
			"since_message":                intProp("since_local_id 的别名"),
			"before_message_local_id":      intProp("before_message 的别名"),
			"after_message_local_id":       intProp("after_message 的别名"),
			"before_local_id":              intProp("before_message 的别名"),
			"after_local_id":               intProp("after_message 的别名"),
			"before_message_id":            intProp("before_message 的别名"),
			"after_message_id":             intProp("after_message 的别名"),
			"before_server_id":             intProp("锚点 server_id; 返回该消息之前更旧的消息"),
			"after_server_id":              intProp("锚点 server_id; 返回该消息之后更新的消息"),
			"before_server_id_str":         strProp("before_server_id 字符串形式, 避免 64-bit JSON 精度损失"),
			"after_server_id_str":          strProp("after_server_id 字符串形式, 避免 64-bit JSON 精度损失"),
			"before_message_server_id":     intProp("before_server_id 的别名"),
			"after_message_server_id":      intProp("after_server_id 的别名"),
			"before_message_server_id_str": strProp("before_server_id_str 的别名"),
			"after_message_server_id_str":  strProp("after_server_id_str 的别名"),
			"keyword":                      strProp("消息内容关键词"),
			"type":                         strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name":                    strProp("可选: 同 type"),
			"base_kind":                    intProp("可选: base_kind raw int"),
			"sender":                       strProp("可选: sender wxid/昵称; 可传 me/self 表示自己"),
			"from_me":                      boolProp("仅返回自己发出的消息; 等价 sender=me"),
			"view":                         enumStrProp("返回视图: default 保持原 fields 输出; agent 返回低噪声扁平 timeline", "default", "agent"),
			"order":                        enumStrProp("查询顺序: desc 最近消息优先 (默认) / asc 最早消息优先", "desc", "asc"),
			"display_order":                enumStrProp("输出展示顺序: query 保持查询顺序 (默认) / desc / asc; 用 order=desc + display_order=asc 展示最近 N 条的聊天顺序", "query", "desc", "asc"),
			"fields":                       enumStrProp("lite (默认) / full", "lite", "full"),
			"include_media_paths":          boolProp("是否补齐图片/视频/文件本机资源路径和 display-ready media refs (默认 true; 传 false 可关闭)"),
			"include_debug":                boolProp("是否在 lite/agent 输出中包含调试媒体字段或 debug 节点 (默认 false)"),
			"debug":                        boolProp("include_debug 的别名"),
		}, nil),
	},
	{
		Name:        "chat_timeline",
		Description: "面向 agent 展示/总结的高层聊天时间线工具, 是普通查消息的首选入口. 自动解析 chat, live 读取最近消息, 默认 order=desc + display_order=asc 展示最近窗口的聊天顺序. 返回对象包含 query / freshness / messages; query 含 returned/limit/offset/has_more/next_offset 便于可靠分页爬全量; messages 是低噪声 agent 行, 每条有稳定 id、time/create_time/time_iso、sender_wxid/is_from_me、display-ready 非文本结构和轻量 warnings, 默认隐藏调试噪音.",
		InputSchema: jsonSchema(props{
			"talker":                       strProp("会话对象 (wxid 或 xxx@chatroom)"),
			"chat":                         strProp("会话显示名/备注/alias/群名; talker 为空时自动解析"),
			"limit":                        intProp("返回条数 (默认 50)"),
			"offset":                       intProp("跳过条数 (默认 0)"),
			"after":                        strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"since_time":                   strProp("after 的小助手别名: 从这个本地时间之后读"),
			"since":                        strProp("since_time 的别名"),
			"before":                       strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before_message":               intProp("锚点 local_id; 返回该消息之前更旧的消息"),
			"after_message":                intProp("锚点 local_id; 返回该消息之后更新的消息"),
			"since_local_id":               intProp("after_message 的小助手别名: 从这个 local_id 之后读"),
			"since_message":                intProp("since_local_id 的别名"),
			"before_message_local_id":      intProp("before_message 的别名"),
			"after_message_local_id":       intProp("after_message 的别名"),
			"before_local_id":              intProp("before_message 的别名"),
			"after_local_id":               intProp("after_message 的别名"),
			"before_message_id":            intProp("before_message 的别名"),
			"after_message_id":             intProp("after_message 的别名"),
			"before_server_id":             intProp("锚点 server_id; 返回该消息之前更旧的消息"),
			"after_server_id":              intProp("锚点 server_id; 返回该消息之后更新的消息"),
			"before_server_id_str":         strProp("before_server_id 字符串形式, 避免 64-bit JSON 精度损失"),
			"after_server_id_str":          strProp("after_server_id 字符串形式, 避免 64-bit JSON 精度损失"),
			"before_message_server_id":     intProp("before_server_id 的别名"),
			"after_message_server_id":      intProp("after_server_id 的别名"),
			"before_message_server_id_str": strProp("before_server_id_str 的别名"),
			"after_message_server_id_str":  strProp("after_server_id_str 的别名"),
			"keyword":                      strProp("消息内容关键词"),
			"type":                         strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name":                    strProp("可选: 同 type"),
			"base_kind":                    intProp("可选: base_kind raw int"),
			"sender":                       strProp("可选: sender wxid/昵称; 可传 me/self 表示自己"),
			"from_me":                      boolProp("仅返回自己发出的消息; 等价 sender=me"),
			"order":                        enumStrProp("查询顺序: desc 最近消息优先 (默认) / asc 最早消息优先", "desc", "asc"),
			"display_order":                enumStrProp("输出展示顺序: asc 默认聊天顺序 / desc / query", "query", "desc", "asc"),
			"include_images":               boolProp("是否补齐图片/文件路径 (默认 true; false 时等价 include_media_paths=false)"),
			"include_media_paths":          boolProp("是否补齐图片/视频/文件本机资源路径和 display-ready media refs (默认 true)"),
			"include_debug":                boolProp("是否附带 debug 节点 (默认 false)"),
			"debug":                        boolProp("include_debug 的别名"),
		}, nil),
	},
	{
		Name: "message_context",
		Description: "以一条已知消息为锚点向前/向后展开上下文. 这是 search 结果、timeline 某条消息、用户指定 local_id/server_id 后继续读前后文的首选入口. " +
			"输入 chat/talker + local_id 或 server_id/server_id_str; 返回 query/freshness/messages, messages 与 timeline agent 行同形, 额外带 context_role=before/anchor/after. " +
			"默认 before_count=20、after_count=20、include_anchor=true、display_order=asc, 用 sort_seq/local_id 定位, 比只按时间更接近微信 UI 顺序. " +
			"include_media_paths 默认 true; debug/include_debug 仅用于诊断媒体/解析 warning.",
		InputSchema: jsonSchema(props{
			"talker":                strProp("会话对象 (wxid 或 xxx@chatroom)"),
			"chat":                  strProp("会话显示名/备注/alias/群名; talker 为空时自动解析"),
			"local_id":              intProp("锚点消息 local_id"),
			"message_local_id":      intProp("local_id 的别名"),
			"around_local_id":       intProp("local_id 的别名"),
			"server_id":             intProp("锚点消息 server_id"),
			"server_id_str":         strProp("锚点消息 server_id 字符串形式, 避免 64-bit JSON 精度损失"),
			"message_server_id":     intProp("server_id 的别名"),
			"message_server_id_str": strProp("message_server_id 字符串形式"),
			"around_server_id":      intProp("server_id 的别名"),
			"around_server_id_str":  strProp("server_id 字符串形式"),
			"before_count":          intProp("锚点之前返回多少条 (默认 20, 最大 500)"),
			"after_count":           intProp("锚点之后返回多少条 (默认 20, 最大 500)"),
			"before_messages":       intProp("before_count 的别名"),
			"after_messages":        intProp("after_count 的别名"),
			"limit":                 intProp("before_count/after_count 都未传时的共同窗口大小"),
			"include_anchor":        boolProp("是否包含锚点消息 (默认 true)"),
			"display_order":         enumStrProp("输出展示顺序: asc 默认聊天顺序 / desc", "asc", "desc"),
			"include_media_paths":   boolProp("是否补齐图片/视频/文件本机资源路径和 display-ready media refs (默认 true)"),
			"include_debug":         boolProp("是否附带 debug 节点 (默认 false)"),
			"debug":                 boolProp("include_debug 的别名"),
		}, nil),
	},
	{
		Name: "read_events",
		Description: "只读事件观察入口, 用于小助手增量观察微信而不发送、不控制 UI. " +
			"chat/talker 存在时返回 message events, event.message 与 timeline agent row 同形; 不传 chat 时返回 session/unread events. " +
			"cursor 可直接传回下一次调用; since_local_id/since_time 用于首次建立游标. CLI 的 tail/watch 支持 --jsonl 一次性输出事件行, --follow 按 poll_interval 轮询.",
		InputSchema: jsonSchema(props{
			"mode":                enumStrProp("auto (默认) / messages / sessions", "auto", "messages", "sessions"),
			"talker":              strProp("可选: 限定 wxid 或 xxx@chatroom; 存在时观察该会话新消息"),
			"chat":                strProp("可选: 昵称/备注/群名, 自动解析为 talker"),
			"cursor":              strProp("上次返回的 cursor; message 形如 local_id:123, session 形如 session:1780560000"),
			"since_local_id":      intProp("首次观察某会话时的 local_id 游标; 等价 after_message"),
			"since_time":          strProp("首次观察时的起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"since":               strProp("since_time 的别名"),
			"after":               strProp("since_time 的兼容别名"),
			"type":                strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name":           strProp("可选: 同 type"),
			"sender":              strProp("可选: sender wxid/昵称; 可传 me/self 表示自己"),
			"from_me":             boolProp("仅返回自己发出的 message events; 等价 sender=me"),
			"limit":               intPropBounds("返回事件条数 (默认 50, 最大 1000)", 1, 1000),
			"scan_limit":          intPropBounds("mode=sessions 时内部扫描会话条数 (默认等于 limit, 最大 5000)", 1, 5000),
			"include_media_paths": boolProp("message events 是否补齐图片/视频/文件路径 (默认 true)"),
			"include_debug":       boolProp("是否附带 debug 节点 (默认 false)"),
			"debug":               boolProp("include_debug 的别名"),
			"jsonl":               boolProp("CLI-only: 每个 event 输出一行 JSON"),
			"follow":              boolProp("CLI-only: 持续轮询输出 JSONL"),
			"poll_interval":       strProp("CLI-only: follow 轮询间隔, 如 2s; 纯数字按秒"),
		}, nil),
	},
	{
		Name: "media_resources",
		Description: "消息附件/媒体资源定位. 读取 message_resource.db, 按 chat/talker/local_id/server_id/time/sender/type 过滤, " +
			"默认返回 agent-ready 资源: images/videos/files[].path 和 resources[].path 只会是可直接读取的本机图片/视频/文件路径, resources 默认不暴露 resource_id/status/raw family/variant 等维护字段. " +
			"不可读 .dat、重复候选 paths、local_path_details、raw type/variant_code、解码细节和候选路径默认隐藏; 维护者诊断时传 include_debug=true/debug=true 才返回. " +
			"对图片会补查消息 XML 的真实图片 md5, 若本机 temp 存在同 md5 PNG/JPG 副本则优先返回真实 path. 图片 .dat 会 best-effort 解码到 ~/.wechat-cli/media-cache; 微信 V4 图片缺 image_key 或 image_key 失效时会自动跑 wxkey image-key 刷新并重试, 仍失败才给 concise warning, 不把 .dat 当图片路径给 agent. wechat-cli 不做图片识别. " +
			"适合 agent 在 messages/search 拿到 local_id 或 server_id 后继续定位图片、视频、文件和转发记录里的资源. " +
			"after/before 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"talker":                strProp("可选: 限定 wxid 或 xxx@chatroom"),
			"chat":                  strProp("可选: 昵称/备注/群名, 自动解析为 talker"),
			"local_id":              intProp("可选: message local_id"),
			"server_id":             intProp("可选: message server_id"),
			"server_id_str":         strProp("可选: message server_id 字符串形式, 避免 64-bit JSON 精度损失"),
			"message_server_id":     intProp("可选: server_id 的别名, 兼容 red_packets/transfers 输出"),
			"message_server_id_str": strProp("可选: message_server_id 字符串形式"),
			"after":                 strProp("可选: 起始时间"),
			"before":                strProp("可选: 截止时间"),
			"sender":                strProp("可选: sender wxid/昵称; 可传 me/self 表示自己"),
			"from_me":               boolProp("仅返回自己发出的媒体消息; 等价 sender=me"),
			"type":                  strProp("可选: kind_name, 如 image/video/file/forward_chat/miniprogram"),
			"kind_name":             strProp("可选: 同 type"),
			"base_kind":             intProp("可选: base_kind raw int"),
			"resource_family":       strProp("可选: image / video / file / cover / unknown"),
			"resource_type_raw":     intProp("可选: MessageResourceDetail.type raw int"),
			"include_local_paths":   boolProp("是否返回已存在本地文件路径 (默认 true)"),
			"include_debug":         boolProp("是否返回 .dat/local_path_details/raw type/解码细节等调试信息 (默认 false)"),
			"debug":                 boolProp("include_debug 的别名"),
			"limit":                 intProp("返回消息条数 (默认 50)"),
			"offset":                intProp("跳过消息条数 (默认 0)"),
		}, nil),
	},
	{
		Name: "group_members",
		Description: "群成员. 字段: username / display_name / nick_name / " +
			"remark (omitempty) / alias (omitempty) / is_owner (bool) / is_friend (bool). " +
			"stats=true 附 msg_count (扫消息表较慢).",
		InputSchema: jsonSchema(props{
			"chatroom_id": strProp("群 ID (xxx@chatroom)"),
			"chat":        strProp("群名/备注; chatroom_id 为空时自动解析"),
			"stats":       boolProp("附带每人发言条数 (扫消息表, 较慢)"),
			"limit":       intProp("返回条数 (默认 100)"),
			"offset":      intProp("跳过条数 (默认 0)"),
		}, nil),
	},
	{
		Name: "sns",
		Description: "朋友圈 timeline. 返回字段: tid / username / nickname / avatar_url / " +
			"create_time / content / type / private / liked_by_me / " +
			"media (type/sub_type/url/thumb/url_key/thumb_key/md5/width/height/total_size/video_md5/video_duration) / location (name/lat/lon) / " +
			"likes ([username, nickname]) / " +
			"comments ([username, nickname, content, create_time, reply_to, reply_to_nick]). " +
			"时间过滤针对 XML 里的 createTime (非 SQL tid), 先按 tid DESC 粗拉再解析过滤.",
		InputSchema: jsonSchema(props{
			"keyword": strProp("正文关键词"),
			"user":    strProp("按发布者 wxid 过滤"),
			"after":   strProp("起始时间 (unix秒 或 2006-01-02)"),
			"before":  strProp("截止时间 (unix秒 或 2006-01-02)"),
			"limit":   intProp("返回条数 (默认 20)"),
			"offset":  intProp("跳过条数 (默认 0)"),
		}, nil),
	},
	{
		Name:        "sns_feed",
		Description: "朋友圈时间线, 等价于 sns 但语义更明确. 支持 user/keyword/after/before/limit/offset.",
		InputSchema: jsonSchema(props{
			"keyword": strProp("正文关键词"),
			"user":    strProp("按发布者 wxid 过滤"),
			"after":   strProp("起始时间 (unix秒 或 2006-01-02)"),
			"before":  strProp("截止时间 (unix秒 或 2006-01-02)"),
			"limit":   intProp("返回条数 (默认 20)"),
			"offset":  intProp("跳过条数 (默认 0)"),
		}, nil),
	},
	{
		Name:        "sns_search",
		Description: "朋友圈正文全文搜索. 返回字段同 sns_feed, keyword 必填.",
		InputSchema: jsonSchema(props{
			"keyword": strProp("正文关键词"),
			"user":    strProp("按发布者 wxid 过滤"),
			"after":   strProp("起始时间 (unix秒 或 2006-01-02)"),
			"before":  strProp("截止时间 (unix秒 或 2006-01-02)"),
			"limit":   intProp("返回条数 (默认 20)"),
			"offset":  intProp("跳过条数 (默认 0)"),
		}, []string{"keyword"}),
	},
	{
		Name:        "sns_notifications",
		Description: "朋友圈互动通知: 点赞/评论. 默认仅未读; include_read=true 返回已读+未读.",
		InputSchema: jsonSchema(props{
			"include_read": boolProp("包含已读通知"),
			"after":        strProp("起始时间 (unix秒 或 2006-01-02)"),
			"before":       strProp("截止时间 (unix秒 或 2006-01-02)"),
			"limit":        intProp("返回条数 (默认 50)"),
		}, nil),
	},
	{
		Name: "search",
		Description: "跨会话消息全文搜索, 默认直接读取微信 message_fts.db 和 Msg_<hash> 分片, 不缓存聊天正文. metadata cache 只用于 chat/sender 名称解析. " +
			"字段: content (群聊已剥 'wxid:\\n' 前缀) / local_id / talker / talker_display_name / chat_type / " +
			"create_time / sender_wxid / sender_display_name / base_kind / kind_name. " +
			"sender + base_kind/kind_name 来自 join 回所有包含 Msg_<hash>(talker) 的 message shard. " +
			"search_mode=fts/like/auto 保留兼容; 三种模式都使用微信 live FTS, 不做全局 LIKE 扫描.",
		InputSchema: jsonSchema(props{
			"keyword":        strProp("搜索关键词"),
			"talker":         strProp("可选: 限定 wxid 或 xxx@chatroom"),
			"chat":           strProp("可选: 限定昵称/备注/群名, 自动解析为 talker"),
			"after":          strProp("可选: 起始时间"),
			"before":         strProp("可选: 截止时间"),
			"type":           strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name":      strProp("可选: 同 type"),
			"base_kind":      intProp("可选: base_kind raw int"),
			"sender":         strProp("可选: sender wxid/昵称; 可传 me/self 表示自己"),
			"from_me":        boolProp("仅返回自己发出的消息; 等价 sender=me"),
			"offset":         intPropBounds("跳过命中条数 (默认 0)", 0, 1000000),
			"max_text_chars": intPropBounds("裁剪 text/match 字符数; 适合统计任务降低 token", 1, 2000),
			"snippet_only":   boolProp("只返回短片段; 等价 max_text_chars 默认 180"),
			"include_text":   boolProp("是否返回 text/match 字段 (默认 true; false 仅返回元数据)"),
			"search_mode":    enumStrProp("兼容参数: fts (默认) / like / auto; 当前都走微信 live FTS", "fts", "like", "auto"),
			"limit":          intPropBounds("返回条数 (默认 20, 最大 1000)", 1, 1000),
		}, []string{"keyword"}),
	},
	{
		Name: "search_with_context",
		Description: "跨会话关键词搜索并自动展开每个命中附近的上下文. 这是 agent 调查问题的高层入口: 先用微信 live FTS 找命中, 再用 live message DB 按 local_id 展开 before/anchor/after. " +
			"返回 query/freshness/hits; hits[].message 是低噪声搜索命中, hits[].context 是 message_context 同形输出. " +
			"limit 控制搜索命中数 (默认 20, 最大 1000); context_limit 控制前多少个命中展开上下文 (默认 min(limit,5), 最大 20); before_count/after_count 默认 5, 最大 500.",
		InputSchema: jsonSchema(props{
			"keyword":             strProp("搜索关键词"),
			"talker":              strProp("可选: 限定 wxid 或 xxx@chatroom"),
			"chat":                strProp("可选: 限定昵称/备注/群名, 自动解析为 talker"),
			"after":               strProp("可选: 起始时间"),
			"before":              strProp("可选: 截止时间"),
			"type":                strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name":           strProp("可选: 同 type"),
			"base_kind":           intProp("可选: base_kind raw int"),
			"sender":              strProp("可选: sender wxid/昵称; 可传 me/self 表示自己"),
			"from_me":             boolProp("仅返回自己发出的命中; 等价 sender=me"),
			"max_text_chars":      intPropBounds("裁剪命中和上下文 text/match 字符数; 适合降低 token", 1, 2000),
			"snippet_only":        boolProp("命中和上下文只返回短片段; 等价 max_text_chars 默认 180"),
			"include_text":        boolProp("是否返回命中和上下文 text/match 字段 (默认 true; false 仅返回元数据)"),
			"search_mode":         enumStrProp("兼容参数: fts (默认) / like / auto; 当前都走微信 live FTS", "fts", "like", "auto"),
			"limit":               intPropBounds("搜索命中条数 (默认 20, 最大 1000)", 1, 1000),
			"context_limit":       intPropBounds("展开上下文的命中条数 (默认 min(limit,5), 最大 20; 0 表示只返回命中不展开)", 0, 20),
			"before_count":        intPropBounds("每个命中之前返回多少条 (默认 5, 最大 500)", 0, 500),
			"after_count":         intPropBounds("每个命中之后返回多少条 (默认 5, 最大 500)", 0, 500),
			"before_messages":     intPropBounds("before_count 的别名", 0, 500),
			"after_messages":      intPropBounds("after_count 的别名", 0, 500),
			"include_media_paths": boolProp("是否补齐上下文里的图片/视频/文件路径 (默认 true)"),
			"include_debug":       boolProp("是否附带 debug 节点 (默认 false)"),
			"debug":               boolProp("include_debug 的别名"),
		}, []string{"keyword"}),
	},
	{
		Name: "sql",
		Description: "本地 WCDB SQL. OS 级 readonly (SQLITE_OPEN_READONLY 打开), DDL/DML 会 rc≠0 直接报错 — " +
			"SELECT/WITH 默认外层限流; PRAGMA/EXPLAIN 允许直接执行. " +
			"db 位置由 subdir/file 定位. 用 schema tool 列出有哪些 db 和表.",
		InputSchema: jsonSchema(props{
			"query":  strProp("SQL 语句"),
			"subdir": strProp("db_storage 下的子目录 (默认 session)"),
			"file":   strProp("数据库文件名 (默认 session.db)"),
			"limit":  intProp("SELECT/WITH 外层最大返回行数 (默认 200, 最大 1000)"),
		}, []string{"query"}),
	},
	{
		Name: "transfers",
		Description: "微信转账记录. 字段: transfer_id / transcation_id / session_username / session_display_name / " +
			"payer_wxid / payer_display_name / receiver_wxid / receiver_display_name / pay_sub_type (raw int) / " +
			"begin_transfer_time / invalid_time / last_modified_time / message_server_id / " +
			"amount (从 messages join 出, 如 '￥5.00') / description (人类可读, 如 '收到转账5.00元') / memo (转账留言, omitempty). " +
			"amount/description/memo 通过 message_server_id 从所有匹配 Msg_<hash>(session_username) 的 shard 拉 XML 提取. " +
			"after/before 按 begin_transfer_time 过滤, 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"limit":  intProp("返回条数 (默认 50)"),
			"after":  strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before": strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
		}, nil),
	},
	{
		Name: "red_packets",
		Description: "微信红包记录. 字段: send_id / sender_wxid / sender_display_name / " +
			"session_username / session_display_name / native_url (微信红包深链) / message_server_id / " +
			"wishing (祝福语 如 '恭喜发财大吉大利', 从 join XML 提取) / scene_text (如 '微信红包', omitempty). " +
			"红包金额随机, 仅领取后可见, 不在本地数据中. " +
			"不传 after/before 时按 rowid DESC (近似收到顺序); 传时间过滤时 live join 对应 Msg_<hash> 取 create_time.",
		InputSchema: jsonSchema(props{
			"limit":  intProp("返回条数 (默认 50)"),
			"talker": strProp("可选: 限定会话对象"),
			"chat":   strProp("可选: 昵称/备注/群名, 自动解析为 talker"),
			"sender": strProp("可选: sender wxid 或昵称"),
			"after":  strProp("可选: 起始时间"),
			"before": strProp("可选: 截止时间"),
		}, nil),
	},
	{
		Name: "favorites",
		Description: "微信收藏. 字段: server_id / favorite_type (text/image/voice/video/link/location/file/" +
			"chat_history/miniprogram/unknown) / update_time / source_id (内部复合 ID) / " +
			"from_wxid / from_display_name / source_chat_username (omitempty) / source_chat_display_name / " +
			"content (XML 原文) / title (从 XML 提取, omitempty) / description (omitempty) / url (omitempty). " +
			"after/before 按 update_time 过滤, 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"limit":  intProp("返回条数 (默认 50)"),
			"after":  strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before": strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
		}, nil),
	},
	{
		Name: "chatroom_announcements",
		Description: "群公告. 字段: chatroom_id / chatroom_display_name / announcement / " +
			"editor_wxid / editor_display_name / publish_time. " +
			"不传 chatroom_id 按 publish_time DESC 列所有群公告. " +
			"after/before 按 publish_time 过滤, 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"chatroom_id": strProp("群 ID (xxx@chatroom), 不传则返回所有群公告 (按发布时间倒序)"),
			"limit":       intProp("返回条数 (默认 20)"),
			"after":       strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before":      strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
		}, nil),
	},
	{
		Name: "forward_history",
		Description: "最近转发目标列表 (你最近转发到了哪些会话, 用于快捷转发 UI). " +
			"非'被转发的消息历史' — 不存消息内容. 字段: username / display_name / forward_time. " +
			"after/before 按 forward_time 过滤, 接 unix秒 或 2006-01-02 (本地时区).",
		InputSchema: jsonSchema(props{
			"limit":  intProp("返回条数 (默认 50)"),
			"after":  strProp("起始时间 (unix秒 或 2006-01-02, 本地时区)"),
			"before": strProp("截止时间 (unix秒 或 2006-01-02, 本地时区)"),
		}, nil),
	},
	{
		Name: "schema",
		Description: "WCDB 数据库结构. 不传参数列出所有 subdir 下 db 的表名 (分片的 message_*.db 折叠为一条 + shard_count). " +
			"传 subdir+file 返回该 db 每张表的 CREATE TABLE DDL.",
		InputSchema: jsonSchema(props{
			"subdir": strProp("db_storage 下子目录"),
			"file":   strProp("数据库文件名"),
		}, nil),
	},
	{
		Name:        "cache_status",
		Description: "查看 wechat-cli metadata snapshot cache 与统一 index.sqlite 诊断信息. 默认只缓存联系人/会话用于名称解析, 不缓存聊天正文, 不触发 wxkey setup; 不再输出 fresh=true 这种易误解的全局新鲜度结论.",
		InputSchema: jsonSchema(props{}, nil),
	},
	{
		Name: "cache_refresh",
		Description: "刷新 metadata snapshot cache 并重建统一 index.sqlite. 默认只 snapshot contact/contact.db 和 session/session.db; 聊天正文现查. " +
			"background=true 立即返回并在后台刷新, 避免长时间阻塞 agent 运行.",
		InputSchema: jsonSchema(props{
			"force":      boolProp("强制重建所有 plaintext snapshots"),
			"background": boolProp("后台刷新并立即返回"),
		}, nil),
	},
	{
		Name:        "cache_rebuild",
		Description: "删除当前 wechat-cli cache 目录后完整重建 metadata snapshot cache + index.sqlite.",
		InputSchema: jsonSchema(props{}, nil),
	},
	{
		Name:        "unread",
		Description: "未读会话列表. metadata cache-backed; 字段同 sessions, 仅返回 unread_count > 0. type_filter/filter 支持 private,group 等逗号分隔.",
		InputSchema: jsonSchema(props{
			"limit":       intProp("返回条数 (默认 50)"),
			"type_filter": strProp("all/private/group/official_account/folded/bot, 可逗号分隔"),
			"filter":      strProp("type_filter 的别名, 兼容 wx-cli 风格"),
		}, nil),
	},
	{
		Name:        "stats",
		Description: "metadata cache 状态统计. wechat-cli 不缓存聊天正文, 因此只返回 sessions/contacts 计数和提示.",
		InputSchema: jsonSchema(props{}, nil),
	},
	{
		Name: "export_messages",
		Description: "导出单个 chat/talker 的消息到本地文件, 直接读取实时消息 DB; 不支持全局无关键词导出. " +
			"format=jsonl/markdown/html, 默认 view=agent: JSONL 每行与 timeline message 行同形; view=raw 保留底层消息字段. 支持 after/before/keyword/limit 过滤.",
		InputSchema: jsonSchema(props{
			"path":      strProp("输出文件绝对路径"),
			"format":    enumStrProp("jsonl (默认) / markdown / html", "jsonl", "markdown", "html"),
			"view":      enumStrProp("agent (默认, timeline message 行同形) / raw (底层消息字段)", "agent", "raw"),
			"talker":    strProp("可选: 限定会话对象"),
			"chat":      strProp("可选: 昵称/备注/群名, 自动解析为 talker"),
			"after":     strProp("可选: 起始时间"),
			"before":    strProp("可选: 截止时间"),
			"keyword":   strProp("可选: 内容关键词"),
			"type":      strProp("可选: kind_name, 如 text/image/link/file/quote/transfer/red_packet"),
			"kind_name": strProp("可选: 同 type"),
			"base_kind": intProp("可选: base_kind raw int"),
			"sender":    strProp("可选: sender wxid/昵称; 可传 me/self 表示自己"),
			"from_me":   boolProp("仅导出自己发出的消息; 等价 sender=me"),
			"limit":     intProp("最大导出条数 (默认 10000)"),
			"offset":    intProp("跳过条数 (默认 0)"),
		}, []string{"path"}),
	},
}
