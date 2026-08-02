# 如何读取微信聊天记录

本文给出一条可以直接照着执行的 Windows 流程：从微信在线数据创建增量离线副本，再用 Wetrace 或底层 `wechat-cli` 只读查询聊天记录。

推荐日常使用 Wetrace。它强制读取带安全标记的离线副本，不会直接打开微信正在使用的数据目录。

## 1. 安全边界

- 创建或更新离线副本时，必须从系统托盘彻底退出微信。
- 离线副本创建完成后，可以重新启动微信；查询仍只读取上一次完成的副本。
- 读取已完成的离线副本不会发送、删除、撤回或标记微信消息，也不会改变手机端未读状态。
- Wetrace 不控制微信界面，不需要在查询时打开联系人、群聊、图片或语音。
- Wetrace 本身在本机读取和统计数据。若把结果交给 Codex 或其他 Agent 做摘要，数据处理边界取决于所使用的 Agent 平台与配置。

离线副本只复制账号目录中的 `db_storage`，不复制完整图片、视频和文件目录。因此，微信文件总目录即使超过 100 GB，聊天数据库副本也可能只有几 GB。

## 2. 准备路径

从仓库根目录打开 PowerShell：

```powershell
cd 'C:\path\to\wechat-local-analytics'
```

找到账号目录。`SourceAccountRoot` 必须是包含 `db_storage` 的账号目录，而不是 `xwechat_files`，也不是 `db_storage` 本身：

```powershell
Get-ChildItem 'G:\微信文件\xwechat_files' -Directory

$source = 'G:\微信文件\xwechat_files\<账号目录>'
Test-Path "$source\db_storage"
```

最后一条命令必须返回 `True`。

确认程序已安装：

```powershell
wechat-cli --help
python --version
```

若尚未安装，先阅读 [Windows 安装与首次读取](WINDOWS_QUICKSTART.md)。

## 3. 创建或更新增量离线副本

先从系统托盘彻底退出微信，再确认没有残留进程：

```powershell
Get-Process Weixin, WeChat -ErrorAction SilentlyContinue
```

没有输出后执行：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass `
  -File '.\scripts\create-wetrace-offline-copy.ps1' `
  -SourceAccountRoot 'G:\微信文件\xwechat_files\<账号目录>' `
  -DestinationRoot 'G:\微信离线副本' `
  -Incremental `
  -SetUserEnvironment
```

第一次运行会完整复制 `db_storage`。以后重复运行同一命令时，会复用固定的
`<账号目录>-rolling` 目录，只同步新增、变化和删除的数据库文件。

这是文件级增量，不是消息行级增量。一个数据库文件发生变化时，仍需重新复制整个文件。

脚本完成后会：

- 创建 `.wetrace-offline-copy.json` 完成标记。
- 删除 `.wetrace-offline-copy.updating.json` 更新中标记。
- 把用户环境变量 `WETRACE_OFFLINE_DB_ROOT` 指向滚动副本。

复制失败或中断时，不要手工伪造完成标记。退出微信后重新运行同一条增量命令即可续跑。

## 4. 让当前 PowerShell 识别离线副本

`-SetUserEnvironment` 只会自动影响以后新开的 PowerShell。当前窗口需要手动载入：

```powershell
$offline = [Environment]::GetEnvironmentVariable(
  'WETRACE_OFFLINE_DB_ROOT',
  'User'
)

if ([string]::IsNullOrWhiteSpace($offline)) {
  throw 'WETRACE_OFFLINE_DB_ROOT 尚未设置'
}

$env:WETRACE_OFFLINE_DB_ROOT = $offline
Get-Item "$offline\.wetrace-offline-copy.json"
Get-ChildItem "$offline\db_storage" | Select-Object -First 5
```

不要把 `WETRACE_OFFLINE_DB_ROOT` 手工设置成微信在线账号目录。

## 5. 初始化或刷新离线元数据

首次创建以及每次更新离线副本后，刷新该副本自己的联系人和会话元数据缓存：

```powershell
$env:WECHAT_CLI_DB_ROOT = $offline
$env:WECHAT_CLI_STATE_DIR = Join-Path $offline '.wechat-cli-state'

wechat-cli cache refresh --force --pretty

Remove-Item Env:WECHAT_CLI_DB_ROOT -ErrorAction SilentlyContinue
Remove-Item Env:WECHAT_CLI_STATE_DIR -ErrorAction SilentlyContinue
```

这一步读取离线数据库，并只向离线副本内的 `.wechat-cli-state` 写辅助缓存。

最后两行很重要。Wetrace 会自行把底层 CLI 指向离线副本；若当前
`WECHAT_CLI_DB_ROOT` 仍与 `WETRACE_OFFLINE_DB_ROOT` 相同，安全检查会拒绝继续。

若 `cache refresh` 报缺少数据库密钥，说明现有密钥表不能解密副本中的某些数据库。
严格离线流程不会为了补密钥自动启动微信或读取微信进程。

## 6. 检查读取状态

```powershell
.\scripts\wetrace.ps1 doctor
```

成功结果应至少包含：

```text
"ok": true
"source": "wechat_cli_offline_copy"
"strict_read_only": true
```

若 `wechat-cli` 不在 `PATH`：

```powershell
$env:WECHAT_CLI_BIN = "$env:LOCALAPPDATA\wechat-cli\wechat-cli.exe"
.\scripts\wetrace.ps1 doctor
```

## 7. 找到联系人或群

查看最近会话：

```powershell
.\scripts\wetrace.ps1 sessions --limit 50
```

按名称查找：

```powershell
.\scripts\wetrace.ps1 sessions --keyword "椰椰" --limit 20
.\scripts\wetrace.ps1 sessions --keyword "龙虾池" --type-filter group --limit 20
```

只看私聊或群聊：

```powershell
.\scripts\wetrace.ps1 sessions --type-filter private --limit 50
.\scripts\wetrace.ps1 sessions --type-filter group --limit 50
```

如果存在同名会话，使用结果中的原始 `username` 作为后续命令的 `--talker`，避免读错会话。

## 8. 读取聊天记录

读取最近 50 条：

```powershell
.\scripts\wetrace.ps1 messages --talker "椰椰" --limit 50
```

读取最近一周：

```powershell
.\scripts\wetrace.ps1 messages `
  --talker "龙虾池" `
  --time-range "last_7_days" `
  --limit 1000
```

按时间正序排列：

```powershell
.\scripts\wetrace.ps1 messages `
  --talker "龙虾池" `
  --time-range "last_7_days" `
  --limit 1000 `
  --reverse
```

读取明确日期范围：

```powershell
.\scripts\wetrace.ps1 messages `
  --talker "椰椰" `
  --time-range "2026-06-01~2026-06-30" `
  --limit 5000 `
  --reverse
```

只看某个发送者：

```powershell
.\scripts\wetrace.ps1 messages `
  --talker "龙虾池" `
  --sender "张三" `
  --time-range "last_30_days" `
  --limit 1000
```

按消息类型过滤：

```powershell
.\scripts\wetrace.ps1 messages --talker "龙虾池" --kind image --limit 500
.\scripts\wetrace.ps1 messages --talker "龙虾池" --kind voice --limit 500
.\scripts\wetrace.ps1 messages --talker "龙虾池" --kind file --limit 500
```

## 9. 搜索关键词和读取上下文

在一个会话中搜索：

```powershell
.\scripts\wetrace.ps1 search `
  --keyword "验收" `
  --talker "龙虾池" `
  --time-range "last_90_days" `
  --limit 50
```

跨全部会话搜索：

```powershell
.\scripts\wetrace.ps1 search `
  --keyword "合同" `
  --time-range "2026-01-01~2026-12-31" `
  --limit 100
```

从搜索结果中取得 `local_id`，再展开前后文：

```powershell
.\scripts\wetrace.ps1 search-context `
  --talker "龙虾池" `
  --local-id 123456 `
  --before 10 `
  --after 10
```

## 10. 分页和截断

`messages`、`sessions` 和 `search` 都支持 `--limit` 与 `--offset`。

例如每页读取 500 条消息：

```powershell
.\scripts\wetrace.ps1 messages --talker "龙虾池" --limit 500 --offset 0
.\scripts\wetrace.ps1 messages --talker "龙虾池" --limit 500 --offset 500
.\scripts\wetrace.ps1 messages --talker "龙虾池" --limit 500 --offset 1000
```

离线副本在下一次更新前保持不变，因此分页期间不会受新消息到达影响。

分析或导出结果中的 `scope.truncated=true` 表示命令达到消息或会话上限。此时应增加
`--max-messages`，或缩小时间范围后分段读取，不能把当前结果表述成完整全量结论。

## 11. 导出聊天记录

导出 JSON：

```powershell
.\scripts\wetrace.ps1 export chat `
  --talker "椰椰" `
  --time-range "last_30_days" `
  --format json
```

指定文件并导出 CSV：

```powershell
.\scripts\wetrace.ps1 export chat `
  --talker "龙虾池" `
  --time-range "last_7_days" `
  --format csv `
  --output 'D:\Reports\龙虾池-最近一周.csv'
```

还支持 `txt` 和 `html`。未指定 `--output` 时，文件默认写入：

```text
%USERPROFILE%\wetrace-exports
```

## 12. 直接使用底层 wechat-cli

Wetrace 适合日常查询和统计。需要查看底层时间线、游标或更完整诊断时，可直接使用
`wechat-cli`，但仍应显式指向离线副本并启用严格只读：

```powershell
$env:WECHAT_CLI_DB_ROOT = $offline
$env:WECHAT_CLI_STATE_DIR = Join-Path $offline '.wechat-cli-state'
$env:WECHAT_CLI_STRICT_READ_ONLY = '1'
```

查找会话并读取时间线：

```powershell
wechat-cli sessions --keyword "龙虾池" --limit 20 --pretty
wechat-cli timeline "龙虾池" --limit 50 --pretty
```

继续向前读取更早的消息。把上一页结果中
`query.cursor.next_before_message` 的值代入：

```powershell
wechat-cli timeline "龙虾池" `
  --before-message 123456 `
  --limit 50 `
  --pretty
```

搜索并自动展开部分命中的上下文：

```powershell
wechat-cli search-context "验收" `
  --in "龙虾池" `
  --limit 5 `
  --context-limit 3 `
  --before-count 10 `
  --after-count 10 `
  --pretty
```

使用完成后清理当前窗口的临时变量：

```powershell
Remove-Item Env:WECHAT_CLI_DB_ROOT -ErrorAction SilentlyContinue
Remove-Item Env:WECHAT_CLI_STATE_DIR -ErrorAction SilentlyContinue
Remove-Item Env:WECHAT_CLI_STRICT_READ_ONLY -ErrorAction SilentlyContinue
```

底层 `wechat-cli export` 在严格只读模式下会被禁用。需要导出时使用 Wetrace 的
`export chat`，它只把查询结果写入用户指定目录，不修改微信数据库。

## 13. 图片、语音和文件限制

离线副本默认只包含数据库：

- 文本、发送者、时间、消息类型和媒体元数据通常可以读取。
- 原始图片、语音、视频和文件可能位于未复制的媒体目录中。
- 图片还可能需要 `image_key` 或 `image_xor_key` 才能解密。
- 语音转写需要本地音频文件、SILK 解码链路和可选 ASR 运行时。
- 缺少媒体文件或密钥时，Wetrace 仍可统计图片、语音等消息数量，但不能凭空恢复内容。

为保护未读状态，日常分析不应为了补媒体内容而自动启动微信、打开聊天或播放语音。

## 14. 日常更新流程

以后要读取更新的聊天记录时：

1. 从托盘彻底退出微信。
2. 重新运行第 3 节的 `-Incremental` 命令。
3. 重新载入当前窗口的 `WETRACE_OFFLINE_DB_ROOT`。
4. 重新运行第 5 节的 `cache refresh`。
5. 运行 `.\scripts\wetrace.ps1 doctor`。
6. 可以重新启动微信，并继续查询已完成的离线副本。

## 15. 常见错误

### 微信仍在运行

```text
WeChat is running during offline snapshot preparation
```

从系统托盘退出微信，并确认任务管理器中没有 `Weixin.exe` 或 `WeChat.exe`。

### 离线副本正在更新

```text
离线副本正在更新或上次更新未完成
```

退出微信，重新运行同一条 `-Incremental` 命令。不要手工删除或伪造标记文件。

### 离线目录与 WECHAT_CLI_DB_ROOT 相同

```text
WETRACE_OFFLINE_DB_ROOT 与当前 WECHAT_CLI_DB_ROOT 相同
```

清理当前 PowerShell 中为初始化缓存临时设置的变量：

```powershell
Remove-Item Env:WECHAT_CLI_DB_ROOT -ErrorAction SilentlyContinue
Remove-Item Env:WECHAT_CLI_STATE_DIR -ErrorAction SilentlyContinue
.\scripts\wetrace.ps1 doctor
```

### 找不到会话

```powershell
.\scripts\wetrace.ps1 sessions --keyword "名称的一部分" --limit 50
```

使用结果里的原始 `username` 作为 `--talker`。

### 中文输出乱码

```powershell
$env:PYTHONUTF8 = '1'
$env:PYTHONIOENCODING = 'utf-8'
```

### 结果不完整

检查输出中的：

```text
scope.has_more
scope.truncated
scope.failed_chat_count
scope.total_messages_scanned
```

通过分页、增加上限或缩小时间范围继续读取。
