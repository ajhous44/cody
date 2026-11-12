#!/usr/bin/env bash
# Local builds are ad-hoc signed; release builds require Developer ID + notarization.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="${1:-$ROOT/dist}"
APP="$OUT/Cody.app"
VERSION="${CODY_VERSION:-3.0.0}"
ARCH="${CODY_ARCH:-$(uname -m)}"
IDENTITY="${CODY_SIGN_IDENTITY:--}"
PROFILE="${CODY_NOTARY_PROFILE:-}"
case "$ARCH" in
  universal) ARCHES=(arm64 x86_64) ;;
  amd64|x86_64) ARCHES=(x86_64) ;;
  arm64) ARCHES=(arm64) ;;
  *) echo "unsupported CODY_ARCH: $ARCH" >&2; exit 1 ;;
esac
if [[ -n "$PROFILE" && "$IDENTITY" == "-" ]]; then
  echo "notarization requires CODY_SIGN_IDENTITY (Developer ID Application)" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
for arch in "${ARCHES[@]}"; do
  goarch="$arch"
  [[ "$arch" != x86_64 ]] || goarch=amd64
  echo "building cody and app shell ($arch)..."
  (cd "$ROOT" && CGO_ENABLED=0 GOOS=darwin GOARCH="$goarch" go build -trimpath \
    -ldflags="-s -w -X github.com/ajhous44/cody/v3/internal/cli.Version=$VERSION" \
    -o "$WORK/engine-$arch" .)
  swiftc -O -swift-version 5 -target "${arch}-apple-macosx13.0" \
    -framework AppKit -framework WebKit -o "$WORK/shell-$arch" \
    "$ROOT/packaging/macos/CodyApp.swift"
done
lipo -create "$WORK"/engine-* -output "$APP/Contents/Resources/cody"
lipo -create "$WORK"/shell-* -output "$APP/Contents/MacOS/Cody"
lipo "$APP/Contents/Resources/cody" -verify_arch "${ARCHES[@]}"
lipo "$APP/Contents/MacOS/Cody" -verify_arch "${ARCHES[@]}"
sed "s/__VERSION__/$VERSION/g" "$ROOT/packaging/macos/Info.plist" > "$APP/Contents/Info.plist"
cp "$ROOT/packaging/icons/cody.icns" "$APP/Contents/Resources/cody.icns"

SIGN=(--force --sign "$IDENTITY")
if [[ "$IDENTITY" != "-" ]]; then SIGN+=(--options runtime --timestamp); fi
codesign "${SIGN[@]}" "$APP/Contents/Resources/cody"
codesign "${SIGN[@]}" "$APP/Contents/MacOS/Cody"
codesign "${SIGN[@]}" "$APP"
codesign --verify --deep --strict "$APP"

if [[ -n "$PROFILE" ]]; then
  ZIP="$OUT/Cody_${VERSION}_macos_${ARCH}.zip"
  ditto -c -k --keepParent "$APP" "$WORK/submit.zip"
  xcrun notarytool submit "$WORK/submit.zip" --keychain-profile "$PROFILE" --wait
  xcrun stapler staple "$APP"
  xcrun stapler validate "$APP"
  spctl --assess --type execute "$APP"
  ditto -c -k --keepParent "$APP" "$ZIP"
  (cd "$OUT" && shasum -a 256 "$(basename "$ZIP")" > "$(basename "$ZIP").sha256")
fi
echo "built $APP"
