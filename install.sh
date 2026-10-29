#!/bin/sh
# Install the cody CLI from GitHub Releases on macOS or Linux.
#
#   curl -fsSL https://github.com/ajhous44/cody/releases/latest/download/install.sh | sh
#
# Environment (all optional):
#   CODY_VERSION      release tag to install, e.g. v3.0.0 (default: latest)
#   CODY_INSTALL_DIR  destination directory (default: ~/.local/bin)
#   CODY_RELEASES     releases base URL, for mirrors and tests
#                     (default: https://github.com/ajhous44/cody/releases)
set -eu

releases="${CODY_RELEASES:-https://github.com/ajhous44/cody/releases}"
dir="${CODY_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	echo "cody install: $*" >&2
	exit 1
}
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is required"; }
need curl
need tar

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported OS $(uname -s); on Windows use install.ps1" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported CPU $(uname -m)" ;;
esac
# A shell running under Rosetta reports x86_64; install the native build.
if [ "$os" = darwin ] && [ "$(sysctl -n hw.optional.arm64 2>/dev/null)" = 1 ]; then
	arch=arm64
fi

version="${CODY_VERSION:-latest}"
if [ "$version" = latest ]; then
	# /releases/latest redirects to /releases/tag/<tag>: no API token or JSON.
	version="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$releases/latest")" ||
		fail "could not reach $releases"
	version="${version##*/}"
fi
case "$version" in v*) ;; *) version="v$version" ;; esac
case "$version" in v[0-9]*) ;; *) fail "could not find a release (got '$version')" ;; esac
asset="cody_${version#v}_${os}_${arch}.tar.gz"
url="$releases/download/$version"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
trap 'exit 130' INT TERM
echo "downloading cody $version ($os/$arch)"
curl -fsSL -o "$tmp/$asset" "$url/$asset" || fail "no $asset in release $version"
curl -fsSL -o "$tmp/checksums.txt" "$url/checksums.txt" || fail "no checksums.txt in release $version"

want="$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/checksums.txt")"
[ -n "$want" ] || fail "$asset is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
	got="$(sha256sum "$tmp/$asset" | awk '{ print $1 }')"
else
	need shasum
	got="$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')"
fi
[ "$got" = "$want" ] || fail "checksum mismatch for $asset; nothing was installed"

tar -xzf "$tmp/$asset" -C "$tmp" cody
mkdir -p "$dir"
# Rename into place so a running cody keeps working until it exits.
cp "$tmp/cody" "$dir/.cody.new"
chmod 755 "$dir/.cody.new"
mv -f "$dir/.cody.new" "$dir/cody"
echo "installed $("$dir/cody" version) to $dir/cody"
case ":$PATH:" in
*":$dir:"*) ;;
*) echo "add it to your PATH: export PATH=\"$dir:\$PATH\" (in ~/.zshrc or ~/.bashrc)" ;;
esac
