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
VERSION="${1:-1.9.0}"
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

  # Potluck.app: the native app (macos/: menu bar item, chat and control panel windows,
  # notifications, updates) with the Potluck program for both kinds of Mac inside it. As a real
  # app, macOS asks for Local Network permission by name instead of blocking it silently.
  APP=dist/Potluck.app
  rm -rf "$APP"
  DD="${POTLUCK_MAC_BUILD:-$PWD/dist/mac-build}"
  (cd macos && xcodegen generate >/dev/null && xcodebuild build -project PotluckMac.xcodeproj -scheme Potluck -configuration Release \
     -derivedDataPath "$DD" MARKETING_VERSION="$VERSION" CODE_SIGNING_ALLOWED=NO -quiet)
  cp -R "$DD/Build/Products/Release/Potluck.app" "$APP"
  lipo -create -output "$APP/Contents/Resources/potluck" dist/potluck-darwin-arm64 dist/potluck-darwin-amd64
  # The other platforms' programs travel inside the app so it can hand them to helpers.
  mkdir -p "$APP/Contents/Resources/kit"
  for f in dist/potluck-windows-amd64.exe dist/potluck-windows-arm64.exe dist/potluck-linux-amd64 dist/potluck-linux-arm64 dist/potluck-darwin-arm64 dist/potluck-darwin-amd64; do
    cp "$f" "$APP/Contents/Resources/kit/"
  done
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
