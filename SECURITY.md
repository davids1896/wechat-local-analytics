# Security

wechat-local-analytics is a local-first toolkit for reading and analyzing WeChat
data on a Windows PC or Mac controlled by the user running it. The CLI and
Wetrace adapter do not require a remote service. If their output is passed to
Codex or another agent, that platform's data handling and retention settings
apply.

## Sensitive Local State

The following files are intentionally local and must not be committed or shared:

- `~/.config/wxcli/config.json`: contains the wxid, DB root, and DB key material.
- `~/.wechat-cli/cache/`: contains plaintext snapshot DBs and `index.sqlite`.
- macOS wxkey sudo credential in Keychain: contains the user's stored sudo password for unattended no-SIP key refresh.
- `dist/`, `wechat-cli`, `wxkey`, and local `libWCDB.dylib` build artifacts.
- `~/wetrace-exports/`: may contain plaintext messages, names, statistics, and
  generated reports.

The repository `.gitignore` excludes the common local artifacts, but users should
still review `git status --short` before publishing changes.

## Permissions

wechat-cli reads local WeChat databases in readonly mode. First-run key extraction is
handled by the companion `wxkey` CLI and requests administrator privileges to
read the local WeChat process memory. The supported path is no-SIP only:
`wxkey bootstrap` stores the user's sudo password in macOS Keychain, prepares an
ad-hoc signed wechat-cli shadow copy of WeChat when the installed app is protected by
macOS app-management controls, and future key refreshes reuse the Keychain
credential through `sudo -S`.

Run these commands when diagnosing a new machine:

```bash
./install.sh --doctor --json
./wxkey doctor
./wechat-cli cache status
```

## Reporting Issues

Please avoid including message contents, DB keys, raw `config.json`, plaintext
cache files, generated Wetrace reports, or personal identifiers in public
issues. Redact local paths and account identifiers when possible.
