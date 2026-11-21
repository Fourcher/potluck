#!/bin/sh
# Builds Potluck for every supported kind of computer into dist/.
# The programs are self-contained: no installer, nothing else to download to start them.
#
#   ./build.sh [version]
#
# On a Mac this also gives the Mac programs a signature and builds dist/Potluck.app.
# Set POTLUCK_SIGN_ID to a "Developer ID Application: ..." identity to sign for distribution;
# without it the signature is a local (ad-hoc) one, which is enough to run and to be allowed
# through the macOS firewall, but still triggers the "unidentified developer" warning.
set -e
cd "$(dirname "$0")"
VERSION="${1:-1.6.0}"
mkdir -p dist
for t in windows/amd64 windows/arm64 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${t%/*}; arch=${t#*/}; out="dist/potluck-$os-$arch"; [ "$os" = windows ] && out="$out.exe"
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" .
  echo "built $out"
done

if [ "$(uname -s)" = Darwin ] && command -v codesign >/dev/null 2>&1; then
  ID="${POTLUCK_SIGN_ID:--}"
  # Go signs Apple Silicon programs itself but leaves Intel ones unsigned, and macOS will not
  # honour a firewall permission for unsigned code.
  codesign -s "$ID" -f dist/potluck-darwin-amd64 dist/potluck-darwin-arm64 2>/dev/null
  echo "signed the Mac programs ($([ "$ID" = - ] && echo "local signature" || echo "$ID"))"

  # Potluck.app: one double-clickable app for both kinds of Mac. As a real app, macOS asks
  # for Local Network permission by name instead of blocking it silently.
  APP=dist/Potluck.app
  rm -rf "$APP"; mkdir -p "$APP/Contents/MacOS"
  lipo -create -output "$APP/Contents/MacOS/potluck" dist/potluck-darwin-arm64 dist/potluck-darwin-amd64
  # The other platforms' programs travel inside the app so it can hand them to helpers.
  mkdir -p "$APP/Contents/Resources/kit"
  for f in dist/potluck-windows-amd64.exe dist/potluck-windows-arm64.exe dist/potluck-linux-amd64 dist/potluck-linux-arm64 dist/potluck-darwin-arm64 dist/potluck-darwin-amd64; do
    cp "$f" "$APP/Contents/Resources/kit/"
  done
  cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>Potluck</string>
  <key>CFBundleDisplayName</key><string>Potluck</string>
  <key>CFBundleIdentifier</key><string>io.github.potluck.app</string>
  <key>CFBundleExecutable</key><string>potluck</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>LSUIElement</key><true/>
  <key>NSLocalNetworkUsageDescription</key><string>Potluck connects to your other computers on this network to share one AI model between them.</string>
</dict></plist>
PLIST
  if [ "$ID" = - ]; then
    codesign -s - -f --deep "$APP" 2>/dev/null
  else
    codesign -s "$ID" -f --deep --options runtime --timestamp "$APP"
  fi
  echo "built $APP"

  # Notarization, so a downloaded Potluck.app opens without a warning. Needs a Developer ID
  # signature (above) and credentials stored once with:
  #   xcrun notarytool store-credentials potluck --apple-id <you> --team-id <team> --password <app-specific password>
  # then run:  POTLUCK_SIGN_ID="Developer ID Application: ..." POTLUCK_NOTARY_PROFILE=potluck ./build.sh
  if [ "$ID" != - ] && [ -n "${POTLUCK_NOTARY_PROFILE:-}" ]; then
    ditto -c -k --keepParent "$APP" dist/Potluck-notarize.zip
    xcrun notarytool submit dist/Potluck-notarize.zip --keychain-profile "$POTLUCK_NOTARY_PROFILE" --wait
    xcrun stapler staple "$APP"
    rm -f dist/Potluck-notarize.zip
    echo "notarized $APP"
  fi
fi
