# wechat-cli Agent Guide

This repository is meant to be installed and operated by an agent for a user on
macOS or Windows.

## Project Identity

`wechat-cli` is a WeChat/微信 local chat history CLI for macOS and Windows
agents. Use it for local WeChat messages, contacts, groups, media, Moments,
favorites, transfers, red packets, unread chats, and full-text search from the
user's own machine.

Do not classify it as a screen-control WeChat bot, Accessibility automation,
WeChat Official Account publisher, Mini Program devtools bridge, WeChat Work /
WeCom bot, reply bot, or external protocol server. It is a local-data CLI for
WeChat 4.x.

## Fast Install

Prefer release bootstrap or latest release zip over a source clone.

Human-friendly macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh | zsh
```

Agent JSON macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh | env WECHAT_CLI_INSTALL_JSON=1 zsh
```

Windows:

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "irm https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.ps1 | iex"
powershell -NoProfile -ExecutionPolicy Bypass -Command "[Environment]::SetEnvironmentVariable('WECHAT_CLI_INSTALL_JSON','1','Process'); irm https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.ps1 | iex"
```

Default install is CLI-only. It does not register external agent protocols and
does not install a watcher.

Release assets:

- `wechat-cli-latest-darwin-arm64.zip`
- `wechat-cli-latest-windows-amd64.zip`
- `wechat-cli-vX.Y.Z-darwin-arm64.zip`
- `wechat-cli-vX.Y.Z-windows-amd64.zip`

Release zip contents:

- macOS: `wechat-cli`, `wxkey`, `libWCDB.dylib`, `install.sh`, docs, and
  `scripts/install-release.sh`
- Windows: `wechat-cli.exe`, `libWCDB.dll`, `install.ps1`, docs, and
  `scripts/install-release.ps1`
- Optional voice ASR is not bundled into the default release. Users explicitly
  opt in with installer `--with-asr` / `-WithASR` or `wechat-cli asr setup`.
  Setup creates `~/.wechat-cli/asr-venv`, installs `faster-whisper` and
  `silk-python`, and preloads `large-v3` unless `--skip-model-download` is set.

## Runtime Facts

- macOS arm64 with WeChat 4.x, or Windows amd64 with Windows WeChat / Weixin 4.x.
- macOS first key setup should use `./wxkey bootstrap`. It may quit/reopen
  WeChat, sign a wechat-cli shadow copy, and store a wxkey sudo credential in
  Keychain.
- On WeChat 4.1.10+, passive scan `found=0` can be followed by PBKDF fallback.
  The fallback defaults to a 5-minute probe and stops existing WeChat before
  launching the original ad-hoc app or shadow app under LLDB. If it prints
  `PBKDF diagnostics`, use `pbkdf_calls`, `matching_db_salt_calls`, and
  `matching_mac_salt_calls` to distinguish no DB decrypt, wrong account root,
  or unsupported derivation.
- macOS runtime DB reads and key refreshes do not require disabling SIP after
  `wxkey bootstrap` has stored the sudo credential and written a schema-2 key map.
- Windows first key setup is built into `wechat-cli.exe cache refresh --force`.
  Keep Windows WeChat logged in and open at least one chat first.
  Windows key scan defaults to a 3-minute timeout; set
  `WECHAT_CLI_KEY_SCAN_TIMEOUT=5m` only on slow machines that need longer.
- `libWCDB.dylib` must be present beside `wechat-cli` on macOS; `libWCDB.dll`
  must be present beside `wechat-cli.exe` on Windows.
- Install dir defaults: `~/.local/share/wechat-cli` on macOS and
  `%LOCALAPPDATA%\wechat-cli` on Windows.
- Command shim defaults to `~/.local/bin/wechat-cli` on macOS. On Windows it
  defaults to `%LOCALAPPDATA%\Microsoft\WindowsApps\wechat-cli.cmd` when that
  directory exists, otherwise `%USERPROFILE%\.local\bin\wechat-cli.cmd`.
- State/cache dir defaults to `~/.wechat-cli`.
- Key config remains wxkey-compatible at `~/.config/wxcli/config.json`.
- Preferred env prefix is `WECHAT_CLI_*`.

## macOS Password Step

`wxkey bootstrap` needs `task_for_pid` permission. The supported path is no-SIP:
prepare an ad-hoc signed wechat-cli shadow copy of WeChat when needed, ask the
user for their Mac admin password once, verify it with sudo, and store it in the
user's macOS Keychain.

Agents may run:

```bash
./install.sh --all --yes --json
```

The user only answers the hidden local password prompt. Later metadata cache
refreshes, DB decryption, image-key refreshes, and key re-scans use the stored
sudo credential unattended.

## Update

For an existing release install, prefer the first-class updater:

```bash
wechat-cli update
```

```powershell
wechat-cli update
```

`wechat-cli update` downloads the latest GitHub release zip, verifies sha256
when the checksum asset is present, then runs the bundled installer in update
mode. On macOS it waits for completion and returns the installer result inside
the normal CLI JSON envelope. On Windows it starts a background updater because
Windows cannot overwrite the running `.exe`; inspect `data.log` if verification
fails.

If an installed macOS binary immediately prints `zsh: killed` even for
`wechat-cli --help`, the existing CLI may not be able to self-update. Use the
release bootstrap command below. Current macOS installers copy/build into a
temporary file, ad-hoc codesign it, then atomically rename it into place so an
update does not in-place overwrite the running Mach-O.

For old installs that do not have `wechat-cli update` yet, run the release
bootstrap again:

```bash
curl -fsSL https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.sh | zsh -s -- --update
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -Command "[Environment]::SetEnvironmentVariable('WECHAT_CLI_INSTALL_JSON','1','Process'); $p=Join-Path $env:TEMP 'wechat-cli-install-release.ps1'; iwr https://raw.githubusercontent.com/r266-tech/wechat-cli/main/scripts/install-release.ps1 -OutFile $p -UseBasicParsing; powershell -NoProfile -ExecutionPolicy Bypass -File $p -Update -Json"
```

When already inside a freshly extracted release zip, this lower-level command
also works:

```bash
./install.sh --update --yes --json
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Update -Yes -Json
```

For an existing git checkout, the same update command runs `git pull --ff-only`
when possible and reinstalls binaries. It does not rerun bootstrap, refresh, or
watcher setup unless flags are explicitly added.

Verify after install:

```bash
wechat-cli sessions
```

```powershell
wechat-cli sessions
```

## Reset / Uninstall

Use dry-run before destructive cleanup:

```bash
./install.sh --clear-state --dry-run --json
./install.sh --uninstall --purge-state --dry-run --json
```

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -ClearState -DryRun -Json
powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1 -Uninstall -PurgeState -DryRun -Json
```

`--uninstall` removes installed files and watcher plist while preserving
key/state unless `--purge-state` is passed. `--clear-state` removes user state:
`~/.config/wxcli/config.json`, `~/.wechat-cli`, legacy state directories, logs,
the stored wxkey sudo credential, and any watcher that would recreate state.

Do not ask the user to manually delete key config, cache directories, logs, or
Keychain credentials.

## TCC Quiet Mode

After macOS install completes, advise the user once:

1. Open **System Settings -> Privacy & Security -> Full Disk Access**.
2. Add both `~/.local/share/wechat-cli/wechat-cli` and
   `~/.local/share/wechat-cli/wxkey`.

Without this, macOS 15+ may prompt on cross-container DB reads. The installer
does not install a launchd watcher by default for the same reason.

## Agent Defaults

- Use `wechat-cli --help`, `wechat-cli tools`, `wechat-cli tools --profile all`,
  `wechat-cli call <command-or-tool> --key value`, and
  `wechat-cli call-json <command-or-tool> '{...}'`
  as the stable interface. `tools` defaults to the high-signal assistant
  profile with slim canonical schemas; `--profile all` includes compatibility
  aliases, maintenance, and debug tools. `tool-schema <command>` is slim by
  default; use `tool-schema <command> --profile all` only when debugging
  compatibility fields.
- For strict local read-only work, run commands with
  `WECHAT_CLI_STRICT_READ_ONLY=1` or global `--strict-read-only`. This disables
  auto metadata/key refresh, media decode cache writes, voice transcript cache
  writes, `cache refresh/rebuild`, and `export`.
- Start a WeChat reading task with `wechat-cli agent --pretty` when you need
  the capability matrix, workflows, quality gates, and local readiness status.
  Use `wechat-cli status`, `wechat-cli coverage`, and `wechat-cli workflows`
  when only one slice is needed.
- Do not ask the user to run diagnostics manually. The agent runs CLI
  diagnostics and setup retries; the user only performs OS or WeChat GUI actions
  an agent cannot do.
- Do not manually run `cache refresh` before normal reads. Metadata-backed
  commands perform an internal refresh gate before returning data; use
  `cache status` only to inspect diagnostics and errors.
- Use `resolve-chat` before commands that accept human names when ambiguity matters.
- Use `timeline` as the normal chat-reading entrypoint. It resolves `chat`,
  reads live messages, defaults to `order=desc` plus `display_order=asc`, and
  returns `query` / `freshness` / `messages`. Use `query.has_more` and
  `query.next_offset` to page through a whole chat.
- Use `tail` / `watch` for read-only incremental observation. With `chat` it
  returns message events whose `event.message` has the same shape as timeline
  rows; without `chat`, `--mode sessions` returns session/unread events. Reuse
  the returned `cursor` on the next call. Normal mode returns the standard
  envelope. CLI `--jsonl` and `--follow` emit newline-delimited event objects
  without the envelope; they still do not send messages or control the WeChat UI.
- Use `context` after `search` or `timeline` when you have a message
  `local_id`/`server_id` and need surrounding conversation. It returns the same
  agent message shape as timeline plus `context_role=before/anchor/after`.
- Use `search-context` when the user asks what happened around a keyword hit;
  it combines live FTS search with context windows. Use
  `timeline --before-message <local_id>` or `--after-message <local_id>` when a
  message id should become the paging cursor.
- Use `history --view agent` only when lower-level filters or ordering are
  needed. Do not use `fields=full` for normal user-facing chat reading.
- Use `export` only when an explicit local file output is appropriate for large
  single-chat reads; it is a local file write, not part of the default assistant
  read-only tool shortlist.

Agent-ready message rows include:

`id(local_id/server_id_str/talker)`, `time`, `create_time`, `time_iso`, `sender`,
`sender_wxid`, `is_from_me`, `kind`, `text`, display-ready `images` / `videos` /
`files` / `link` / `music` / `miniprogram` / `forward_chat` / `quote` /
`transfer` / `red_packet` / `location` / `voice.transcript` / `solitaire`, and
concise `warnings`.
`media` remains path/resource-first, but includes `id`, `time`, `time_iso`,
`sender`, and `kind` so agents can correlate media paths back to timeline rows
without asking the CLI to interpret image contents.

Default output follows the WeChat UI principle: if a human sees information in
WeChat and it affects understanding, return it by default. Implementation
materials such as protocol codes, CDN/aeskey, raw XML, unreadable `.dat`, raw
SILK, and duplicate candidate paths belong in `include_debug=true`,
`fields=full`, or `media --debug`.

Images/videos in agent view should expose directly readable local `path` values
only. Voice rows are read from `media_0.db` / `VoiceInfo`; wechat-cli attempts
local transcription with `faster-whisper large-v3` first and returns
`voice.transcript` by default. If no external `silk_v3_decoder` exists, the
ASR venv's `pysilk` module is the fallback SILK decoder. Raw SILK/WAV paths
stay in debug/media output.

wechat-cli best-effort decodes local image `.dat` files into
`~/.wechat-cli/media-cache`; if WeChat V4 image `image_key` or `image_xor_key`
is missing/stale, it runs `wxkey image-key`, writes refreshed key material to
config, and retries decoding. It does not perform visual recognition.

## Failure Handling

- If macOS key setup fails, inspect installer `blocked_by` / `next_action`, then
  run `./wxkey doctor` if needed. Do not suggest disabling SIP.
- If macOS key setup returns partial key coverage, run `./wxkey doctor`, identify
  missing DB paths, ask the user to open only the corresponding chats/pages in
  WeChat, wait briefly, then rerun `./wxkey bootstrap`.
- If Windows key setup fails, inspect installer `blocked_by` / `next_action`,
  confirm WeChat/Weixin is logged in, confirm `WECHAT_CLI_DB_ROOT` points to the
  account directory that directly contains `db_storage`, and rerun
  `.\wechat-cli.exe cache refresh --force`.
- If a display-name lookup fails, call `resolve-chat` and pass the returned raw
  `username` through `--talker` or `--chat`.
- Treat `freshness`, `warnings`, `errors[]`, `parse_error`, and missing
  enrichment fields as actionable diagnostics, not prose.
- For release validation against a real logged-in WeChat, run
  `scripts/wechat-read-regression.sh` with `WECHAT_READ_TEST_CHAT` and
  `WECHAT_READ_TEST_KEYWORD`. It exercises `agent/status/coverage/workflows`,
  `resolve-chat`, `sessions`, `timeline`, `context`, timeline anchor paging,
  `tail`, `search-context`, manual search context, `media`, `members`, and
  `export`.
