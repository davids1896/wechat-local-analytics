# Wetrace 使用手册

Wetrace 是本仓库的 Python 分析层。它直接调用本机 `wechat-cli call-json`，不需要启动 HTTP 服务。

## 1. 启动方式

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

## 2. 时间范围

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

## 3. 查找会话

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

## 4. 读取聊天记录

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

## 5. 关键词搜索与上下文

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

## 6. 单会话统计

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

## 7. 跨会话统计

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

## 8. 仪表板与导出

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

## 9. Codex Skill 用法

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

## 10. 数据和隐私边界

- Wetrace 总是用 `WECHAT_CLI_STRICT_READ_ONLY=1` 调用数据层。
- 不发送、删除或标记消息。
- 不控制微信 UI。
- Wetrace 适配器自身不发起聊天数据上传；若把输出交给 Codex 或其他 Agent 做摘要、待办或关系分析，数据处理方式取决于所使用的 Agent 平台与配置。
- 报告文件只写入用户指定目录或 `~/wetrace-exports`。
- 图片默认只计入类型统计。
- 未落盘语音不推测内容。
- 报告中应区分事实统计、Agent 总结和推断。

## 11. 故障排查

### 找不到 wechat-cli

```powershell
$env:WECHAT_CLI_BIN = "$env:LOCALAPPDATA\wechat-cli\wechat-cli.exe"
.\scripts\wetrace.ps1 doctor
```

### doctor 报密钥未就绪

严格只读模式不会自动取密钥。退出 Wetrace 流程，显式执行：

```powershell
wechat-cli cache refresh --force --pretty
```

完成后再运行 Wetrace。

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
