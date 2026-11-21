#!/bin/sh
# Classifies a release tag for release.yml. Prints GITHUB_OUTPUT lines: the version without "v", whether it is a
# pre-release, and the docker/metadata-action tags. A final release moves X.Y and latest; a pre-release
# (vX.Y.Z-suffix) gets only its own tag. Anything but vMAJOR.MINOR.PATCH[-suffix] is refused.
set -eu
tag=${1:-}
semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'
if ! printf '%s\n' "$tag" | grep -Eq "$semver"; then
	echo "release tag must look like v1.2.3 or v1.2.3-rc.1, got '$tag'" >&2
	exit 1
fi
version=${tag#v}
case $version in
*-*)
	printf 'version=%s\nprerelease=true\nimage_tags<<EOF\ntype=raw,value=%s\nEOF\n' "$version" "$version"
	;;
*)
	minor=${version%.*}
	printf 'version=%s\nprerelease=false\nimage_tags<<EOF\ntype=raw,value=%s\ntype=raw,value=%s\ntype=raw,value=latest\nEOF\n' \
		"$version" "$version" "$minor"
	;;
esac
