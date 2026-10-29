#!/usr/bin/env bash
# Runs install.sh (macOS/Linux) or install.ps1 (Windows, both PowerShells)
# against a local release mirror: a real build must install and run, and a
# tampered archive must be refused without installing anything.
set -euo pipefail

root="$(cd "$(dirname "$0")/../.." && pwd)"
version=9.9.9
os="$(go env GOOS)"
arch="$(go env GOARCH)"
py=python3
exe=cody
if [ "$os" = windows ]; then
	py=python
	exe=cody.exe
fi
tmp="$(mktemp -d)"
trap 'kill "${server:-}" 2>/dev/null || true; rm -rf "$tmp"' EXIT

rel="$tmp/mirror/download/v$version"
mkdir -p "$rel" "$tmp/build"
(cd "$root" && go build -ldflags "-X github.com/ajhous44/cody/v3/internal/cli.Version=$version" -o "$tmp/build/$exe" .)
asset="cody_${version}_${os}_${arch}"
if [ "$os" = windows ]; then
	asset="$asset.zip"
	(cd "$tmp/build" && "$py" -m zipfile -c "$rel/$asset" "$exe")
else
	asset="$asset.tar.gz"
	tar -czf "$rel/$asset" -C "$tmp/build" "$exe"
fi
sha() { "$py" -c 'import hashlib, sys; print(hashlib.sha256(open(sys.argv[1], "rb").read()).hexdigest())' "$1"; }
echo "$(sha "$rel/$asset")  $asset" >"$rel/checksums.txt"

port=18766
(cd "$tmp/mirror" && exec "$py" -m http.server --bind 127.0.0.1 "$port") >/dev/null 2>&1 &
server=$!
disown "$server" # no "Terminated" notice when the trap stops it
for _ in $(seq 50); do
	curl -fs "http://127.0.0.1:$port/" >/dev/null && break
	sleep 0.2
done

native() { if [ "$os" = windows ]; then cygpath -w "$1"; else echo "$1"; fi; }
install_with() { # install_with <dir> <shell> <args...>
	CODY_RELEASES="http://127.0.0.1:$port" CODY_VERSION="v$version" CODY_INSTALL_DIR="$(native "$1")" \
		"$2" "${@:3}"
}
run_installers() { # run_installers <dir> calls each installer for this OS
	if [ "$os" = windows ]; then
		install_with "$1/pwsh" pwsh -NoProfile -File "$(native "$root/install.ps1")"
		install_with "$1/powershell" powershell -NoProfile -ExecutionPolicy Bypass -File "$(native "$root/install.ps1")"
	else
		install_with "$1/sh" sh "$root/install.sh"
	fi
}

run_installers "$tmp/good"
for bin in "$tmp"/good/*/"$exe"; do
	"$bin" version | grep -qx "cody $version" || { echo "FAIL: $bin did not run" >&2; exit 1; }
done
echo "ok: installer put a working cody $version in place"

printf 'x' >>"$rel/$asset"
if run_installers "$tmp/bad" 2>"$tmp/err"; then
	echo "FAIL: installer accepted a tampered archive" >&2
	exit 1
fi
grep -q "checksum mismatch" "$tmp/err" || { cat "$tmp/err" >&2; echo "FAIL: wrong error for a tampered archive" >&2; exit 1; }
if ls "$tmp"/bad/*/"$exe" >/dev/null 2>&1; then
	echo "FAIL: tampered archive was installed" >&2
	exit 1
fi
echo "ok: tampered archive refused"
