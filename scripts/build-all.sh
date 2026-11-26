#!/bin/sh
# Build the release binaries uploaded to a GitHub release. Asset names must
# match internal/update.findAsset: qmm_<goos>_<goarch>.
set -eu

version=${VERSION:-$(sh ./scripts/version.sh)}
out=${OUT_DIR:-dist}
go=${GO:-go}
mkdir -p "$out"

build() {
	os=$1
	arch=$2
	name="qmm_${os}_${arch}"
	printf 'building %s (version %s)\n' "$name" "$version"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" "$go" build -trimpath \
		-ldflags="-s -w -X main.version=$version" -o "$out/$name" ./cmd/qmm
}

# qmm uses Unix sockets and PTYs, so no Windows artifacts are built.
build linux amd64
build linux arm64
build darwin amd64
build darwin arm64
build freebsd amd64
