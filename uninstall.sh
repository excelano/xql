#!/bin/sh
# xql uninstaller — finds and removes the xql binary, with an optional
# follow-up step to remove the REPL history at ~/.config/xql/. POSIX sh,
# no bash extensions.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/excelano/xql/main/uninstall.sh | sh
#
# Environment variables:
#   XQL_UNINSTALL_YES=1  Skip the binary-removal confirmation (assume yes).
#                        Does NOT imply purge: the config dir is kept
#                        unless XQL_PURGE=1 is also set.
#   XQL_PURGE=1          Also remove ~/.config/xql/ (history, etc.),
#                        independent of XQL_UNINSTALL_YES.

set -eu

say() { printf '%s\n' "$*" >&2; }
err() { say "error: $*"; exit 1; }

# read_yes reads a y/N answer from the controlling terminal, not stdin,
# because this script is typically invoked as `curl ... | sh` where stdin
# is the script itself.
read_yes() {
	prompt="$1"
	if [ "${XQL_UNINSTALL_YES:-0}" = "1" ]; then
		return 0
	fi
	if [ ! -t 0 ] && [ ! -e /dev/tty ]; then
		err "no terminal available for confirmation; re-run with XQL_UNINSTALL_YES=1 to skip the prompt"
	fi
	printf '%s [y/N]: ' "$prompt" >&2
	if [ -e /dev/tty ]; then
		read ans </dev/tty
	else
		read ans
	fi
	case "$ans" in
		y|Y|yes|YES) return 0 ;;
		*) return 1 ;;
	esac
}

if ! command -v xql >/dev/null 2>&1; then
	say "xql is not on PATH; nothing to uninstall."
	say "If you installed to a custom location, remove it manually:"
	say "    rm /path/to/xql"
	exit 0
fi

TARGET=$(command -v xql)
say "Found xql at $TARGET"

if [ ! -w "$TARGET" ] && [ ! -w "$(dirname "$TARGET")" ]; then
	err "$TARGET is not writable; re-run with sudo to remove it"
fi

if ! read_yes "Remove $TARGET?"; then
	say "Aborted."
	exit 1
fi

rm -f "$TARGET" || err "could not remove $TARGET"
say "Removed $TARGET"

# Invalidate the shell's command hash; without this, `command -v` happily
# reports the just-deleted path as still present and the duplicate-install
# check below cries wolf.
hash -r 2>/dev/null || true

# Check for additional installs (e.g. one in /usr/local/bin plus one in
# ~/.local/bin). PATH lookup only finds the first; warn so the user knows
# the others remain.
LEFTOVER=$(command -v xql 2>/dev/null || true)
if [ -n "$LEFTOVER" ]; then
	say ""
	say "Note: another xql binary is still on PATH at $LEFTOVER"
	say "Re-run this uninstaller to remove it, or remove it manually."
fi

# Optional state cleanup, decoupled from the binary confirmation on purpose:
# XQL_UNINSTALL_YES means "don't ask me about the binary", NOT "delete my
# data". Purging the history is opt-in via XQL_PURGE=1 or an explicit
# interactive yes — skipping prompts should never silently drop state.
CONFIG_DIR="${XDG_CONFIG_HOME:-$HOME/.config}/xql"
if [ -d "$CONFIG_DIR" ]; then
	if [ "${XQL_PURGE:-0}" = "1" ]; then
		rm -rf "$CONFIG_DIR"
		say "Removed $CONFIG_DIR"
	elif [ "${XQL_UNINSTALL_YES:-0}" = "1" ]; then
		say "Kept $CONFIG_DIR (history); set XQL_PURGE=1 to remove it"
	elif read_yes "Also remove $CONFIG_DIR (history)?"; then
		rm -rf "$CONFIG_DIR"
		say "Removed $CONFIG_DIR"
	else
		say "Kept $CONFIG_DIR (history)"
	fi
fi

say ""
say "Done."
