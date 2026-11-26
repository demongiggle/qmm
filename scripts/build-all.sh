#!/bin/sh
# Build the release binaries uploaded to a GitHub release. Raw assets
# qmm_<goos>_<goarch> are consumed by `qmm update` (see internal/update.findAsset).
# Each binary is also packaged as qmm_<goos>_<goarch>.tar.gz containing a single
# executable `qmm` (preserving the executable bit for manual downloads, which
# lose the mode on raw GitHub assets), plus SHA256SUMS of the tarballs.
# Layout mirrors ../qcode's dist/ (raw + tar.gz + SHA256SUMS).
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

# Package each raw binary as a tarball containing a single `qmm` executable.
# Staging through a temp dir gives the inner file the plain `qmm` name (like
# qcode's tarballs) with the executable bit intact.
package() {
	os=$1
	arch=$2
	name="qmm_${os}_${arch}"
	tmp=$(mktemp -d)
	cp "$out/$name" "$tmp/qmm"
	chmod 755 "$tmp/qmm"
	tar -czf "$out/${name}.tar.gz" -C "$tmp" qmm
	rm -rf "$tmp"
	printf 'packaged %s.tar.gz\n' "$name"
}

package linux amd64
package linux arm64
package darwin amd64
package darwin arm64
package freebsd amd64

# Checksums of the tarballs for manual verification (qcode-style SHA256SUMS).
(cd "$out" && sha256sum qmm_*.tar.gz > SHA256SUMS)
printf 'wrote %s/SHA256SUMS\n' "$out"
