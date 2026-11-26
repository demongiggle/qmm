#!/bin/sh
# Build the tar-only release assets uploaded to a GitHub release.
# `qmm update` consumes qmm_<goos>_<arch>.tar.gz (see internal/update.findAsset);
# each tarball contains a single executable `qmm` with the executable bit intact
# (raw GitHub downloads lose the mode). SHA256SUMS covers the tarballs.
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

# Package each binary as a tarball containing a single `qmm` executable.
# Staging through a temp dir gives the inner file the plain `qmm` name with
# the executable bit intact. Raw binaries are removed afterwards: releases
# ship only *.tar.gz + SHA256SUMS.
package() {
	os=$1
	arch=$2
	name="qmm_${os}_${arch}"
	tmp=$(mktemp -d)
	cp "$out/$name" "$tmp/qmm"
	chmod 755 "$tmp/qmm"
	tar -czf "$out/${name}.tar.gz" -C "$tmp" qmm
	rm -rf "$tmp" "$out/$name"
	printf 'packaged %s.tar.gz\n' "$name"
}

package linux amd64
package linux arm64
package darwin amd64
package darwin arm64
package freebsd amd64

# Checksums of the tarballs for manual verification.
(cd "$out" && sha256sum qmm_*.tar.gz > SHA256SUMS)
printf 'wrote %s/SHA256SUMS\n' "$out"
