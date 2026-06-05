# wechat-cli

本机微信数据 CLI。给强 agent 用，也给人直接用。

macOS / Windows · 本地解密 · 一行安装 · 稳定 JSON · 聊天记录 / 搜索 / 图片 / 文件 / 语音转写 / 朋友圈 / 转账红包

`wechat-cli` 读取你电脑上的 WeChat / 微信 4.x 本地数据库，把消息、联系人、群聊、媒体、朋友圈、收藏、转账、红包等数据输出成结构化 JSON。数据默认留在本机，不上传到云端。

它不是微信机器人，不控制屏幕，不发消息，不自动点赞评论，也不是公众号或小程序工具。

## 安装

macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh | zsh
~/.local/share/wechat-cli/wxkey bootstrap
~/.local/bin/wechat-cli sessions --limit 5 --pretty
```

Windows:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.ps1 | iex"
wechat-cli sessions --limit 5 --pretty
```

第三行是安装成功测试：如果返回 `ok: true` 并看到最近会话，说明 CLI、key、数据库读取都通了。默认安装的是 CLI，不注册外部协议适配器，不装后台 watcher。安装完成后命令会放到用户 PATH 上：

- macOS: `~/.local/bin/wechat-cli`
- Windows: `%LOCALAPPDATA%\Microsoft\WindowsApps\wechat-cli.cmd`，如该目录不存在则使用 `%USERPROFILE%\.local\bin\wechat-cli.cmd`

读取微信数据前请确认：

- macOS arm64 + WeChat 4.x，或 Windows amd64 + Windows WeChat / Weixin 4.x
- 微信已登录，并至少打开过一个聊天
- macOS 15+ 建议安装后把 `~/.local/share/wechat-cli/wechat-cli` 和 `~/.local/share/wechat-cli/wxkey` 加到 Full Disk Access，减少系统隐私弹窗

macOS 的 `wxkey bootstrap` 是首次 key 初始化，可能要求输入一次 Mac admin 密码；密码只输入到本机隐藏提示，不要发给 agent 或网页。密码会存入用户 Keychain，供后续本机 key refresh 使用；`wxkey bootstrap` 也可能临时启动一个 wechat-cli 管理的 WeChat shadow copy 来完成 no-SIP 初始化。质量优先，不要因为这一步跑了一两分钟就手动中断。

WeChat 4.1.10+ 上，`wxkey bootstrap` 可能先打印 passive scan 的 `found=0`，然后进入 `PBKDF breakpoint fallback`。这是预期路径，不等于失败；以最后是否出现 `[OK] key config written` 为准。新版 fallback 默认最多等待 5 分钟，并会在进入 LLDB 前停掉已有 WeChat，让 LLDB 拉起的 WeChat 成为唯一解密实例。质量优先，不要在被拉起的 WeChat 还在登录或打开聊天时手动中断。如果提示 `partial key coverage (24/26)` 这类覆盖率不足，表示当前已打开/核心数据库可用，但还有少数 DB 没拿到 key；先跑 `wechat-cli sessions --limit 5 --pretty` 验证，只有在某些页面或媒体读不到时，再打开对应微信页面后重跑 `~/.local/share/wechat-cli/wxkey bootstrap` 或 `~/.local/share/wechat-cli/wxkey doctor`。

## 更新

安装过 release 版后，直接运行：

```bash
wechat-cli update
```

这个命令会下载 GitHub latest release zip、校验 sha256，然后用包内 installer 覆盖安装。macOS 会等待更新完成并返回 JSON；Windows 会先启动后台 updater 再退出，因为 Windows 不能覆盖正在运行的 `.exe`，返回 JSON 里的 `data.log` 是后台更新日志。agent 更新完后跑一次：

```bash
wechat-cli sessions
```

老版本还没有 `update` 命令时，重新运行上面的安装一行命令也会覆盖升级；agent 场景可在 macOS 上用 `curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh | zsh -s -- --update`。

## 快速开始

```bash
wechat-cli agent --pretty
wechat-cli status --pretty
wechat-cli sessions
wechat-cli resolve-chat "$CHAT"
wechat-cli timeline "$CHAT" --limit 20
wechat-cli context "$CHAT" --local-id 123 --before-count 20 --after-count 20
wechat-cli tail "$CHAT" --since-local-id 123 --jsonl
wechat-cli search-context "$KEYWORD" --in "$CHAT" --context-limit 3
wechat-cli history "$CHAT" --view agent --limit 50
wechat-cli search "$KEYWORD" --in "$CHAT"
wechat-cli media "$CHAT" --type image --limit 10
```

所有命令面向 agent，stdout 默认输出紧凑 JSON；成功统一返回
`{"ok":true,"tool":"...","command":"...","data":...}`，失败返回
`{"ok":false,"error":...}`。`--json` 可传但只是兼容 no-op，人工查看时用
`--pretty`。例外是 `tail/watch --jsonl` 或 `--follow`：它们输出 newline-delimited event JSON，适合长驻小助手循环。常用命令是薄封装，完整能力都可以通过通用调用访问：

```bash
wechat-cli timeline "$CHAT" --limit 20 --pretty
wechat-cli timeline --help
wechat-cli tool-schema chat_timeline
wechat-cli tools
wechat-cli tools --profile all
wechat-cli call chat_timeline --chat "$CHAT" --limit 20
wechat-cli call-json messages '{"chat":"$CHAT","limit":50,"view":"agent"}'
printf '{"keyword":"$KEYWORD","limit":20}' | wechat-cli call-json search
```

`timeline --help` / `tool-schema <command-or-tool>` 也返回同一个成功 envelope，
`data.agent` 里包含 agent 示例、分页策略和常见恢复动作。默认 schema 是 assistant
瘦身版，只显示 canonical 参数；历史 alias/raw/debug 字段用
`wechat-cli tool-schema timeline --profile all` 或 `wechat-cli tools --profile all`
查看。默认 `images[]` 只暴露
一个最佳可读本地路径：有原图/高清图就返回原图/高清图，只有拿不到时才回落到缩略图。
`export` 默认 `view=agent`，JSONL 每行与 timeline message 行同形；需要底层字段时传
`--view raw`。合并转发里的图片会尽量解析到 `forward_chat.items[].images[].path`；
只有拿不到来源资源或本地文件不可读时才保留 `forward_image_not_resolved`。

严格只读任务可加全局开关：

```bash
wechat-cli --strict-read-only timeline "$CHAT" --limit 20
WECHAT_CLI_STRICT_READ_ONLY=1 wechat-cli agent --pretty
```

strict 模式只读已有数据库/已有文件；不会自动刷新 metadata/key，不生成媒体解码 cache
或语音转写 cache，也会拒绝 `cache refresh/rebuild` 和 `export` 这类本地写命令。

`freshness` 是返回数据的新鲜度/诊断信息：例如是否触发过 metadata 自动刷新、分页是否还有下一页、结果是否可能受缺 key 或 cache 滞后影响。`status` / `agent` 会直接给出 `readiness=ready|degraded|blocked`；如果 metadata cache 可用但已滞后，会返回 `warnings=["metadata_cache_degraded"]` 和具体 stale reason。

`agent` 是 agent 进入微信读取环境的总入口。它返回当前能力矩阵、推荐工作流、质量验收命令和本机 readiness，不会读取大量聊天正文，也不会修改微信数据；`read-os` 仍是兼容别名：

```bash
wechat-cli agent --pretty
wechat-cli status --pretty
wechat-cli coverage --pretty
wechat-cli workflows --pretty
```

搜索或 timeline 拿到某条消息后，用 `context` 自然展开上下文：

```bash
wechat-cli search "$KEYWORD" --in "$CHAT" --limit 5 --pretty
wechat-cli context "$CHAT" --local-id 123 --before-count 20 --after-count 20 --pretty
wechat-cli search-context "$KEYWORD" --in "$CHAT" --context-limit 3 --pretty
wechat-cli timeline "$CHAT" --before-message 123 --limit 20 --pretty
```

`context` 返回的 `messages[]` 与 `timeline` 同形，额外带 `context_role=before/anchor/after`，用于让 agent 从一句话继续向前向后读。`search-context` 是 `search -> context` 的组合入口；`timeline --before-message/--after-message` 则把 `local_id` 当游标翻更旧或更新的消息。

小助手增量观察用 `tail` / `watch`。它仍然是 read-only：不发消息、不控制 UI。传 chat 时返回 message events，`event.message` 与 timeline message 行同形；不传 chat 时返回 session/unread events。普通模式返回标准 envelope；`--jsonl`/`--follow` 输出一行一个 event，不包 envelope。`cursor` 可原样传回下一次调用：

```bash
wechat-cli tail "$CHAT" --since-local-id 123
wechat-cli tail "$CHAT" --since-local-id 123 --jsonl
wechat-cli watch --mode sessions --cursor session:1780560000 --jsonl
wechat-cli watch "$CHAT" --cursor local_id:123 --jsonl --follow
```

## 常用命令

| 命令 | 用途 |
| --- | --- |
| `tools` | 默认列 assistant 高信噪比工具；`--profile all` 列全部兼容/维护工具 |
| `agent` | WeChat Read OS 总入口：覆盖率矩阵、工作流、质量验收、本机状态 |
| `status` / `coverage` / `workflows` | 更短的状态、覆盖率、工作流入口 |
| `update` | 更新到 GitHub latest release |
| `sessions` | 最近会话、未读数、最后消息摘要 |
| `resolve-chat` | 把昵称、备注、群名解析成稳定 talker |
| `timeline` | 普通读聊天的首选入口，返回 `query` / `freshness` / `messages` |
| `context` | 以 `local_id` / `server_id` 为锚点展开前后消息 |
| `tail` / `watch` | read-only 增量事件观察，message events 复用 timeline 行 |
| `history` | 更底层的消息读取，支持时间、类型、sender、分页等过滤 |
| `search` | 走微信本地 FTS 的跨会话全文搜索 |
| `search-context` | 搜索并自动展开每个命中附近上下文 |
| `media` | 按消息定位图片、视频、文件等本机可读资源 |
| `members` | 群成员、群名片、好友关系 |
| `sns-feed` / `sns-search` / `sns-notifications` | 朋友圈时间线、搜索、点赞评论通知 |
| `transfers` / `red-packets` | 转账和红包记录 |
| `favorites` | 微信收藏 |
| `export` | 显式本地文件写入：单个会话导出到 jsonl / markdown / html |
| `schema` / `sql` | 只读数据库结构和 SQL 诊断 |
| `cache status` / `cache refresh` | metadata cache 诊断与刷新 |

典型消息行：

```json
{
  "id": {"local_id": 123, "server_id_str": "9876543210", "talker": "xxx@chatroom"},
  "time_iso": "2026-05-26T13:00:00+08:00",
  "sender": "Alice",
  "sender_wxid": "wxid_xxx",
  "is_from_me": false,
  "kind": "image",
  "text": "[图片]",
  "images": [{"path": "/Users/me/.wechat-cli/media-cache/xxx.jpg"}],
  "warnings": []
}
```

默认输出只给 agent 可用的信息：可读图片/视频/文件路径、链接、引用、转账红包、位置、语音转写等。raw XML、CDN/aeskey、不可读 `.dat`、候选路径和解码细节默认隐藏；维护者需要时再传 `include_debug=true` 或 `fields=full`。

## 数据与隐私

- `wechat-cli` 只读打开微信本地数据库。
- 聊天正文默认 live read，不做全量正文 cache。
- 联系人和会话 metadata cache 位于 `~/.wechat-cli/cache/`，用于名称解析和会话排序。
- key map 位于 `~/.config/wxcli/config.json`。不要把它、微信 DB、聊天导出、截图或日志贴到公开 issue。
- macOS sudo 凭据只保存在本机 Keychain；需要清除时用安装器的 `--clear-state` 或 `--uninstall --purge-state`，不要手工删散落文件。
- `wechat-cli` 不发送消息、不自动转发、不点赞评论、不修改微信数据。

## 排障

| 现象 | 处理 |
| --- | --- |
| 找不到会话 | 先用 `wechat-cli resolve-chat "名字"` 看候选，必要时在微信里打开对应聊天后重试 |
| 提示缺 key | 确认微信已登录并打开过聊天；macOS agent 可跑 `~/.local/share/wechat-cli/wxkey bootstrap` / `~/.local/share/wechat-cli/wxkey doctor` |
| `wxkey bootstrap` 先显示 `found=0` 或 `initial passive scan did not capture DB keys before its deadline`，随后进入 PBKDF fallback | WeChat 4.1.10+ 的正常 fallback 路径；不要在 fallback 运行中中断。最终出现 `[OK] key config written` 且 `wechat-cli sessions --limit 5 --pretty` 返回 `ok: true` 就算成功 |
| `PBKDF fallback got partial key coverage (24/26)` | 不是安装失败；表示已拿到大部分 DB key，核心聊天通常可读。先用 `sessions` 验证；如果后续某些页面/媒体缺 key，打开对应微信页面后重跑 `wxkey bootstrap` 或 `wxkey doctor` |
| `PBKDF fallback found no keys` | 先确认已更新到最新 wechat-cli；新版会在 PBKDF fallback 前停掉已有 WeChat，避免 LLDB 调试 shadow 而原 WeChat 仍占着登录态/DB。若更新后仍看到 `pbkdf_calls=0`，表示 LLDB 拉起的微信没有触发 DB 解密，保持该微信窗口登录并打开一个普通聊天后重跑；`pbkdf_calls>0` 但 `matching_db_salt_calls=0` 通常是 DB root/账号目录不匹配，改用正确的 `--root .../xwechat_files/<wxid>` 或 `WECHAT_CLI_DB_ROOT`；有匹配 salt 但仍没 key，可能是当前微信构建的派生逻辑变化，反馈诊断日志 |
| 首次 key 初始化卡在 key scan | 新版会超时返回 `blocked_by=key_scan_timeout` 或 `blocked_by=key_not_found`，不会无限挂住；保持微信打开、点进目标聊天后重跑 `~/.local/share/wechat-cli/wxkey bootstrap`。如果进入 PBKDF fallback 但机器很慢，可用 `WXKEY_PBKDF_PROBE_TIMEOUT=5m ~/.local/share/wechat-cli/wxkey bootstrap` 明确放宽等待 |
| `zsh: killed ~/.local/bin/wechat-cli --help` 或 `sessions` 启动即被杀 | 更新到最新版。旧版 update 可能在 macOS arm64 上 in-place 覆盖正在运行的 Mach-O，导致后续 code signature invalid；新版安装器改为临时文件 + ad-hoc codesign + 原子替换 |
| macOS 频繁弹隐私授权 | 给 `wechat-cli` 和 `wxkey` 加 Full Disk Access |
| 图片只有 warning 没 path | 微信本地只有 `.dat` 且 image key 仍不可用；打开原图或对应聊天后重试 |
| Windows 初始化失败 | 确认 Windows 微信登录、`WECHAT_CLI_DB_ROOT` 指向直接包含 `db_storage` 的账号目录；极慢机器可设 `WECHAT_CLI_KEY_SCAN_TIMEOUT=5m` 后重试 |

更详细的 agent 操作说明见 [AGENTS.md](AGENTS.md)，模型发现摘要见 [llms.txt](llms.txt)。

## 开发

```bash
go test ./...
go build -trimpath -o wechat-cli ./cmd/wechat-cli
```

真实微信读取验收可用：

```bash
WECHAT_CLI_BIN=./wechat-cli WECHAT_READ_TEST_CHAT="$CHAT" WECHAT_READ_TEST_KEYWORD="$KEYWORD" ./scripts/wechat-read-regression.sh
```

它会依次验证 `agent/status/coverage/workflows -> resolve-chat -> sessions -> timeline -> context -> timeline anchor paging -> tail -> search -> search-context -> manual search context -> media -> members -> export`，并把每一步 JSON 保存到 `0700` 临时目录。该目录包含本地聊天数据，不要上传或分享。

macOS release 包：

```bash
WECHAT_CLI_WCDB_DYLIB=/path/to/libWCDB.dylib ./scripts/package.sh 1.6.12
```

Windows release 包由 GitHub Actions 的 `Windows Release Package` workflow 构建。

## 相关项目

- [wxkey](https://github.com/r266-tech/wxkey): macOS WeChat key bootstrap companion，release 包内已包含，普通用户通常不需要单独安装。
- [jackwener/wx-cli](https://github.com/jackwener/wx-cli): 面向终端/脚本的 WeChat data CLI，命令体验值得参考。
- [joeseesun/wechat-radar](https://github.com/joeseesun/wechat-radar): 基于微信数据的本地情报看板。
- [ylytdeng/wechat-decrypt](https://github.com/ylytdeng/wechat-decrypt): 微信数据库解密与导出工具集。

## License

See [LICENSE](LICENSE).

---

<!-- babata-star-callout-v2 -->
## If this saved you time

Starring the repo helps prioritize which integrations stay maintained. This project is part of [babata](https://github.com/r266-tech).
