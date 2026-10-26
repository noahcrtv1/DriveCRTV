#!/usr/bin/env bash
# Gera dist/DriveCRTV.app (Apple Silicon + Intel) e dist/DriveCRTV-mac.zip.
# Chave de API opcional embutida: DRIVE_API_KEY=... ./packaging/build.sh
set -euo pipefail
cd "$(dirname "$0")/.."
OUT=dist
APP="$OUT/DriveCRTV.app"
VER=$(grep -o 'appVersion = "[^"]*"' config.go | cut -d'"' -f2)
rm -rf "$OUT" && mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources/bin"
LDF="-s -w -X main.defaultAPIKey=${DRIVE_API_KEY:-}"
for arch in arm64 amd64; do
  CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "$LDF" -o "$APP/Contents/Resources/bin/drivecrtv-$arch" .
done
cat > "$APP/Contents/MacOS/DriveCRTV" <<'SH'
#!/bin/bash
# Escolhe o binário certo (Apple Silicon ou Intel) e vira o próprio app.
DIR="$(cd "$(dirname "$0")/../Resources/bin" && pwd)"
if [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" = "1" ]; then BIN="$DIR/drivecrtv-arm64"; else BIN="$DIR/drivecrtv-amd64"; fi
exec "$BIN"
SH
chmod 755 "$APP/Contents/MacOS/DriveCRTV" "$APP"/Contents/Resources/bin/*
cat > "$APP/Contents/Info.plist" <<PL
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleExecutable</key><string>DriveCRTV</string>
<key>CFBundleIdentifier</key><string>com.lipenoe.drivecrtv</string>
<key>CFBundleName</key><string>DriveCRTV</string>
<key>CFBundleDisplayName</key><string>DriveCRTV</string>
<key>CFBundleIconFile</key><string>AppIcon</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$VER</string>
<key>CFBundleVersion</key><string>$VER</string>
<key>LSMinimumSystemVersion</key><string>11.0</string>
<key>LSApplicationCategoryType</key><string>public.app-category.utilities</string>
<key>NSHighResolutionCapable</key><true/>
<key>NSSupportsAutomaticGraphicsSwitching</key><true/>
<key>NSAppTransportSecurity</key><dict><key>NSAllowsLocalNetworking</key><true/><key>NSAllowsArbitraryLoadsInWebContent</key><true/></dict>
</dict></plist>
PL
python3 packaging/make_icon.py "$APP/Contents/Resources/AppIcon.icns"
mv "$APP/Contents/Resources/AppIcon-preview.png" "$OUT/icon-preview.png"
cp packaging/LEIA-ME.txt "$OUT/LEIA-ME.txt"
(cd "$OUT" && rm -f DriveCRTV-mac.zip && zip -qry DriveCRTV-mac.zip DriveCRTV.app LEIA-ME.txt)
echo "pronto: $OUT/DriveCRTV-mac.zip"
