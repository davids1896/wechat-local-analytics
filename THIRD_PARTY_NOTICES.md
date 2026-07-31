# Third-Party Notices

## r266-tech/wechat-cli

This repository preserves the Git history and substantial source code of:

- https://github.com/r266-tech/wechat-cli

The upstream project is licensed under the MIT License. Its copyright notice is
preserved in this repository's `LICENSE`.

## afumu/wetrace-skill

The Wetrace product direction and report categories were informed by:

- https://github.com/afumu/wetrace-skill

This repository does not bundle that project's original HTTP service. The
included Python adapter was developed to call local `wechat-cli call-json`
directly and to enforce strict read-only database access.

## Tencent WCDB

Source code does not commit third-party binary libraries.

Release or local installations may use `libWCDB.dylib` / `libWCDB.dll` so the
CLI can load Tencent WCDB at runtime:

- https://github.com/Tencent/wcdb
- https://github.com/Tencent/wcdb/blob/master/LICENSE

The WCDB library is loaded locally for read-only access to the user's own WeChat
databases.

## Optional voice transcription

`wechat-cli asr setup` may create a user-local Python virtual environment and
download packages from PyPI. These packages are not committed to this
repository:

- `faster-whisper` for local ASR
- `silk-python` / `pysilk` for local WeChat SILK decoding
