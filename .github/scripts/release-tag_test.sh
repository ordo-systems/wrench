#!/bin/sh
# Tests release-tag.sh: version, pre-release flag and image tags for good tags; refusal of malformed ones.
set -u
script="$(dirname "$0")/release-tag.sh"
fail=0

expect() { # tag, expected output
	got=$(sh "$script" "$1" 2>&1) || { echo "FAIL $1: exited non-zero: $got"; fail=1; return; }
	[ "$got" = "$2" ] || { printf 'FAIL %s:\n got: %s\nwant: %s\n' "$1" "$got" "$2"; fail=1; }
}

refuse() { # tag
	if sh "$script" "$1" >/dev/null 2>&1; then echo "FAIL $1: accepted"; fail=1; fi
}

expect v1.2.3 "version=1.2.3
prerelease=false
image_tags<<EOF
type=raw,value=1.2.3
type=raw,value=1.2
type=raw,value=latest
EOF"
expect v1.2.3-rc.1 "version=1.2.3-rc.1
prerelease=true
image_tags<<EOF
type=raw,value=1.2.3-rc.1
EOF"
refuse v1.2
refuse 1.2.3
refuse v1.2.3+meta
refuse v01.2.3
refuse ""

[ "$fail" = 0 ] && echo "release-tag: ok"
exit "$fail"
