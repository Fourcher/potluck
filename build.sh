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
VERSION="${1:-1.10.0}"
mkdir -p dist
for t in windows/amd64 windows/arm64 darwin/arm64 darwin/amd64 linux/amd64 linux/arm64; do
  os=${t%/*}; arch=${t#*/}; out="dist/potluck-$os-$arch"; [ "$os" = windows ] && out="$out.exe"
  GOOS=$os GOARCH=$arch CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o "$out" .
  echo "built $out"
done

# The Windows app people download (names that differ from the plain programs' names in more
# than upper and lower case: on a Mac's disk Potluck-windows-arm64.exe and
# potluck-windows-arm64.exe are the same file): installs itself for the person (no administrator), with a
# tray icon and its own window (desktop_windows.go). Fixed names, so a website can link to them.
for arch in amd64 arm64; do
  out=dist/Potluck-windows.exe; [ $arch = arm64 ] && out=dist/Potluck-windows-arm.exe
  GOOS=windows GOARCH=$arch CGO_ENABLED=0 go build -tags desktop -trimpath -ldflags "-s -w -H windowsgui -X main.version=$VERSION" -o "$out" .
  echo "built $out"
done
# The Linux one-line installer.
cp install.sh dist/install.sh

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
     -derivedDataPath "$DD" -destination "generic/platform=macOS" ARCHS="arm64 x86_64" ONLY_ACTIVE_ARCH=NO MARKETING_VERSION="$VERSION" CODE_SIGNING_ALLOWED=NO -quiet)
  case "$(lipo -archs "$DD/Build/Products/Release/Potluck.app/Contents/MacOS/Potluck")" in
    *arm64*x86_64* | *x86_64*arm64*) ;;
    *) echo "the app is not built for both Apple Silicon and Intel Macs"; exit 1 ;;
  esac
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
    # Inside out: every program in the app gets its own hardened, timestamped signature first
    # (codesign --deep skips programs under Resources, and Apple's check rejects unsigned ones).
    for f in "$APP/Contents/Resources/potluck" "$APP/Contents/Resources/kit/potluck-darwin-arm64" "$APP/Contents/Resources/kit/potluck-darwin-amd64"; do
      codesign -s "$ID" -f --options runtime --timestamp "$f"
    done
    codesign -s "$ID" -f --options runtime --timestamp "$APP"
    codesign --verify --strict --deep "$APP"
  fi
  echo "built $APP"

  # Notarization, so a downloaded Potluck opens without a warning. Needs a Developer ID
  # signature (above) and a notarytool keychain profile, stored once with:
  #   xcrun notarytool store-credentials potluck --key AuthKey_XXXX.p8 --key-id XXXX --issuer <issuer id>
  # then run:  POTLUCK_SIGN_ID="Developer ID Application: ..." POTLUCK_NOTARY_PROFILE=potluck ./build.sh
  if [ "$ID" != - ] && [ -n "${POTLUCK_NOTARY_PROFILE:-}" ]; then
    rm -f dist/Potluck-notarize.zip
    ditto -c -k --keepParent "$APP" dist/Potluck-notarize.zip
    xcrun notarytool submit dist/Potluck-notarize.zip --keychain-profile "$POTLUCK_NOTARY_PROFILE" --wait
    xcrun stapler staple "$APP"
    rm -f dist/Potluck-notarize.zip
    echo "notarized $APP"
  fi

  # What people download: a disk image with the app and a shortcut to Applications (fixed name,
  # so a website can always link to the newest one), and a zip the app's updater uses.
  rm -rf dist/dmg dist/Potluck-mac.dmg dist/Potluck-*-mac.zip
  mkdir -p dist/dmg && cp -R "$APP" dist/dmg/ && ln -s /Applications dist/dmg/Applications
  hdiutil create -quiet -volname Potluck -srcfolder dist/dmg -fs HFS+ -format UDZO dist/Potluck-mac.dmg
  rm -rf dist/dmg
  ditto -c -k --keepParent "$APP" "dist/Potluck-$VERSION-mac.zip"
  if [ "$ID" != - ]; then
    codesign -s "$ID" --timestamp dist/Potluck-mac.dmg
    if [ -n "${POTLUCK_NOTARY_PROFILE:-}" ]; then
      xcrun notarytool submit dist/Potluck-mac.dmg --keychain-profile "$POTLUCK_NOTARY_PROFILE" --wait
      xcrun stapler staple dist/Potluck-mac.dmg
    fi
  fi
  echo "built dist/Potluck-mac.dmg and dist/Potluck-$VERSION-mac.zip"
fi

# Checksums for everything a release offers (the installers and updaters check them).
(cd dist && rm -f SHA256SUMS.txt && for f in Potluck-mac.dmg Potluck-*-mac.zip Potluck-windows.exe Potluck-windows-arm.exe install.sh \
    potluck-windows-amd64.exe potluck-windows-arm64.exe potluck-darwin-arm64 potluck-darwin-amd64 potluck-linux-amd64 potluck-linux-arm64; do
  [ -f "$f" ] && shasum -a 256 "$f"
done > SHA256SUMS.txt)
echo "wrote dist/SHA256SUMS.txt"
