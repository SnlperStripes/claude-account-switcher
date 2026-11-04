#!/bin/bash
# Build an opt-in native macOS app without touching an installed copy.
set -euo pipefail
cd "$(dirname "$0")/.."
app="${1:-dist/Claude Account Switcher.app}"
if [ -e "$app" ]; then
    printf 'Output already exists: %s\nChoose a new output path.\n' "$app" >&2
    exit 1
fi
work=$(mktemp -d "${TMPDIR:-/tmp}/claude-switcher-build.XXXXXX")
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/app/Contents/MacOS" "$work/app/Contents/Resources"
archs="${ARCHS:-$(uname -m)}"
for arch in $archs; do
    case "$arch" in
        arm64) goarch=arm64 ;;
        x86_64) goarch=amd64 ;;
        *) printf 'Unsupported architecture: %s\n' "$arch" >&2; exit 1 ;;
    esac
    CGO_ENABLED=1 GOARCH="$goarch" go build -trimpath -o "$work/backend-$arch" .
    xcrun swiftc -swift-version 5 -parse-as-library -O \
        -module-cache-path "$work/swift-cache" \
        -target "$arch-apple-macosx13.0" native/Panel.swift -o "$work/panel-$arch"
done
xcrun lipo -create "$work"/backend-* -output "$work/app/Contents/MacOS/claude-account-switcher"
xcrun lipo -create "$work"/panel-* -output "$work/app/Contents/MacOS/switcher-panel"
version=$(sed -n 's/^const version = "\([^"]*\)"/\1/p' main.go)
cat > "$work/app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>claude-account-switcher</string>
<key>CFBundleIdentifier</key><string>io.github.snlperstripes.claude-account-switcher</string>
<key>CFBundleName</key><string>Claude Account Switcher</string>
<key>CFBundleDisplayName</key><string>Claude Account Switcher</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$version</string>
<key>CFBundleVersion</key><string>$version</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>LSUIElement</key><true/>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
cp LICENSE NOTICE "$work/app/Contents/Resources/"
codesign --force --sign - "$work/app/Contents/MacOS/switcher-panel"
codesign --force --sign - "$work/app"
codesign --verify --deep --strict "$work/app"
mkdir -p "$(dirname "$app")"
mv "$work/app" "$app"
printf 'Built %s (%s)\n' "$app" "$archs"
