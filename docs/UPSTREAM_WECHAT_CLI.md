# wechat-cli

面向人和 AI agent 的本机微信数据 CLI。

它读取 macOS / Windows 上 WeChat 4.x 的本地数据库，将聊天、联系人、群聊、媒体、朋友圈、收藏、转账与红包整理为稳定 JSON。消息正文默认实时读取，不上传、不发送消息，也不控制微信界面。

> 这是本地数据工具，不是微信机器人、公众号工具、Mini Program bridge 或 WeCom bot。

## 支持范围

- macOS arm64 + WeChat 4.x
- Windows amd64 + Windows WeChat / Weixin 4.x
- 文本、图片、视频、文件、链接、引用、合并转发、位置、语音
- 会话、联系人、群成员、全文搜索、朋友圈、收藏、转账、红包
- 紧凑 JSON、稳定分页、上下文展开、只读增量观察

微信需要保持登录，并至少打开过一个聊天。

## 安装

### macOS

```bash
curl -fsSL https://github.com/r266-tech/wechat-cli/releases/latest/download/install-release.sh | zsh
~/.local/share/wechat-cli/wxkey bootstrap
wechat-cli agent --pretty
```

首次执行 `wxkey bootstrap` 可能会退出并重新打开微信，并通过本机隐藏窗口请求管理员密码。为支持后续无人值守刷新，当前实现会将经 sudo 验证的密码存入当前用户 Keychain；密码不要粘贴给 agent、网页或终端日志。不接受这一取舍时请取消引导。安装完成后，建议在 **系统设置 → 隐私与安全性 → 完全磁盘访问权限** 中加入：

- `~/.local/share/wechat-cli/wechat-cli`
- `~/.local/share/wechat-cli/wxkey`

### Windows

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://github.com/r266-tech/wechat-cli/releases/latest/download/install-release.ps1 | iex"
wechat-cli cache refresh --force
wechat-cli agent --pretty
```

Windows 首次刷新时请保持微信登录，并打开一个普通聊天。

默认只安装 CLI，不注册外部 agent 协议，也不安装后台 watcher。安装位置：

- macOS：`~/.local/share/wechat-cli`
- Windows：`%LOCALAPPDATA%\wechat-cli`

## Agent 快速开始

先检查环境，不要先手动刷新缓存：

```bash
wechat-cli agent --pretty
wechat-cli status --pretty
wechat-cli tools
```

正常阅读从 `sessions → resolve-chat → timeline` 开始：

```bash
wechat-cli sessions --type-filter private,group --limit 20
wechat-cli resolve-chat "聊天名"
wechat-cli timeline "聊天名" --limit 50
```

`timeline` 默认查询最新消息，再按时间正序展示。继续翻旧消息时，优先使用 `data.query.cursor.next_before_message`：

```bash
wechat-cli timeline "聊天名" --before-message 123 --limit 50
```

搜索后展开上下文：

```bash
wechat-cli search "关键词" --in "聊天名" --limit 10
wechat-cli context "聊天名" --local-id 123 --before-count 10 --after-count 10
wechat-cli search-context "关键词" --in "聊天名" --context-limit 3
```

增量观察不会发消息，也不会控制微信 UI：

```bash
wechat-cli tail "聊天名" --since-local-id 123
wechat-cli tail "聊天名" --cursor local_id:456 --jsonl
wechat-cli watch --mode sessions --jsonl --follow
```

每次调用都应检查：

- `ok`：调用是否成功
- `data.query.has_more` 与 cursor：是否需要继续分页
- `data.freshness`：数据来源与完整性
- `data.warnings` / `error`：缓存滞后、局部缺 key、媒体补齐失败等诊断

## 输出契约

stdout 默认是一行紧凑 JSON。人工查看加 `--pretty`。

成功：

```json
{"ok":true,"tool":"chat_timeline","command":"timeline","data":{"query":{},"freshness":{},"messages":[]}}
```

失败：

```json
{"ok":false,"error":{"code":"tool_error","message":"...","next_action":"..."}}
```

列表字段即使为空也保持为数组，适合 agent 稳定解析。只有 `tail/watch --jsonl` 与 `--follow` 输出 JSONL 事件流，不套标准 envelope。

通用调用接口：

```bash
wechat-cli tool-schema timeline
wechat-cli tools --profile all
wechat-cli call timeline --chat "聊天名" --limit 20
jq -n --arg chat "聊天名" '{chat:$chat,limit:20}' | wechat-cli call-json timeline
```

默认 schema 只显示高信噪比 canonical 参数；兼容 alias、维护与 debug 参数在 `--profile all` 中。

## 常用命令

| 命令 | 用途 |
| --- | --- |
| `agent` / `status` | 能力矩阵、本机 readiness、恢复动作 |
| `tools` / `tool-schema` | agent 工具面与参数 schema |
| `sessions` / `unread` | 最近会话与未读状态 |
| `resolve-chat` | 将昵称、备注或群名解析为稳定 talker |
| `timeline` | 阅读聊天的默认入口 |
| `context` | 以消息 ID 展开前后文 |
| `search` / `search-context` | 本地全文搜索与上下文展开 |
| `tail` / `watch` | 只读增量消息、会话事件 |
| `media` | 图片、视频、文件的可读本机路径 |
| `members` | 群成员与群名片 |
| `favorites` | 微信收藏 |
| `sns-feed` / `sns-search` | 朋友圈时间线与搜索 |
| `transfers` / `red-packets` | 转账与红包记录 |
| `export` | 显式导出单个聊天到 JSONL / Markdown / HTML |
| `schema` / `sql` | 只读数据库诊断 |
| `cache status` | 元数据缓存诊断 |
| `update` | 更新到最新 GitHub release |

典型消息行：

```json
{
  "id": {"local_id": 123, "server_id_str": "9876543210", "talker": "xxx@chatroom"},
  "time_iso": "2026-05-26T13:00:00+08:00",
  "sender": "Room Nickname",
  "sender_wxid": "wxid_alice",
  "sender_group_nickname": "Room Nickname",
  "sender_contact_display": "Alice",
  "is_from_me": false,
  "kind": "image",
  "text": "[图片]",
  "images": [{"path": "/Users/me/.wechat-cli/media-cache/xxx.jpg"}]
}
```

群消息优先使用可可靠解析的群内昵称作为 `sender`。`sender_wxid` 保留稳定身份，
`sender_contact_display` 与 `sender_group_nickname` 仅在对应信息可可靠解析时返回。

默认只返回人能在微信里看到、且 agent 可直接使用的信息。raw XML、CDN key、协议码、不可读 `.dat` 与候选路径只在 debug/full 输出中出现。

## 严格只读

普通读取不会修改微信数据库，但可能刷新本地 metadata/key、解码图片或缓存语音转写。需要连这些辅助写入也禁用时：

```bash
wechat-cli --strict-read-only timeline "聊天名" --limit 20
WECHAT_CLI_STRICT_READ_ONLY=1 wechat-cli agent --pretty
```

严格只读会禁用：

- metadata / key 自动刷新
- 图片解码与语音转写缓存
- `cache refresh/rebuild`
- `export`

## 图片与语音

图片、视频和文件默认返回可直接读取的本机 `path`。本地 `.dat` 图片会尽力解码到 `~/.wechat-cli/media-cache`；失败时返回 warning，不把不可读文件伪装成图片。

语音转写是可选能力：

```bash
wechat-cli asr setup --model large-v3
wechat-cli asr status --pretty
```

它会创建 `~/.wechat-cli/asr-venv`，安装 `faster-whisper` 与 `silk-python`。模型、语言、device 与 compute type 会持久化到本机 ASR 配置。首次模型下载可能占用数 GB；只安装依赖可加 `--skip-model-download`。

## 微信助手

```bash
wechat-cli companion
```

`companion` 默认只监听 `127.0.0.1:18789`，启动时生成一次随机 bearer token，并通过 URL fragment 交给自动打开的本机窗口；未认证首页不包含 token。

远程监听必须显式传 `--allow-remote`，并自行提供 TLS 或 SSH tunnel。不要把远程 Companion 直接暴露到公网。它是否调用云端模型由后端 CPU 决定；CLI 本身不保存模型密钥。

## 更新与清理

```bash
wechat-cli update
wechat-cli update --dry-run
```

更新器下载 latest release，校验 sha256，并保留当前自定义安装目录。

清理前先 dry-run：

```bash
./install.sh --clear-state --dry-run --json
./install.sh --uninstall --purge-state --dry-run --json
```

Windows：

```powershell
.\install.ps1 -ClearState -DryRun -Json
.\install.ps1 -Uninstall -PurgeState -DryRun -Json
```

危险目录会被拒绝；卸载只删除受管理的安装目录。`--purge-state` 会额外清除本机 key/config/cache/log 与托管凭据。

## 排障

| 现象 | 处理 |
| --- | --- |
| `readiness=degraded` | 查看 `status.data.status.warnings`；缓存滞后不等于聊天正文滞后 |
| 名字解析失败或有重名 | 先运行 `resolve-chat`，再把返回的 raw username 传给 `--chat/--talker` |
| 某个聊天缺 key | 在微信中打开对应聊天，随后由 agent 重跑 `wxkey bootstrap` / `doctor` |
| macOS 频繁弹隐私授权 | 给安装目录中的 `wechat-cli` 与 `wxkey` 加 Full Disk Access |
| 图片只有 warning | 在微信中打开原图后重试，并检查 image-key 诊断 |
| Windows 首次刷新失败 | 确认微信登录，且 `WECHAT_CLI_DB_ROOT` 直接包含 `db_storage` |
| `zsh: killed`，连 `--help` 都失败 | 重新运行 release bootstrap；旧二进制可能无法自更新 |

更完整的 Windows 说明见 [docs/WINDOWS_USER_GUIDE.md](docs/WINDOWS_USER_GUIDE.md)。安全问题请按 [SECURITY.md](SECURITY.md) 私下报告，不要在公开 issue 附上 key、数据库、聊天导出或日志。

## 开发

```bash
go test ./...
go vet ./...
go test -race ./...
```

发布包必须从与 `appVersion` 一致的干净 tag 构建；打包脚本会校验版本、架构、WCDB 导出与产物身份。

许可证与第三方组件见 [LICENSE](LICENSE) 与 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
