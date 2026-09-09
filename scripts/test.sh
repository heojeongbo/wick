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
#
# # Why it walks a list of modules
#
# The sinks that need a third-party SDK are modules of their own, so that a
# build which does not want the AWS SDK does not carry it. Each of them has its
# own go.mod, its own tests and its own hundred per cent.

set -o errexit
set -o pipefail
# set -o xtrace

__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)" # Directory where this script exists.
__root="$(cd "$(dirname "${__dir}")" && pwd)"         # Root directory of project.

cd "$__root"

# Every module in this repository, the root first. Kept here rather than found
# by walking for go.mod files, so that adding one is a decision and not an
# accident.
readonly MODULES=(
	.
	./sink/s3
)

# What is not counted, and why.
#
#	main.go			Three statements with no seam in them. It is run
#				by the Docker build, which calls `version` on the
#				binary it has just made.
#	*.g.go			Generated.
#
# Matched against the file's path alone, which is why every reader below cuts
# the line and column off first. It is exported rather than passed with `-v`
# because awk reads escapes out of a `-v` value: `\.g\.go` arrives as `.g.go`,
# which matches "config.go" -- and a gate that quietly stops counting two files
# is exactly the thing this gate is for.
export EXCLUDE='(/wick/main\.go$|\.g\.go$)'

check() {
	local module="$1"
	shift

	echo "==> $module"
	cd "$__root/$module"

	fmt="$(gofmt -l .)"
	if [ -n "$fmt" ]; then
		echo "    not gofmt'd:"
		echo "$fmt"
		exit 1
	fi

	go vet ./...

	# -coverpkg is not optional. Without it a package whose code is only
	# exercised by another package's tests is reported as 0.0%, and a package
	# with no test file of its own is left out of the profile altogether --
	# which reads as a pass for code nothing has run.
	#
	# It is the list and not the "./..." pattern because a workspace makes that
	# pattern reach across module boundaries: the root would instrument the
	# sinks that live in modules of their own, count them against itself, and
	# fail because their tests are not the ones being run.
	local packages
	packages="$(go list ./... | tr '\n' ',')"

	go test -trimpath -covermode=atomic -coverpkg="${packages%,}" -coverprofile=cover.out "$@" ./...

	gate "$__root/$module/cover.out"
}

gate() {
	local profile="$1"

	# `go tool cover -func` is what decides, because it folds the several test
	# binaries that may each have reported the same block. A function that is
	# not at 100% is named with the file and line it starts on.
	local short
	short="$(go tool cover -func="$profile" | awk '
		BEGIN { ex = ENVIRON["EXCLUDE"] }
		$1 == "total:" { next }
		{ split($1, p, ":"); if (p[1] ~ ex) next }
		$NF != "100.0%" { print }
	')"

	# The summary, over the same set of files the gate is about.
	awk '
		BEGIN { ex = ENVIRON["EXCLUDE"] }
		NR == 1 { next }                                  # "mode: atomic"
		{
			# <path>:<line>.<col>,<line>.<col> <numstmts> <count>
			split($1, p, ":")
			if (p[1] ~ ex) next
			stmts[$1] = $2 + 0
			if ($3 + 0 > 0) hit[$1] = 1
		}
		END {
			for (b in stmts) { total += stmts[b]; if (b in hit) covered += stmts[b] }
			if (total == 0) { print "    no statements were measured, which is not a pass"; exit 1 }
			printf "    %d/%d statements, %.2f%%\n", covered, total, 100 * covered / total
		}
	' "$profile"

	if [ -z "$short" ]; then
		return 0
	fi

	echo ""
	echo "not covered:"
	echo "$short" | sed 's/^/    /'

	# And the blocks themselves, so the fix does not need a second command to
	# find.
	#
	# Blocks of no statements are left out. An empty `default:` in a select is
	# one, and so is a loop body that a test drains without doing anything in;
	# neither can be covered, and neither is what `-func` is complaining about.
	echo ""
	echo "the statements nothing ran:"
	awk '
		BEGIN { ex = ENVIRON["EXCLUDE"] }
		NR == 1 { next }
		{
			split($1, p, ":")
			if (p[1] ~ ex) next
			if ($2 + 0 == 0) next
			stmts[$1] = $2 + 0
			if ($3 + 0 > 0) hit[$1] = 1
		}
		END { for (b in stmts) if (!(b in hit)) print "    " b }
	' "$profile" | sort

	exit 1
}

for module in "${MODULES[@]}"; do
	check "$module" "$@"
done

echo "==> ok"
