# Wetrace 命令参考

入口：`scripts/wetrace_api.py`

所有命令都要求 `WETRACE_OFFLINE_DB_ROOT` 指向带
`.wetrace-offline-copy.json` 完成标记的离线账号目录。Wetrace 不读取外部
`WECHAT_CLI_DB_ROOT` 指定的在线目录。

若目录存在 `.wetrace-offline-copy.updating.json`，表示滚动副本正在更新或上次更新
未完成，所有查询都会拒绝运行。退出微信并重新执行离线副本的 `-Incremental` 更新命令。

## 查询

- `doctor`
- `sessions [--keyword] [--type-filter private|group|private,group] [--limit] [--offset]`
- `contacts [--keyword] [--limit] [--offset]`
- `messages --talker NAME [--time-range RANGE] [--sender NAME] [--keyword TEXT] [--kind KIND]`
- `search --keyword TEXT [--talker NAME] [--sender NAME] [--kind KIND] [--time-range RANGE]`
- `search-context --talker NAME --local-id ID [--before N] [--after N]`
- `chatrooms [--keyword]`
- `chatroom ID`
- `need-contact [--days N]`

## 单会话分析

```text
analysis TYPE SESSION [--time-range RANGE] [--max-messages N]
```

TYPE：

- `summary`
- `hourly`
- `daily`
- `weekday`
- `monthly`
- `type`
- `member`
- `repeat`
- `wordcloud`

## 跨会话分析

```text
analysis top_contacts [--time-range RANGE] --session-limit N --max-messages-per-chat N
analysis annual [--year YYYY | --time-range RANGE] --session-limit N --max-messages-per-chat N
```

## 输出

```text
dashboard --talker NAME [--time-range RANGE] --html [PATH]
export chat --talker NAME [--time-range RANGE] --format html|csv|json|txt
```

通用结果字段：

- `source`
- `scope`
- `warnings`
- `messages` / `sessions` / `data`

跨会话范围字段：

- `session_limit`
- `scanned_sessions`
- `sessions_truncated`
- `max_messages_per_chat`
- `truncated_chat_count`
- `failed_chat_count`
- `total_messages_scanned`
- `truncated`

当 `scope.truncated=true` 时，不得声称结果为账号全量统计。
