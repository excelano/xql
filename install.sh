#!/bin/sh
# xql installer — fetches the latest release binary for the host platform
# and drops it into /usr/local/bin (or ~/.local/bin if /usr/local/bin is not
# writable). POSIX sh, no bash extensions.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/excelano/xql/main/install.sh | sh
#
# Environment variables:
#   XQL_INSTALL_DIR   Override install directory (e.g. /opt/bin or $HOME/bin)
#   XQL_VERSION       Install a specific release tag (e.g. v1.0.0) instead of latest

set -eu

REPO="excelano/xql"

say() { printf '%s\n' "$*" >&2; }
err() { say "error: $*"; exit 1; }

need_cmd() {
	if ! command -v "$1" >/dev/null 2>&1; then
		err "this installer needs '$1' on PATH; please install it and re-run"
	fi
}

need_cmd curl
need_cmd tar
need_cmd uname

detect_platform() {
	OS=$(uname -s | tr '[:upper:]' '[:lower:]')
	ARCH=$(uname -m)
	case "$OS" in
		linux|darwin) ;;
		*) err "unsupported OS: $OS (xql ships linux + darwin binaries)";;
	esac
	case "$ARCH" in
		x86_64|amd64) ARCH=amd64 ;;
		aarch64|arm64) ARCH=arm64 ;;
		*) err "unsupported architecture: $ARCH";;
	esac
	PLATFORM="${OS}_${ARCH}"
}

resolve_version() {
	if [ -n "${XQL_VERSION:-}" ]; then
		VERSION="$XQL_VERSION"
		say "Installing xql $VERSION (pinned via XQL_VERSION)"
		return
	fi
	# Resolve the latest tag via the GitHub API. The web /releases/latest
	# redirect is cached at GitHub's edge for several minutes after a new
	# release, which made re-running this script right after a tag silently
	# install the previous version. The API is real-time. Anonymous calls
	# are rate-limited to 60/hour per IP, which is fine for a human-run
	# installer.
	VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
		| awk -F'"' '/"tag_name":/ { print $4; exit }')
	if [ -z "${VERSION:-}" ]; then
		err "could not resolve latest release tag from GitHub"
	fi
	say "Installing xql $VERSION (latest)"
}

detect_existing() {
	EXISTING_PATH=""
	EXISTING_DIR=""
	if command -v xql >/dev/null 2>&1; then
		EXISTING_PATH=$(command -v xql)
		EXISTING_DIR=$(dirname "$EXISTING_PATH")
	fi
}

pick_install_dir() {
	if [ -n "${XQL_INSTALL_DIR:-}" ]; then
		INSTALL_DIR="$XQL_INSTALL_DIR"
	elif [ -n "$EXISTING_DIR" ]; then
		# An existing install wins over the default — upgrade in place rather
		# than scattering a second copy into a directory earlier on PATH.
		INSTALL_DIR="$EXISTING_DIR"
		say "Existing install at $EXISTING_PATH — upgrading in place"
	elif [ -w /usr/local/bin ] 2>/dev/null; then
		INSTALL_DIR=/usr/local/bin
	else
		# /usr/local/bin needs root; fall back to a user-writable spot.
		INSTALL_DIR="$HOME/.local/bin"
	fi
	mkdir -p "$INSTALL_DIR" || err "cannot create install dir $INSTALL_DIR"
	# Many users land here because they tried `sudo curl ... | sh`, which only
	# sudoes curl, not the sh that's writing the binary. Give them the literal
	# correct command (sudo wraps sh, not curl) so they don't have to figure
	# out which side of the pipe needs the privilege.
	if [ ! -w "$INSTALL_DIR" ]; then
		if [ -n "$EXISTING_DIR" ] && [ "$EXISTING_DIR" = "$INSTALL_DIR" ]; then
			err "existing install at $EXISTING_PATH is not writable; re-run as
       curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo sh"
		fi
		err "$INSTALL_DIR is not writable; either set XQL_INSTALL_DIR to a
       writable directory, or re-run as
       curl -fsSL https://raw.githubusercontent.com/${REPO}/main/install.sh | sudo sh"
	fi
	if [ -n "$EXISTING_DIR" ] && [ "$EXISTING_DIR" != "$INSTALL_DIR" ]; then
		say "Warning: xql already installed at $EXISTING_PATH"
		say "         New copy will land at $INSTALL_DIR/xql"
		say "         You will have two copies; PATH order decides which runs"
		say "         To remove the other one: xql uninstaller at"
		say "         https://raw.githubusercontent.com/excelano/xql/main/uninstall.sh"
	fi
}

download_and_install() {
	VERSION_NUM=${VERSION#v}
	ARCHIVE="xql_${VERSION_NUM}_${PLATFORM}.tar.gz"
	URL="https://github.com/${REPO}/releases/download/${VERSION}/${ARCHIVE}"
	CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

	TMPDIR=$(mktemp -d)
	trap 'rm -rf "$TMPDIR"' EXIT INT TERM

	say "Downloading $ARCHIVE"
	if ! curl -fsSL -o "$TMPDIR/$ARCHIVE" "$URL"; then
		err "download failed: $URL"
	fi

	say "Verifying checksum"
	if ! curl -fsSL -o "$TMPDIR/checksums.txt" "$CHECKSUMS_URL"; then
		err "could not fetch checksums.txt from release"
	fi
	EXPECTED=$(awk -v a="$ARCHIVE" '$2==a {print $1}' "$TMPDIR/checksums.txt")
	if [ -z "$EXPECTED" ]; then
		err "checksums.txt has no entry for $ARCHIVE"
	fi
	if command -v sha256sum >/dev/null 2>&1; then
		ACTUAL=$(sha256sum "$TMPDIR/$ARCHIVE" | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		ACTUAL=$(shasum -a 256 "$TMPDIR/$ARCHIVE" | awk '{print $1}')
	else
		err "need sha256sum or shasum to verify the download"
	fi
	if [ "$EXPECTED" != "$ACTUAL" ]; then
		err "checksum mismatch: expected $EXPECTED, got $ACTUAL"
	fi

	say "Extracting to $INSTALL_DIR"
	tar -xzf "$TMPDIR/$ARCHIVE" -C "$TMPDIR" xql
	# install(1) handles permissions and atomicity better than mv on most systems.
	if command -v install >/dev/null 2>&1; then
		install -m 0755 "$TMPDIR/xql" "$INSTALL_DIR/xql"
	else
		mv "$TMPDIR/xql" "$INSTALL_DIR/xql"
		chmod 0755 "$INSTALL_DIR/xql"
	fi
}

post_install_message() {
	say ""
	say "xql installed to $INSTALL_DIR/xql"
	case ":$PATH:" in
		*":$INSTALL_DIR:"*) ;;
		*) say "Note: $INSTALL_DIR is not on your PATH. Add it to your shell rc:"
		   say "    export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
	esac
	say ""
	say "Try it:"
	say "    xql --help"
}

detect_platform
detect_existing
resolve_version
pick_install_dir
download_and_install
post_install_message
