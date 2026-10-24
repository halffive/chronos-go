#!/bin/sh
# Tags the commit being built as v<Version> from version.go, publishes the
# GitHub release, and asks the module proxy to fetch it.
set -eu

version="$(sed -n 's/^const Version = "\(.*\)"$/\1/p' version.go)"
if [ -z "$version" ]; then
	echo "no Version constant in version.go" >&2
	exit 1
fi
tag="v$version"

if gh api "repos/$GITHUB_REPOSITORY/git/ref/tags/$tag" >/dev/null 2>&1; then
	echo "$tag already exists; bump Version in version.go first" >&2
	exit 1
fi

gh release create "$tag" --target "$GITHUB_SHA" --title "$tag" --generate-notes
GOPROXY=https://proxy.golang.org go list -m "chronos.sh/go@$tag"
