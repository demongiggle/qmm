#!/bin/sh
# Print the exact tag on HEAD for release builds, or "dev" for untagged ones.
# The value is stamped into main.version with -ldflags (see the Makefile).
set -eu

if version=$(git describe --tags --exact-match HEAD 2>/dev/null); then
	printf '%s\n' "$version"
else
	printf '%s\n' dev
fi
