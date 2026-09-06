#!/bin/bash
# sign-and-notarize.sh — 用 Apple Developer ID 对 DshShell.app 签名并公证
#
# 当前 build-and-install.sh 使用 ad-hoc 签名（codesign --sign -），仅能在本机
# 运行。要分发给其他 Mac，需要：
#   1. Apple Developer 账号（付费），注册 App ID 与 Developer ID Application 证书
#   2. 本脚本完成：Developer ID 签名 + notarytool 公证 + stapler 盖章
#
# 用法（建议先在 xcode 里把证书装进钥匙串）：
#   APPLE_ID="you@example.com" \
#   APPLE_TEAM_ID="XXXXXXXXXX" \
#   DEVELOPER_CERT="Developer ID Application: Your Name (TEAMID)" \
#   ./scripts/sign-and-notarize.sh
#
# notarytool 也可用 API Key（--key --key-id --team-id），见 xcrun notarytool 文档。
set -euo pipefail
export PATH="/usr/local/go/bin:/opt/homebrew/bin:${PATH:-}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

APP_NAME="${APP_NAME:-DshShell}"
APP_DEST="/Applications/${APP_NAME}.app"
APP_BUILD="${PWD}/bin/${APP_NAME}.app"
ZIP="${PWD}/bin/${APP_NAME}-notarize.zip"

APPLE_ID="${APPLE_ID:?需要 APPLE_ID（开发者账号邮箱）}"
APPLE_TEAM_ID="${APPLE_TEAM_ID:?需要 APPLE_TEAM_ID（Team ID）}"
DEVELOPER_CERT="${DEVELOPER_CERT:?需要 DEVELOPER_CERT（钥匙串里的 Developer ID Application 证书名）}"

echo "==> 1/6 先生产构建（含已修改的代码）"
./scripts/build-and-install.sh || true   # 先有 /Applications 版；下面从源码重新组 bundle
go build -tags production -trimpath -buildvcs=false -ldflags="-w -s" -o bin/dsh-desktop .
rm -rf "$APP_BUILD"
mkdir -p "$APP_BUILD/Contents/MacOS" "$APP_BUILD/Contents/Resources"
cp bin/dsh-desktop "$APP_BUILD/Contents/MacOS/${APP_NAME}"
cp build/darwin/icon.icns "$APP_BUILD/Contents/Resources/icon.icns"
cp build/darwin/Info.plist "$APP_BUILD/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleExecutable ${APP_NAME}" "$APP_BUILD/Contents/Info.plist" 2>/dev/null || true
/usr/libexec/PlistBuddy -c "Set :CFBundleName ${APP_NAME}" "$APP_BUILD/Contents/Info.plist" 2>/dev/null || true
/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier com.dsh.desktop" "$APP_BUILD/Contents/Info.plist" 2>/dev/null || true

echo "==> 2/6 Developer ID 签名（含 runtime 加固，公证必需）"
codesign --force --deep --options runtime --sign "$DEVELOPER_CERT" \
  --timestamp "$APP_BUILD"

echo "==> 3/6 校验签名"
codesign --verify --deep --strict --verbose=2 "$APP_BUILD"

echo "==> 4/6 打包并提交公证"
rm -f "$ZIP"
ditto -c -k --keepParent "$APP_BUILD" "$ZIP"
xcrun notarytool submit "$ZIP" \
  --apple-id "$APPLE_ID" --team-id "$APPLE_TEAM_ID" \
  --wait

echo "==> 5/6 盖章（stapler）"
xcrun stapler staple "$APP_BUILD"

echo "==> 6/6 安装到 /Applications"
pkill -f "${APP_DEST}/Contents/MacOS/${APP_NAME}" 2>/dev/null || true
rm -rf "$APP_DEST"
cp -R "$APP_BUILD" "$APP_DEST"
chmod -R a+rX "$APP_DEST"
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP_DEST" 2>/dev/null || true
open "$APP_DEST"

echo "✔ 签名+公证完成并已启动: $APP_DEST"
echo "  其他 Mac 用户首次打开时右键→打开 或 Gatekeeper 放行即可（Developer ID 公证后通常不再提示）"
