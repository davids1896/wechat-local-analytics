#!/bin/bash
# Build a distribution zip: wechat-cli + wxkey binaries + local libWCDB.dylib +
# install.sh + one-line release helper + docs. Friend/agent 解压后跑
# `./install.sh --yes --json` 安装 CLI；需要首次 key/cache 初始化时再跑
# `./install.sh --all --yes --json`.
# 前提: 若目标机器没有现成 schema-2 key map, ./install.sh --all 会跑
# ./wxkey bootstrap; 它会走 no-SIP + Keychain sudo + ad-hoc 重签路线完成首次
# key 初始化. wechat-cli 运行时解密不要求关闭 SIP.
set -euo pipefail

VERSION="${1:-}"
SRCDIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SRCDIR"

SOURCE_VERSION="$(sed -nE 's/^[[:space:]]*appVersion[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' cmd/wechat-cli/product.go | head -n 1)"
[[ -n "$SOURCE_VERSION" ]] || { echo "ERROR: could not read appVersion from cmd/wechat-cli/product.go" >&2; exit 1; }
if [[ -z "$VERSION" ]]; then
  VERSION="$SOURCE_VERSION"
fi
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "ERROR: version must be semantic numeric form such as 1.6.20 or 1.6.20-rc.1" >&2
  exit 1
fi
if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
  echo "ERROR: macOS release packages must be built on macOS arm64" >&2
  exit 1
fi

if [[ "$SOURCE_VERSION" != "$VERSION" ]]; then
  echo "ERROR: package version $VERSION does not match appVersion $SOURCE_VERSION" >&2
  exit 1
fi
if [[ "${WECHAT_CLI_ALLOW_UNTAGGED_PACKAGE:-0}" != "1" ]]; then
  command -v git >/dev/null 2>&1 || { echo "ERROR: git is required to verify the release source tag" >&2; exit 1; }
  git rev-parse --git-dir >/dev/null 2>&1 || { echo "ERROR: release packaging requires a git checkout" >&2; exit 1; }
  TAG="$(git describe --tags --exact-match HEAD 2>/dev/null || true)"
  [[ "$TAG" == "v$VERSION" ]] || { echo "ERROR: release packaging requires HEAD at tag v$VERSION" >&2; exit 1; }
  if [[ -n "$(git status --porcelain --untracked-files=normal)" ]]; then
    echo "ERROR: release packaging requires a clean worktree" >&2
    exit 1
  fi
fi

DYLIB_SRC="${WECHAT_CLI_WCDB_DYLIB:-$SRCDIR/lib/libWCDB.dylib}"
if [[ ! -f "$DYLIB_SRC" ]]; then
  echo "ERROR: libWCDB.dylib missing — set WECHAT_CLI_WCDB_DYLIB or place it at $SRCDIR/lib/libWCDB.dylib" >&2
  exit 1
fi

WXKEY_SRC="${WXKEY_SRC:-$SRCDIR/../wxkey}"
WXKEY_RELEASE_TAG="v1.4.8"
WXKEY_GO_INSTALL_OVERRIDE="${WXKEY_GO_INSTALL:-}"
WXKEY_GO_INSTALL="github.com/r266-tech/wxkey/cmd/wxkey@${WXKEY_RELEASE_TAG}"
# Local development only; release builds must prove the sibling source identity.
if [[ "${WECHAT_CLI_ALLOW_UNTAGGED_WXKEY:-0}" == "1" && -n "$WXKEY_GO_INSTALL_OVERRIDE" ]]; then
  WXKEY_GO_INSTALL="$WXKEY_GO_INSTALL_OVERRIDE"
fi
if [[ -d "$WXKEY_SRC" && "${WECHAT_CLI_ALLOW_UNTAGGED_WXKEY:-0}" != "1" ]]; then
  git -C "$WXKEY_SRC" rev-parse --git-dir >/dev/null 2>&1 || {
    echo "ERROR: local wxkey source must be a git checkout at $WXKEY_RELEASE_TAG" >&2
    exit 1
  }
  WXKEY_HEAD_TAG="$(git -C "$WXKEY_SRC" describe --tags --exact-match HEAD 2>/dev/null || true)"
  [[ "$WXKEY_HEAD_TAG" == "$WXKEY_RELEASE_TAG" ]] || {
    echo "ERROR: local wxkey source must have HEAD exactly at tag $WXKEY_RELEASE_TAG" >&2
    exit 1
  }
  if [[ -n "$(git -C "$WXKEY_SRC" status --porcelain --untracked-files=normal)" ]]; then
    echo "ERROR: local wxkey source must be clean at tag $WXKEY_RELEASE_TAG" >&2
    exit 1
  fi
fi

DIST="$SRCDIR/dist/wechat-cli-v${VERSION}-darwin-arm64"
rm -rf "$DIST" && mkdir -p "$DIST"

echo "→ building wechat-cli binary..."
# -trimpath strips build-host absolute paths from the binary; -ldflags "-s -w"
# strips symbol/debug tables so release binaries do not leak the build
# environment (e.g. /Users/<dev>/... or Go module cache locations).
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$DIST/wechat-cli" ./cmd/wechat-cli
chmod +x "$DIST/wechat-cli"
[[ "$(file "$DIST/wechat-cli")" == *"Mach-O 64-bit executable arm64"* ]] || { echo "ERROR: wechat-cli is not darwin arm64" >&2; exit 1; }
"$DIST/wechat-cli" --version | grep -Fq '"version":"'"$VERSION"'"' || { echo "ERROR: built CLI version does not match $VERSION" >&2; exit 1; }

echo "→ building wxkey binary..."
if [[ -d "$WXKEY_SRC" ]]; then
  ( cd "$WXKEY_SRC" && GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$DIST/wxkey" ./cmd/wxkey )
else
  GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 GOFLAGS="-trimpath -ldflags=-s -w" GOBIN="$DIST" go install "$WXKEY_GO_INSTALL"
fi
chmod +x "$DIST/wxkey"
[[ "$(file "$DIST/wxkey")" == *"Mach-O 64-bit executable arm64"* ]] || { echo "ERROR: wxkey is not darwin arm64" >&2; exit 1; }

echo "→ bundling libWCDB.dylib ($(du -h "$DYLIB_SRC" | cut -f1))..."
cp "$DYLIB_SRC" "$DIST/libWCDB.dylib"
[[ "$(file "$DIST/libWCDB.dylib")" == *"Mach-O"* && "$(file "$DIST/libWCDB.dylib")" == *"arm64"* ]] || { echo "ERROR: libWCDB.dylib is not a Mach-O arm64 library" >&2; exit 1; }

echo "→ copying docs..."
cp README.md llms.txt LICENSE SECURITY.md THIRD_PARTY_NOTICES.md AGENTS.md "$DIST/"
mkdir -p "$DIST/scripts"
cp scripts/install-release.sh "$DIST/scripts/"
cp scripts/wechat-read-regression.sh "$DIST/scripts/"
chmod +x "$DIST/scripts/wechat-read-regression.sh"

echo "→ copying installer..."
cp install.sh "$DIST/"
chmod +x "$DIST/install.sh"

echo "→ staging standalone release bootstraps..."
cp scripts/install-release.sh dist/install-release.sh
cp scripts/install-release.ps1 dist/install-release.ps1
chmod +x dist/install-release.sh

echo "→ zipping..."
cd dist
rm -f "wechat-cli-v${VERSION}-darwin-arm64.zip" \
  "wechat-cli-v${VERSION}-darwin-arm64.zip.sha256" \
  "wechat-cli-latest-darwin-arm64.zip" \
  "wechat-cli-latest-darwin-arm64.zip.sha256"
zip -qr "wechat-cli-v${VERSION}-darwin-arm64.zip" "wechat-cli-v${VERSION}-darwin-arm64"
shasum -a 256 "wechat-cli-v${VERSION}-darwin-arm64.zip" > "wechat-cli-v${VERSION}-darwin-arm64.zip.sha256"
cp "wechat-cli-v${VERSION}-darwin-arm64.zip" "wechat-cli-latest-darwin-arm64.zip"
shasum -a 256 "wechat-cli-latest-darwin-arm64.zip" > "wechat-cli-latest-darwin-arm64.zip.sha256"
shasum -a 256 "install-release.sh" > "install-release.sh.sha256"
shasum -a 256 "install-release.ps1" > "install-release.ps1.sha256"

echo
echo "✓ dist/wechat-cli-v${VERSION}-darwin-arm64.zip"
ls -lh "wechat-cli-v${VERSION}-darwin-arm64.zip"
echo "✓ dist/wechat-cli-v${VERSION}-darwin-arm64.zip.sha256"
echo "✓ dist/wechat-cli-latest-darwin-arm64.zip"
echo "✓ dist/install-release.sh + .sha256"
echo "✓ dist/install-release.ps1 + .sha256"
