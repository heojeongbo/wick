#!/usr/bin/env bash

# Everything CI decides on, in one place.
#
# Run this rather than the parts of it. A list of commands to remember is a list
# somebody forgets one of, and the one that gets forgotten is the coverage gate,
# because it is the only one here that is not already a habit.
#
# # Why there is a coverage gate at all
#
# This is a thing that deletes files. Every branch it takes that nobody has run
# is a branch that will first run on a machine holding the only copy of
# something. A hundred per cent is not a claim that the tests are good; it is a
# claim that there is no code here nobody has ever executed, which is a much
# smaller claim and the one worth keeping.

set -o errexit
set -o pipefail
# set -o xtrace

__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" # Directory where this script exists.
__root="$(cd "$(dirname "${__dir}")" && pwd)"         # Root directory of project.

cd "$__root"

# What is not counted, and why.
#
#	main.go			Three statements with no seam in them. It is run
#				by the Docker build, which calls `version` on the
#				binary it has just made.
#	*.g.go			Generated.
#
# Matched against the path as it appears in the profile, which is the import
# path and then the file.
readonly EXCLUDE='(/wick/main\.go|\.g\.go)'

echo "==> gofmt"
fmt="$(gofmt -l .)"
if [ -n "$fmt" ]; then
	echo "not gofmt'd:"
	echo "$fmt"
	exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> go test"
# -coverpkg=./... is not optional. Without it a package whose code is only
# exercised by another package's tests is reported as 0.0%, and a package with
# no test file of its own is left out of the profile altogether -- which reads
# as a pass for code nothing has run.
go test -trimpath -covermode=atomic -coverpkg=./... -coverprofile=cover.out "$@" ./...

echo "==> coverage"

# `go tool cover -func` is what decides, because it folds the several test
# binaries that may each have reported the same block. A function that is not
# at 100% is named with the file and line it starts on.
short="$(go tool cover -func=cover.out | awk -v ex="$EXCLUDE" '
	$1 == "total:" { next }
	$1 ~ ex        { next }
	$NF != "100.0%" { print }
')"

# The summary, over the same set of files the gate is about.
awk -v ex="$EXCLUDE" '
	NR == 1 { next }                                  # "mode: atomic"
	{
		# <path>:<line>.<col>,<line>.<col> <numstmts> <count>
		if ($1 ~ ex) next
		stmts[$1] = $2 + 0
		if ($3 + 0 > 0) hit[$1] = 1
	}
	END {
		for (b in stmts) { total += stmts[b]; if (b in hit) covered += stmts[b] }
		if (total == 0) { print "    no statements were measured, which is not a pass"; exit 1 }
		printf "    %d/%d statements, %.2f%%\n", covered, total, 100 * covered / total
	}
' cover.out

if [ -z "$short" ]; then
	echo "==> ok"
	exit 0
fi

echo ""
echo "not covered:"
echo "$short" | sed 's/^/    /'

# And the blocks themselves, so the fix does not need a second command to find.
#
# Blocks of no statements are left out. An empty `default:` in a select is one,
# and so is a loop body that a test drains without doing anything in; neither
# can be covered, and neither is what `-func` is complaining about.
echo ""
echo "the statements nothing ran:"
awk -v ex="$EXCLUDE" '
	NR == 1 { next }
	{
		if ($1 ~ ex) next
		if ($2 + 0 == 0) next
		stmts[$1] = $2 + 0
		if ($3 + 0 > 0) hit[$1] = 1
	}
	END { for (b in stmts) if (!(b in hit)) print "    " b }
' cover.out | sort

exit 1
