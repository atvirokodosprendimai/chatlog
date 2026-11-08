#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Atviro kodo sprendimai
#
# release-build.sh VERSION [OUTDIR]: the release artifacts, the same in CI and on a laptop. modernc.org/sqlite is pure
# Go, so every target cross-compiles with CGO off from any host. Writes, per target:
#   OUTDIR/chatlog_VERSION_<os>_<arch>.tar.gz   (chatlog, LICENSE, NOTICE, README.md, INSTALL.md, docs/)
#   OUTDIR/chatlog-linux-<arch>                 (the bare linux binaries, for the container image)
# and OUTDIR/SHA256SUMS over the tarballs.
set -eu
version=${1:?usage: release-build.sh VERSION [OUTDIR]}
version=${version#v} # a tag v1.2.3 names files 1.2.3
out=${2:-dist}
targets="darwin/arm64 darwin/amd64 linux/amd64 linux/arm64"

cd "$(dirname "$0")/.."
rm -rf "$out"
mkdir -p "$out"
for t in $targets; do
	os=${t%/*}
	arch=${t#*/}
	name="chatlog_${version}_${os}_${arch}"
	stage="$out/$name"
	mkdir -p "$stage"
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$stage/chatlog" ./cmd/chatlog
	cp LICENSE NOTICE README.md INSTALL.md "$stage/"
	cp -R docs "$stage/docs"
	# the tarball holds one top directory, which Homebrew enters before install
	tar -C "$out" -czf "$out/$name.tar.gz" "$name"
	if [ "$os" = linux ]; then
		cp "$stage/chatlog" "$out/chatlog-linux-$arch"
	fi
	rm -rf "$stage"
done
(
	cd "$out"
	if command -v sha256sum >/dev/null 2>&1; then sha256sum ./*.tar.gz; else shasum -a 256 ./*.tar.gz; fi | sed 's| \./| |' >SHA256SUMS
)
echo "built $(ls "$out" | wc -l | tr -d ' ') files in $out"
