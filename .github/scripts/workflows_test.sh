#!/bin/sh
# Structural checks of the release path that actionlint cannot express.
set -u
w=.github/workflows
fail=0
check() { # description, command...
	desc=$1; shift
	"$@" >/dev/null 2>&1 || { echo "FAIL: $desc"; fail=1; }
}
# The image is published only once the checks passed.
check "release image waits for the checks" grep -q 'needs: \[meta, ci\]' $w/release.yml
# CI enforces actionlint, the SHA pins and these checks.
check "ci runs make lint-workflows" grep -q 'make lint-workflows' $w/ci.yml
[ "$fail" = 0 ] && echo "workflows: ok"
exit "$fail"
