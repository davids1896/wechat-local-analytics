# Wetrace 使用手册

Wetrace 是本仓库的 Python 分析层。它直接调用本机 `wechat-cli call-json`，不需要启动 HTTP 服务。

## 1. 准备离线副本

Wetrace 不接受微信正在使用的在线目录。先从托盘彻底退出微信，然后运行：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\create-wetrace-offline-copy.ps1 `
  -SourceAccountRoot 'G:\微信文件\xwechat_files\<账号目录>' `
  -DestinationRoot 'G:\微信离线副本' `
  -Incremental `
  -SetUserEnvironment
```

`-Incremental` 使用固定的 `<账号目录>-rolling` 目录。第一次完整复制 `db_storage`，
以后重复运行同一命令只同步新增、变化和删除的文件。它是文件级增量，不是消息行级增量：
只要一个 SQLite/WCDB 文件发生变化，就需要重新复制整个文件。

脚本会确认 `Weixin`/`WeChat` 进程均已退出。主数据库、`.db-wal` 和 `.db-shm` 必须来自
同一个静止时刻，否则顺序复制得到的文件可能互不一致。更新开始时脚本会撤销完成标记；
只有复制完成、源目录未变化、目标清单一致并再次确认微信仍未启动后，才原子写入
`.wetrace-offline-copy.json`。失败或中断后，退出微信并重新运行同一命令即可续跑。

不传 `-Incremental` 时，脚本会创建新的带时间戳完整快照。

重新打开 PowerShell 后，初始化离线副本自己的元数据缓存。首次创建以及每次更新副本后都应执行：

```powershell
$offline = [Environment]::GetEnvironmentVariable('WETRACE_OFFLINE_DB_ROOT', 'User')
$env:WETRACE_OFFLINE_DB_ROOT = $offline
$env:WECHAT_CLI_DB_ROOT = $offline
$env:WECHAT_CLI_STATE_DIR = Join-Path $offline '.wechat-cli-state'
wechat-cli cache refresh --force --pretty
Remove-Item Env:WECHAT_CLI_DB_ROOT -ErrorAction SilentlyContinue
Remove-Item Env:WECHAT_CLI_STATE_DIR -ErrorAction SilentlyContinue
```

初始化时无需启动微信。若已有数据库密钥不足，命令会失败；不要为了分析而打开微信或聊天。
清理两个临时变量后，Wetrace 会在自己的子进程中重新设置严格只读的离线目录。

只需要查询聊天记录时，可直接按 [如何读取微信聊天记录](READ_CHAT_HISTORY.md) 的线性流程操作。

## 2. 启动方式

从仓库根目录运行：

```powershell
.\scripts\wetrace.ps1 doctor
```

也可以直接调用 Python：

```powershell
$env:PYTHONUTF8 = '1'
python .\skills\wetrace\scripts\wetrace_api.py doctor
```

若 `wechat-cli` 不在 PATH：

```powershell
$env:WECHAT_CLI_BIN = 'D:\Tools\wechat-cli\wechat-cli.exe'
.\scripts\wetrace.ps1 doctor
```

Wetrace 能安全解析由安装器创建的简单 `wechat-cli.cmd` 转发文件，但不会通过 Shell 执行任意批处理内容。

Wetrace 会忽略外部 `WECHAT_CLI_DB_ROOT`，只使用经过标记验证的
`WETRACE_OFFLINE_DB_ROOT`。

## 3. 时间范围

支持：

```text
2026-06-01
2026-06-01~2026-06-30
last_week
last_month
last_year
last_7_days
last_30_days
最近一周
最近一个月
最近30天
```

明确日期范围的结束日期会扩展到当天 `23:59:59`。

## 4. 查找会话

查看最近会话：

```powershell
.\scripts\wetrace.ps1 sessions --limit 50
```

按名称搜索：

```powershell
.\scripts\wetrace.ps1 sessions --keyword "项目" --limit 20
```

只查群聊：

```powershell
.\scripts\wetrace.ps1 sessions --keyword "项目" --type-filter group --limit 20
```

只查私聊：

```powershell
.\scripts\wetrace.ps1 sessions --type-filter private --limit 50
```

若存在同名会话，优先使用返回结果里的原始 `username` 作为 `--talker`。

## 5. 读取聊天记录

读取最近 50 条：

```powershell
.\scripts\wetrace.ps1 messages --talker "张三" --limit 50
```

读取最近一周：

```powershell
.\scripts\wetrace.ps1 messages --talker "项目群" --time-range "last_7_days" --limit 500
```

按时间正序查询一个明确范围：

```powershell
.\scripts\wetrace.ps1 messages --talker "项目群" --time-range "2026-06-01~2026-06-30" --limit 5000 --reverse
```

只看某个发送者：

```powershell
.\scripts\wetrace.ps1 messages --talker "项目群" --sender "张三" --time-range "last_30_days" --limit 1000
```

只看图片或语音消息：

```powershell
.\scripts\wetrace.ps1 messages --talker "项目群" --kind image --time-range "last_30_days" --limit 1000
.\scripts\wetrace.ps1 messages --talker "项目群" --kind voice --time-range "last_30_days" --limit 1000
```

## 6. 关键词搜索与上下文

搜索单个会话：

```powershell
.\scripts\wetrace.ps1 search --keyword "验收" --talker "项目群" --time-range "last_90_days" --limit 50
```

跨会话搜索：

```powershell
.\scripts\wetrace.ps1 search --keyword "合同" --time-range "2026-01-01~2026-12-31" --limit 100
```

取得命中消息的 `local_id` 后读取上下文：

```powershell
.\scripts\wetrace.ps1 search-context --talker "项目群" --local-id 123456 --before 10 --after 10
```

## 7. 单会话统计

汇总：

```powershell
.\scripts\wetrace.ps1 analysis summary "张三" --time-range "last_30_days" --max-messages 20000
```

小时、星期、每日和每月趋势：

```powershell
.\scripts\wetrace.ps1 analysis hourly "张三" --time-range "last_30_days"
.\scripts\wetrace.ps1 analysis weekday "张三" --time-range "last_90_days"
.\scripts\wetrace.ps1 analysis daily "张三" --time-range "last_30_days"
.\scripts\wetrace.ps1 analysis monthly "张三" --time-range "2026-01-01~2026-12-31"
```

消息类型：

```powershell
.\scripts\wetrace.ps1 analysis type "项目群" --time-range "last_30_days"
```

群成员发言排行：

```powershell
.\scripts\wetrace.ps1 analysis member "项目群" --time-range "last_30_days" --max-messages 50000
```

重复短消息：

```powershell
.\scripts\wetrace.ps1 analysis repeat "项目群" --time-range "last_30_days"
```

基础词频：

```powershell
.\scripts\wetrace.ps1 analysis wordcloud "项目群" --time-range "last_90_days"
```

词频使用轻量正则切词，不等同于专业中文分词结果。

## 8. 跨会话统计

最近 30 天聊得最多的联系人和群：

```powershell
.\scripts\wetrace.ps1 analysis top_contacts `
  --time-range "last_30_days" `
  --session-limit 100 `
  --max-messages-per-chat 5000
```

年度报告数据：

```powershell
.\scripts\wetrace.ps1 analysis annual `
  --year 2026 `
  --session-limit 100 `
  --max-messages-per-chat 20000
```

跨会话扫描可能耗时较长。输出必须结合以下字段解释：

```text
scope.session_limit
scope.scanned_sessions
scope.sessions_truncated
scope.max_messages_per_chat
scope.truncated_chat_count
scope.failed_chat_count
scope.total_messages_scanned
scope.truncated
```

只要会话列表、任一会话消息或失败会话导致范围不完整，`scope.truncated` 就会是 `true`。

## 9. 仪表板与导出

生成 HTML 仪表板：

```powershell
.\scripts\wetrace.ps1 dashboard --talker "张三" --time-range "last_30_days" --html
```

指定 HTML 路径：

```powershell
.\scripts\wetrace.ps1 dashboard `
  --talker "张三" `
  --time-range "last_30_days" `
  --html "D:\Reports\zhangsan-dashboard.html"
```

导出 JSON：

```powershell
.\scripts\wetrace.ps1 export chat --talker "张三" --time-range "last_30_days" --format json
```

导出 CSV、TXT 或 HTML：

```powershell
.\scripts\wetrace.ps1 export chat --talker "项目群" --format csv
.\scripts\wetrace.ps1 export chat --talker "项目群" --format txt
.\scripts\wetrace.ps1 export chat --talker "项目群" --format html
```

默认输出目录：

```text
%USERPROFILE%\wetrace-exports
```

PDF、DOCX 和 XLSX 不由该脚本伪造。应先导出 JSON，再交给相应文档工具生成。

## 10. Codex Skill 用法

安装：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\install-wetrace-skill.ps1
```

自然语言示例：

```text
查看“项目群”最近一周的聊天，按时间正序排列。
找出我和“张三”在 2026 年 6 月的所有聊天。
总结我和“张三”最近一个月聊了什么。
把“项目群”近一周的聊天整理成简报。
找出别人明确让我处理但我可能还没有回复的事情。
对“项目群”统计群成员发言排行。
找出“项目群”重复出现次数最多的短消息。
生成最近三个月的聊天词频。
统计最近 30 天我聊得最多的联系人和群。
分析今年每个月的微信活跃度变化。
找出最近 90 天最活跃的 20 个会话。
比较我和“张三”最近三个月与前三个月的沟通频率。
为“张三”生成最近 30 天的可视化仪表板。
为“张三”生成一份带统计图和摘要的 PDF 月报。
生成 2026 年微信年度报告，扫描最近活跃的 100 个会话，每个会话最多读取 20000 条，并注明截断状态。
```

待办、摘要、关系分析等语义判断由 Agent 基于消息 JSON 完成。脚本本身不会把关键词规则包装成确定事实。

## 11. 数据和隐私边界

- Wetrace 只读取带完成标记的离线副本。
- 滚动副本更新期间或上次更新未完成时，Wetrace 拒绝读取。
- Wetrace 强制用 `WETRACE_OFFLINE_DB_ROOT` 覆盖 `WECHAT_CLI_DB_ROOT`。
- Wetrace 总是用 `WECHAT_CLI_STRICT_READ_ONLY=1` 调用数据层。
- 不发送、删除或标记消息。
- 不控制微信 UI。
- Wetrace 适配器自身不发起聊天数据上传；若把输出交给 Codex 或其他 Agent 做摘要、待办或关系分析，数据处理方式取决于所使用的 Agent 平台与配置。
- 报告文件只写入用户指定目录或 `~/wetrace-exports`。
- 图片默认只计入类型统计。
- 未落盘语音不推测内容。
- 报告中应区分事实统计、Agent 总结和推断。

## 12. 故障排查

### 缺少离线副本

运行第 1 节的副本创建命令。不要手工伪造 `.wetrace-offline-copy.json`。

### 离线副本正在更新或上次更新未完成

确保 Wetrace 没有正在执行的长查询，从托盘彻底退出微信，然后重新运行第 1 节的
`-Incremental` 命令。脚本会继续同步并在校验成功后恢复完成标记。

### 找不到 wechat-cli

```powershell
$env:WECHAT_CLI_BIN = "$env:LOCALAPPDATA\wechat-cli\wechat-cli.exe"
.\scripts\wetrace.ps1 doctor
```

### doctor 报密钥未就绪

严格只读模式不会自动取密钥。只针对离线副本显式执行：

```powershell
$env:WECHAT_CLI_DB_ROOT = $env:WETRACE_OFFLINE_DB_ROOT
$env:WECHAT_CLI_STATE_DIR = Join-Path $env:WETRACE_OFFLINE_DB_ROOT '.wechat-cli-state'
wechat-cli cache refresh --force --pretty
Remove-Item Env:WECHAT_CLI_DB_ROOT -ErrorAction SilentlyContinue
Remove-Item Env:WECHAT_CLI_STATE_DIR -ErrorAction SilentlyContinue
```

完成并清理临时变量后再运行 Wetrace。微信应继续保持退出。

### 名称匹配错误

```powershell
wechat-cli resolve-chat "显示名称" --pretty
```

使用返回的原始 `username` 作为 `--talker`。

### 输出显示截断

增加 `--max-messages`、`--session-limit` 或 `--max-messages-per-chat`，或者缩小时间范围。

### Python 中文输出异常

```powershell
$env:PYTHONUTF8 = '1'
$env:PYTHONIOENCODING = 'utf-8'
```
