#!/usr/bin/env sh
# Switchboard installer. Downloads the latest release archive for this platform,
# verifies its SHA-256 checksum, and installs `sxb` (TUI) and `sxbd` (daemon).
#
#   curl -fsSL https://raw.githubusercontent.com/jamesclark123/switchboard/main/install.sh | sh
#
# Overrides (env): SWITCHBOARD_VERSION=vX.Y.Z (default: latest),
# SWITCHBOARD_INSTALL_DIR (default: /usr/local/bin, else ~/.local/bin).
#
# Binaries installed this way are self-update capable: run the TUI and press `u`
# to update the client and every connected host. Each remote host that runs the
# daemon must have `sxbd` installed too — re-run this script there.
set -eu

REPO="jamesclark123/switchboard"
PROJECT="switchboard"

err() { printf 'install: %s\n' "$1" >&2; exit 1; }

# --- detect platform ---
os=$(uname -s)
case "$os" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) err "unsupported OS: $os (supported: Linux, macOS)" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) err "unsupported architecture: $arch (supported: x86_64, arm64)" ;;
esac

asset="${PROJECT}_${os}_${arch}.tar.gz"

# --- resolve the download base (a specific tag, or /latest) ---
version="${SWITCHBOARD_VERSION:-}"
if [ -n "${SWITCHBOARD_BASE_URL:-}" ]; then
	base="$SWITCHBOARD_BASE_URL" # override for mirrors/testing
elif [ -n "$version" ]; then
	base="https://github.com/${REPO}/releases/download/${version}"
else
	base="https://github.com/${REPO}/releases/latest/download"
fi

# --- choose an install dir ---
install_dir="${SWITCHBOARD_INSTALL_DIR:-/usr/local/bin}"
sudo=""
if [ ! -d "$install_dir" ] || [ ! -w "$install_dir" ]; then
	if [ "$install_dir" = "/usr/local/bin" ]; then
		if command -v sudo >/dev/null 2>&1 && [ -d "$install_dir" ]; then
			sudo="sudo"
		else
			install_dir="$HOME/.local/bin"
			mkdir -p "$install_dir"
		fi
	else
		mkdir -p "$install_dir"
	fi
fi

# --- download + verify + install ---
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fetch() { # url dest
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		err "need curl or wget"
	fi
}

printf 'install: downloading %s\n' "$asset"
fetch "${base}/${asset}" "$tmp/$asset" ||
	err "could not download $asset from $base — the release may not exist or may have no build for ${os}/${arch} (check SWITCHBOARD_VERSION and your connection)"
fetch "${base}/checksums.txt" "$tmp/checksums.txt" ||
	err "could not download checksums.txt from $base"

printf 'install: verifying checksum\n'
expected=$(grep " ${asset}\$" "$tmp/checksums.txt" | awk '{print $1}' | head -n1)
[ -n "$expected" ] || err "no checksum listed for $asset"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$asset" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')
else
	err "need sha256sum or shasum to verify the download"
fi
[ "$actual" = "$expected" ] || err "checksum mismatch for $asset (got $actual, want $expected)"

tar -xzf "$tmp/$asset" -C "$tmp"
for bin in sxb sxbd; do
	[ -f "$tmp/$bin" ] || err "$bin missing from archive"
	$sudo install -m 0755 "$tmp/$bin" "$install_dir/$bin"
done

printf 'install: installed sxb and sxbd to %s\n' "$install_dir"
case ":$PATH:" in
	*":$install_dir:"*) ;;
	*) printf 'install: note: %s is not on your PATH — add it to use sxb/sxbd\n' "$install_dir" ;;
esac

cat <<EOF

Next steps:
  1. Start the daemon:   sxbd serve --boot     # start now + on every boot (Linux/systemd)
                         sxbd serve --watch    # or just run it in the background
  2. Launch the TUI:     sxb

Updating: run sxb and press 'u' to update the client and all connected hosts,
or re-run this script. Each remote host that runs sxbd must be updated there too.
EOF
