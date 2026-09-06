#!/bin/bash
# build-and-install.sh — 生产构建 dsh-desktop 并安装到 /Applications
#
# 约定（用户要求）：后续所有构建产物自动安装到 /Applications，替换旧的
# DshShell.app（此前是 Swift 实现的 dsh 桌面壳，现由本 Wails 版本接管）。
#
# 用法:
#   ./scripts/build-and-install.sh            # 构建 + 安装 + 重启
#   APP_NAME="DshShell" ./scripts/build-and-install.sh   # 指定安装名（默认 DshShell）
set -euo pipefail

# Self-contained PATH: script may be run from Finder/automation where the
# shell rc isn't sourced, so ensure go and the common tool dirs are present.
export PATH="/usr/local/go/bin:/opt/homebrew/bin:/Users/chengy/.local/bin:/usr/local/bin:${PATH:-}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

APP_NAME="${APP_NAME:-DshShell}"
APP_BUNDLE_ID="${APP_BUNDLE_ID:-com.dsh.desktop}"
BIN_DIR="bin"
APP_DEST="/Applications/${APP_NAME}.app"

echo "==> 1/5 生产构建"
go build -tags production -trimpath -buildvcs=false -ldflags="-w -s" -o "${BIN_DIR}/dsh-desktop" .

echo "==> 2/5 组装 .app bundle"
APP_BUILD="${BIN_DIR}/${APP_NAME}.app"
rm -rf "$APP_BUILD"
mkdir -p "$APP_BUILD/Contents/MacOS" "$APP_BUILD/Contents/Resources"
cp "${BIN_DIR}/dsh-desktop" "$APP_BUILD/Contents/MacOS/${APP_NAME}"
cp build/darwin/icon.icns "$APP_BUILD/Contents/Resources/icon.icns"
cp build/darwin/Info.plist "$APP_BUILD/Contents/Info.plist"

# Info.plist 里 CFBundleExecutable / CFBundleName 按 APP_NAME 修正
/usr/libexec/PlistBuddy -c "Set :CFBundleExecutable ${APP_NAME}" "$APP_BUILD/Contents/Info.plist" 2>/dev/null || \
  /usr/libexec/PlistBuddy -c "Add :CFBundleExecutable string ${APP_NAME}" "$APP_BUILD/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleName ${APP_NAME}" "$APP_BUILD/Contents/Info.plist" 2>/dev/null || \
  /usr/libexec/PlistBuddy -c "Add :CFBundleName string ${APP_NAME}" "$APP_BUILD/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleIdentifier ${APP_BUNDLE_ID}" "$APP_BUILD/Contents/Info.plist" 2>/dev/null || \
  /usr/libexec/PlistBuddy -c "Add :CFBundleIdentifier string ${APP_BUNDLE_ID}" "$APP_BUILD/Contents/Info.plist"

echo "==> 3/5 停掉旧实例（如运行中）"
pkill -f "${APP_DEST}/Contents/MacOS/${APP_NAME}" 2>/dev/null || true
# 卸载 LaunchServices 对旧 bundle 的注册（若 bundle id 变了）
if [ -d "$APP_DEST" ]; then
  OLD_ID=$(/usr/libexec/PlistBuddy -c "Print :CFBundleIdentifier" "$APP_DEST/Contents/Info.plist" 2>/dev/null || echo "")
  if [ -n "$OLD_ID" ] && [ "$OLD_ID" != "$APP_BUNDLE_ID" ]; then
    echo "   旧 bundle id: $OLD_ID -> 新: $APP_BUNDLE_ID"
    /System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -u "$APP_DEST" 2>/dev/null || true
  fi
fi

echo "==> 4/5 安装到 /Applications"
rm -rf "$APP_DEST"
cp -R "$APP_BUILD" "$APP_DEST"
chmod -R a+rX "$APP_DEST"
codesign --force --deep --sign - "$APP_DEST"

echo "==> 5/5 注册并启动"
/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister -f "$APP_DEST" 2>/dev/null || true
open "$APP_DEST"

echo "✔ 已安装并启动: $APP_DEST"
echo "  配置目录: ~/Library/Application Support/dsh-desktop/config.json"
