#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Atviro kodo sprendimai
#
# install.sh: put chatlog in ~/.local/bin and wire it into Claude Code (hooks, MCP server, skill, starter notes), then
# read the history that already exists and check the result. POSIX sh, no sudo, safe to run again.
#
#   ./install.sh              from a checkout: build with Go; piped (curl ... | sh): download the release for this OS
#   ./install.sh --dry-run    say what would change, change nothing
#   ./install.sh --uninstall  take chatlog's entries out of Claude Code's settings; the data and the binary stay
#
# CHATLOG_BIN_DIR overrides ~/.local/bin; CHATLOG_VERSION (e.g. v0.1.0) picks a release instead of the latest;
# CHATLOG_RELEASES points at a mirror of the GitHub releases (same layout: <base>/download/<tag>/<file>).
# The logic lives in the binary (chatlog install, ingest, doctor): this file only gets the binary and calls it.
set -eu

releases=${CHATLOG_RELEASES:-https://github.com/atvirokodosprendimai/chatlog/releases}
bin_dir=${CHATLOG_BIN_DIR:-"$HOME/.local/bin"}
mode=install
dry=""
for arg in "$@"; do
	case $arg in
	--dry-run) dry=--dry-run ;;
	--uninstall) mode=uninstall ;;
	-h | --help)
		echo "usage: install.sh [--dry-run] [--uninstall]; see the head of the file or INSTALL.md"
		exit 0
		;;
	*)
		echo "install.sh: unknown argument $arg (try --help)" >&2
		exit 2
		;;
	esac
done

if [ "$mode" = uninstall ]; then
	if [ ! -x "$bin_dir/chatlog" ]; then
		echo "install.sh: $bin_dir/chatlog not found: nothing to uninstall with" >&2
		exit 1
	fi
	"$bin_dir/chatlog" install --uninstall $dry
	echo "The binary $bin_dir/chatlog and the data dir are kept; delete them by hand if you want them gone."
	exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# build puts the binary in $tmp/chatlog from the checkout this script stands in.
build() {
	if ! command -v go >/dev/null 2>&1; then
		echo "install.sh: Go is needed to build from a checkout (go.mod names the version): https://go.dev/dl/" >&2
		exit 1
	fi
	(cd "$src" && go build -o "$tmp/chatlog" ./cmd/chatlog)
}

# download puts the release binary for this OS in $tmp/chatlog, checked against the release's SHA256SUMS.
download() {
	case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*)
		echo "install.sh: no release for $(uname -s); build from a checkout instead" >&2
		exit 1
		;;
	esac
	case $(uname -m) in
	arm64 | aarch64) arch=arm64 ;;
	x86_64 | amd64) arch=amd64 ;;
	*)
		echo "install.sh: no release for $(uname -m)" >&2
		exit 1
		;;
	esac
	tag=${CHATLOG_VERSION:-}
	if [ -z "$tag" ]; then
		tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$releases/latest")
		tag=${tag##*/}
	fi
	v=${tag#v}
	name="chatlog_${v}_${os}_${arch}"
	url="$releases/download/$tag"
	echo "downloading $url/$name.tar.gz"
	curl -fsSL -o "$tmp/$name.tar.gz" "$url/$name.tar.gz"
	curl -fsSL -o "$tmp/SHA256SUMS" "$url/SHA256SUMS"
	want=$(awk -v f="$name.tar.gz" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
	if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$name.tar.gz"); else got=$(shasum -a 256 "$tmp/$name.tar.gz"); fi
	got=${got%% *}
	if [ -z "$want" ] || [ "$want" != "$got" ]; then
		echo "install.sh: checksum of $name.tar.gz does not match SHA256SUMS: refusing it" >&2
		exit 1
	fi
	tar -C "$tmp" -xzf "$tmp/$name.tar.gz"
	mv "$tmp/$name/chatlog" "$tmp/chatlog"
}

# a checkout is where the script stands next to go.mod; piped into sh, $0 is the shell and there is none
src=$(dirname "$0")
if [ -f "$src/go.mod" ] && [ -d "$src/cmd/chatlog" ]; then build; else download; fi

if [ -n "$dry" ]; then
	echo "would put the binary at $bin_dir/chatlog"
	"$tmp/chatlog" install --dry-run --bin "$bin_dir/chatlog"
	echo "would then run: chatlog ingest --all; chatlog doctor"
	exit 0
fi

mkdir -p "$bin_dir"
# copy beside the target first, then rename in the same directory: a rename is atomic, a move across file systems is
# not, and a hook starting in between must find a whole binary. A running server keeps its old file.
cp "$tmp/chatlog" "$bin_dir/chatlog.new"
chmod 755 "$bin_dir/chatlog.new"
mv -f "$bin_dir/chatlog.new" "$bin_dir/chatlog"

"$bin_dir/chatlog" install
"$bin_dir/chatlog" ingest --all
case ":$PATH:" in
*":$bin_dir:"*) ;;
*) echo "NOTE: $bin_dir is not on PATH; add it (the settings use the full path, so chatlog works without it)." ;;
esac
"$bin_dir/chatlog" doctor
