---
name: wetrace
description: "基于本机 wechat-cli 查询、搜索、统计、分析和导出微信聊天记录，并生成活跃度趋势、成员排行、词频、AI 摘要、待办、周报/月报和独立 HTML 仪表板。用于涉及 Wetrace、微信聊天记录、聊天统计、关键词搜索、关系分析、年度报告或微信数据可视化的请求。"
---

# Wetrace 本地微信分析

直接调用 `wechat-cli`，不要连接 `127.0.0.1:5200`，不要要求用户启动旧版 Wetrace 服务。

## 安全边界

- 只允许读取带 `.wetrace-offline-copy.json` 标记的离线数据库副本。
- `.wetrace-offline-copy.updating.json` 存在时视为更新中或更新失败，拒绝读取。
- 必须通过 `WETRACE_OFFLINE_DB_ROOT` 指定副本；忽略可能指向在线目录的 `WECHAT_CLI_DB_ROOT`。
- 数据库访问始终设置 `WECHAT_CLI_STRICT_READ_ONLY=1`。
- 不发送、删除、修改或标记消息。
- 不控制微信界面。
- 不要求用户为了分析启动微信、打开聊天、播放语音或查看图片。
- 图片默认只计入类型统计，除非用户明确要求读取图片内容。
- 未落盘语音只统计数量和时长，不推测内容。
- 报告写入 `~/wetrace-exports/` 或用户指定路径。

## 入口

仓库内脚本：

```powershell
$env:PYTHONUTF8 = '1'
python scripts/wetrace_api.py doctor
```

skill 安装后，`scripts/wetrace_api.py` 相对于本 `SKILL.md` 所在目录。

## 工作流

1. 确认 `WETRACE_OFFLINE_DB_ROOT` 指向已完成的离线副本。
2. 先运行 `doctor` 检查离线副本、`wechat-cli` 和密钥状态。
3. 用 `sessions --keyword` 或 `contacts --keyword` 解析联系人/群名称。
4. 普通读取用 `messages`，关键词检索用 `search`。
5. 单会话统计用 `analysis`；设置明确时间范围和 `--max-messages`。
6. 跨会话统计用 `analysis top_contacts` 或 `analysis annual`；明确设置会话和单会话消息上限。
7. 检查 `scope.truncated`、失败会话和扫描上限，并在回答中披露。
8. 摘要、待办和关系洞察由 Agent 基于消息 JSON 分析，不让脚本假装完成语义判断。
9. 可视化用 `dashboard --html`。
10. PDF/XLSX/DOCX 先导出 JSON，再调用对应文档工具。

## 常用命令

```powershell
python scripts/wetrace_api.py sessions --keyword "张三" --limit 20
python scripts/wetrace_api.py messages --talker "张三" --time-range "last_7_days" --limit 100
python scripts/wetrace_api.py search --keyword "项目" --talker "项目群" --time-range "last_30_days"
python scripts/wetrace_api.py search-context --talker "项目群" --local-id 123 --before 10 --after 10

python scripts/wetrace_api.py analysis summary "张三" --time-range "last_30_days" --max-messages 20000
python scripts/wetrace_api.py analysis member "项目群" --time-range "last_7_days"
python scripts/wetrace_api.py analysis repeat "项目群" --time-range "last_30_days"
python scripts/wetrace_api.py analysis wordcloud "项目群" --time-range "last_90_days"

python scripts/wetrace_api.py analysis top_contacts --time-range "last_30_days" --session-limit 100 --max-messages-per-chat 5000
python scripts/wetrace_api.py analysis annual --year 2026 --session-limit 100 --max-messages-per-chat 20000

python scripts/wetrace_api.py dashboard --talker "张三" --time-range "last_30_days" --html
python scripts/wetrace_api.py export chat --talker "张三" --time-range "last_30_days" --format json
```

## 分析要求

- 区分事实统计、模型总结和推测。
- 报告必须写明会话、时间范围、读取消息数和截断状态。
- 待办提取保留原消息时间与发送者，避免把闲聊误判为承诺。
- 未指定范围时采用合理的最近周期，并在结果中明确说明。
- 跨会话结果在达到上限时只能称为扫描范围内结果或下界。

## 参考

- 命令和字段：`references/api.md`
- AI 分析规范：`references/analysis-guidance.md`
- 完整用户手册：仓库根目录 `docs/WETRACE_USAGE.md`
