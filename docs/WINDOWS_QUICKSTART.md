# Windows 安装与首次读取

本文面向 Windows 11/10 amd64。目标是从源码构建本仓库版本，并验证能读取真实消息正文。

## 1. 安全提醒

`wechat-cli` 读取数据库时使用只读打开方式，但首次密钥获取需要读取已登录微信进程内存。

官方桌面微信本身可能把“打开聊天、播放语音或查看图片”的行为同步到手机端。若必须保留未读红点：

1. 不要为了测试随意打开未读会话。
2. 优先选择一个已经读过的普通聊天进行首次密钥获取。
3. 读取成功后，后续分析尽量在桌面微信退出时进行。
4. 对高敏感场景，先复制数据目录到离线快照，再针对快照分析。

## 2. 前置条件

安装 Git、Go 和 Python：

```powershell
winget install --id Git.Git --exact
winget install --id GoLang.Go --exact
winget install --id Python.Python.3.12 --exact
```

重新打开 PowerShell 后验证：

```powershell
git --version
go version
python --version
```

仓库当前 `go.mod` 要求 Go `1.26.5`。

## 3. 获取 libWCDB.dll

源代码仓库不提交腾讯 WCDB 二进制库。`wechat-cli.exe` 与 `libWCDB.dll` 必须放在同一个安装目录。

最省事的方法是先安装上游发布包来取得 DLL：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://github.com/r266-tech/wechat-cli/releases/latest/download/install-release.ps1 | iex"
```

确认文件存在：

```powershell
Get-Item "$env:LOCALAPPDATA\wechat-cli\libWCDB.dll"
```

也可以自行构建 Tencent WCDB，并通过 `WECHAT_CLI_WCDB_LIB` 指向生成的 DLL。

## 4. 克隆和安装本仓库版本

```powershell
git clone https://github.com/davids1896/wechat-local-analytics.git
cd wechat-local-analytics

$env:WECHAT_CLI_WCDB_LIB = "$env:LOCALAPPDATA\wechat-cli\libWCDB.dll"
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Yes -Json
```

安装器会从当前源码构建 `wechat-cli.exe`，复制 DLL，并创建命令转发文件。

验证：

```powershell
wechat-cli --help
wechat-cli tools
```

## 5. 找到账号数据目录

微信数据目录通常包含：

```text
xwechat_files/
└─ <账号目录>/
   ├─ db_storage/
   ├─ msg/
   └─ ...
```

`WECHAT_CLI_DB_ROOT` 应指向账号目录，而不是 `xwechat_files`，也不是 `db_storage`：

```powershell
$accountRoot = 'D:\WeChatData\xwechat_files\<账号目录>'
Test-Path "$accountRoot\db_storage"
$env:WECHAT_CLI_DB_ROOT = $accountRoot
```

若返回 `True`，可以保存为用户环境变量：

```powershell
[Environment]::SetEnvironmentVariable(
  'WECHAT_CLI_DB_ROOT',
  $accountRoot,
  'User'
)
```

新开的 PowerShell 才会自动获得用户级环境变量。

## 6. 首次密钥获取

1. 启动官方桌面微信并完成登录。
2. 选择一个已经读过的普通聊天，打开一次。
3. 保持微信运行。
4. 在 PowerShell 执行：

```powershell
wechat-cli cache refresh --force --pretty
```

本仓库的 Windows 扫描器会寻找：

- ASCII 形式的 raw key 与 salt
- UTF-16 形式的 raw key 与 salt
- 数据库 salt 相邻的二进制 32 字节候选

所有候选都会对真实数据库进行验证，不会仅凭内存匹配直接接受。

若扫描较慢：

```powershell
$env:WECHAT_CLI_KEY_SCAN_TIMEOUT = '5m'
wechat-cli cache refresh --force --pretty
```

这项功能不能保证支持所有微信版本。不要根据目录名或安装成功推断兼容性。

## 7. 三层验收

### 状态可用

```powershell
wechat-cli status --pretty
```

检查数据库根目录、密钥和 WCDB 是否可用。

### 会话非空

```powershell
wechat-cli sessions --type-filter private,group --limit 20 --pretty
```

### 真实正文可读

```powershell
wechat-cli timeline "一个真实会话名称" --limit 20 --pretty
```

必须看到真实消息正文才算完成。仅状态成功或会话列表非空还不够。

## 8. 严格只读查询

PowerShell 当前进程：

```powershell
$env:WECHAT_CLI_STRICT_READ_ONLY = '1'
wechat-cli timeline "会话名称" --limit 50 --pretty
```

或单次命令：

```powershell
wechat-cli --strict-read-only timeline "会话名称" --limit 50 --pretty
```

严格只读模式会禁止：

- 自动刷新数据库密钥和元数据
- 图片解码缓存写入
- 语音转写缓存写入
- `cache refresh` / `cache rebuild`
- `wechat-cli export`

## 9. 使用 Wetrace

先从托盘彻底退出微信，并创建带完成标记的离线副本：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\scripts\create-wetrace-offline-copy.ps1 `
  -SourceAccountRoot $env:WECHAT_CLI_DB_ROOT `
  -DestinationRoot 'G:\微信离线副本' `
  -SetUserEnvironment
```

重新打开 PowerShell 后：

```powershell
.\scripts\wetrace.ps1 doctor
.\scripts\wetrace.ps1 sessions --limit 20
.\scripts\wetrace.ps1 messages --talker "会话名称" --time-range "last_7_days" --limit 100
```

Wetrace 只读取 `WETRACE_OFFLINE_DB_ROOT` 指向的离线副本，并自动设置严格只读环境变量。

## 10. 常见问题

### 找不到 WCDB DLL

```powershell
$env:WECHAT_CLI_WCDB_LIB = 'D:\Tools\wechat-cli\libWCDB.dll'
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Yes -Json
```

### 数据目录错误

确保下面路径存在：

```powershell
Get-ChildItem "$env:WECHAT_CLI_DB_ROOT\db_storage"
```

### 找不到可用密钥

确认：

- 登录账号与 `WECHAT_CLI_DB_ROOT` 是同一账号
- 微信进程仍在运行
- 已打开过一个普通、已读聊天
- 没有同时运行多个不同版本微信

查看错误中的 `diagnostics`，重点关注：

- `scanned_processes`
- `read_successes`
- `raw_key_literals`
- `utf16_raw_key_literals`
- `binary_candidates`
- `verification_attempts`
- `verified_dbs`

### 会话名称解析失败

```powershell
wechat-cli resolve-chat "显示名称" --pretty
```

把返回的原始 `username` / `talker` 传给后续命令。
