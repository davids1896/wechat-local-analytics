# wechat-local-analytics

本地优先、只读的微信聊天记录 CLI 与 Agent 分析工具箱。

本仓库把两个层次放在一起：

- **wechat-cli**：读取本机 WeChat/微信 4.x 数据库，提供会话、消息、联系人、群成员、搜索和媒体元数据等结构化 JSON。
- **Wetrace**：通过 `wechat-cli call-json` 获取数据，完成分页、统计、词频、成员排行、年度分析、导出和独立 HTML 仪表板。

项目不会发送或删除微信消息，不会控制微信界面，也不需要非官方协议登录。数据读取与统计在本机完成；若把结果交给 Codex 或其他 Agent 做语义分析，数据处理边界取决于所使用的 Agent 平台与配置。

> [!IMPORTANT]
> CLI 的数据库访问可以保持只读，但登录、打开聊天或播放媒体的官方桌面微信可能同步手机端已读状态。若未读红点很重要，优先使用离线数据副本，并避免为了取密钥而打开相应会话。

## 功能

### wechat-cli 数据层

- Windows amd64 与 macOS arm64 的 WeChat/微信 4.x 本地数据库读取
- 会话、联系人、群成员、聊天时间线和全文搜索
- 消息上下文、未读会话、收藏、转账、红包和朋友圈数据
- 文本、图片、视频、文件、语音、链接、引用和合并转发等消息类型
- 稳定 JSON、分页游标和适合 Agent 的标准消息结构
- `--strict-read-only` 严格只读模式
- Windows 密钥扫描增强：
  - ASCII raw-key 字面量
  - UTF-16 raw-key 字面量
  - 数据库 salt 相邻的 32 字节二进制候选
  - 候选去重、数量上限和真实数据库逐项验证
  - 详细扫描诊断

### Wetrace 分析层

- 查询会话、联系人、群成员和聊天记录
- 按联系人、群、发送者、时间、关键词和消息类型过滤
- 日、周、月、小时活跃度
- 消息类型分布、群成员发言排行
- 重复短消息和基础词频
- 最近最活跃联系人/群排行
- 年度统计和月度趋势
- JSON、CSV、TXT 和 HTML 导出
- 独立 HTML 仪表板
- Codex skill，可直接用自然语言要求 Agent 分析微信记录
- 每份跨会话结果明确报告会话上限、消息上限、失败会话和截断状态

## 仓库结构

```text
cmd/                         wechat-cli 命令
internal/                    数据库、密钥、媒体和查询实现
skills/wetrace/              Wetrace Codex skill 与 Python 分析器
scripts/wetrace.ps1          Wetrace PowerShell 入口
scripts/install-wetrace-skill.ps1
docs/WINDOWS_QUICKSTART.md   Windows 详细安装说明
docs/WETRACE_USAGE.md        Wetrace 完整使用示例
docs/ARCHITECTURE.md         架构与安全边界
docs/UPSTREAM_WECHAT_CLI.md  上游 wechat-cli 原始完整文档
```

## 快速开始

### 1. 准备环境

Windows 推荐：

```powershell
winget install --id GoLang.Go --exact
python --version
git --version
```

仓库当前使用 Go `1.26.5`。Wetrace 只依赖 Python 标准库，推荐 Python 3.10 或更高版本。

### 2. 克隆仓库

```powershell
git clone https://github.com/davids1896/wechat-local-analytics.git
cd wechat-local-analytics
```

### 3. 安装 wechat-cli

`wechat-cli` 运行时需要 `libWCDB.dll`。本仓库不提交第三方二进制文件。

若电脑上已经安装上游 `wechat-cli`，可以复用其 DLL：

```powershell
$env:WECHAT_CLI_WCDB_LIB = "$env:LOCALAPPDATA\wechat-cli\libWCDB.dll"
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Yes -Json
```

若尚未安装，请先阅读 [Windows 快速开始](docs/WINDOWS_QUICKSTART.md)，其中包含获取 WCDB DLL、设置数据目录和首次密钥读取的完整步骤。

### 4. 设置微信数据目录

`WECHAT_CLI_DB_ROOT` 必须指向**直接包含 `db_storage` 的账号目录**：

```powershell
$env:WECHAT_CLI_DB_ROOT = 'D:\WeChatData\xwechat_files\<账号目录>'
Get-ChildItem "$env:WECHAT_CLI_DB_ROOT\db_storage"
```

长期使用可以写入当前用户环境变量：

```powershell
[Environment]::SetEnvironmentVariable(
  'WECHAT_CLI_DB_ROOT',
  'D:\WeChatData\xwechat_files\<账号目录>',
  'User'
)
```

### 5. 首次读取密钥

首次密钥获取需要官方桌面微信保持登录。执行前请先了解可能的已读同步影响。

```powershell
wechat-cli cache refresh --force
```

Windows 密钥读取属于实验性兼容功能。不能仅凭安装成功判断可用，必须依次通过下面三个验收门：

```powershell
wechat-cli status --pretty
wechat-cli sessions --limit 10 --pretty
wechat-cli timeline "一个真实会话名称" --limit 10 --pretty
```

只有第三步返回真实消息正文，才算读取链路完整可用。

### 6. 使用 Wetrace

先检查连接：

```powershell
.\scripts\wetrace.ps1 doctor
```

查看会话和消息：

```powershell
.\scripts\wetrace.ps1 sessions --keyword "项目" --limit 20
.\scripts\wetrace.ps1 messages --talker "项目群" --time-range "last_7_days" --limit 100
```

生成统计：

```powershell
.\scripts\wetrace.ps1 analysis summary "项目群" --time-range "last_30_days" --max-messages 20000
.\scripts\wetrace.ps1 analysis member "项目群" --time-range "last_7_days"
.\scripts\wetrace.ps1 analysis top_contacts --time-range "last_30_days" --session-limit 100 --max-messages-per-chat 5000
.\scripts\wetrace.ps1 analysis annual --year 2026 --session-limit 100 --max-messages-per-chat 20000
```

生成网页仪表板：

```powershell
.\scripts\wetrace.ps1 dashboard --talker "项目群" --time-range "last_30_days" --html
```

更多命令、时间范围、导出方式和自然语言示例见 [Wetrace 使用手册](docs/WETRACE_USAGE.md)。

## 安装为 Codex Skill

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\install-wetrace-skill.ps1
```

如果目标目录已存在且确认需要覆盖：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\install-wetrace-skill.ps1 -Force
```

安装后在新的 Codex 任务中可以直接提出：

```text
用 Wetrace 查看“项目群”最近一周的聊天，按时间正序排列。
总结我和“张三”最近一个月聊了什么，并注明读取范围。
统计“项目群”近 30 天的成员发言排行。
生成最近 90 天最活跃的 20 个会话。
为“张三”生成最近 30 天的 HTML 仪表板。
```

## 严格只读

Wetrace 每次调用 `wechat-cli` 时都会设置：

```text
WECHAT_CLI_STRICT_READ_ONLY=1
```

这会禁止 `wechat-cli` 自动刷新元数据/密钥、写媒体解码缓存、写语音转写缓存以及执行 CLI 自带的导出操作。

Wetrace 自己仍可在用户明确要求时，把分析结果写入 `~/wetrace-exports/` 或指定路径。它不会修改微信数据库。

首次获取数据库密钥、刷新名称索引和媒体解密密钥不属于严格只读流程，应单独执行，并了解官方客户端可能产生的同步行为。

## 图片与语音

- Wetrace 默认只统计图片和语音类型，不主动读取媒体内容。
- 图片解密可能依赖当前账号的 `image_key` 或 `image_xor_key`，缺失时普通模式可尝试刷新并写入本地媒体缓存。
- 严格只读模式不会刷新图片密钥或写解码缓存。
- 语音转写是可选功能，使用本地 `faster-whisper` 与 SILK 解码器，不上传音频。
- 未落盘或缺少可用解码链路的语音只能统计数量和时长，不能推测内容。

## 数据范围与截断

单会话和跨会话分析都有读取上限。任何报告都应检查：

```text
scope.truncated
scope.sessions_truncated
scope.truncated_chat_count
scope.failed_chat_count
scope.total_messages_scanned
```

当 `scope.truncated=true` 时，结果是当前扫描范围内的统计或下界，不能表述为账号全量结论。

## 开发与测试

```powershell
& 'C:\Program Files\Go\bin\go.exe' test ./...
$env:PYTHONUTF8 = '1'
python .\skills\wetrace\scripts\test_wetrace_api.py -v
```

测试不读取真实聊天记录。真实读取回归脚本会生成含聊天内容的本地产物，只有在显式设置测试会话和关键词时才运行：

```bash
WECHAT_READ_TEST_CHAT="测试群" \
WECHAT_READ_TEST_KEYWORD="可命中的关键词" \
scripts/wechat-read-regression.sh
```

不要上传该脚本产生的目录。

## 来源与许可证

本仓库保留 `r266-tech/wechat-cli` 的 Git 历史，并在其基础上增加 Windows 密钥扫描改进和 Wetrace 分析层。

Wetrace 的产品方向和分析类别参考了 `afumu/wetrace-skill`，本仓库不包含其原服务端实现，数据适配器改为直接调用本地 `wechat-cli`。

代码使用 MIT License。第三方组件与署名见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

本项目与腾讯、微信官方无关联。“微信”和“WeChat”是其各自权利人的商标。
