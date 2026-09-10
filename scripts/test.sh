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
#
# # Narrowing it while you work
#
# Told nothing, this runs everything and holds the floor, which is what CI does
# and what a change is finished against. While the change is being made, three
# things narrow it. Anything else given is passed to `go test`.
#
#	MODULE=./sink/s3	one module instead of all five. Its tests are
#				all still run, so the floor still means
#				something and is still held.
#	PKG=./spool/...		some packages instead of all of them. The floor
#				is reported and *not* held: a run that left
#				most of the tests out has nothing to say about
#				whether everything is covered, and failing on
#				it would only teach you to stop reading it.
#	COVER=off		no instrumentation at all. The fastest of the
#				three, and the one that answers "does it pass".
#
#	PKG=./spool/... ./scripts/test.sh -run TestSettle -v
#
# CI sets none of them.

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
	./sink/azure
	./sink/gcs
	./sink/s3
	./sink/sftp
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

# What is being asked for, out of the three things that narrow a run.
readonly PKG="${PKG:-./...}"
readonly COVER="${COVER:-on}"

# A run that left tests out cannot speak for the floor. It still prints the
# number, because the number is useful while you work; it just does not fail on
# it, and it says which it is doing.
narrowed="no"
if [ "$PKG" != "./..." ]; then
	narrowed="yes"

	# A package pattern is a path inside one module, so naming one and then
	# walking all five would ask four of them about a directory they do not
	# have. The root is what it means unless a module was also named.
	MODULE="${MODULE:-.}"
fi
readonly narrowed

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

	local -a cover=()
	if [ "$COVER" != "off" ]; then
		# -coverpkg is not optional. Without it a package whose code is only
		# exercised by another package's tests is reported as 0.0%, and a
		# package with no test file of its own is left out of the profile
		# altogether -- which reads as a pass for code nothing has run.
		#
		# It is the list and not the "./..." pattern because a workspace makes
		# that pattern reach across module boundaries: the root would
		# instrument the sinks that live in modules of their own, count them
		# against itself, and fail because their tests are not the ones being
		# run.
		#
		# When PKG has narrowed the run it is the list as well. Instrumenting
		# the whole module while running a tenth of its tests earns a "no
		# packages being tested depend on" warning for every package left out,
		# which is a screenful of noise saying only what was already asked for.
		local packages
		if [ "$narrowed" = "yes" ]; then
			packages="$(go list $PKG | tr '\n' ',')"
		else
			packages="$(go list ./... | tr '\n' ',')"
		fi
		cover=(-covermode=atomic -coverpkg="${packages%,}" -coverprofile=cover.out)
	fi

	# The `coverage:` suffix is the whole -coverpkg list repeated once per
	# package -- twenty-six paths, twenty-two times -- for a percentage that
	# does not mean what it looks like: under -coverpkg it is that package's
	# tests measured against every package rather than against itself. The one
	# that means something is what `gate` prints underneath. So the suffix comes
	# off and everything else goes through untouched, failures included.
	go test -trimpath "${cover[@]}" "$@" $PKG \
		| sed -e 's/[[:space:]]*coverage: [0-9.]*% of statements.*$//'

	if [ "$COVER" != "off" ]; then
		gate "$__root/$module/cover.out"
	fi
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

	if [ "$narrowed" = "yes" ]; then
		echo "    PKG left tests out, so the floor is not held on this run"
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

selected=("${MODULES[@]}")
if [ -n "${MODULE:-}" ]; then
	# Refused rather than passed to `cd`, so that a typo says which names there
	# are instead of failing four lines later on a directory that is not one.
	if ! printf '%s\n' "${MODULES[@]}" | grep -qxF -- "$MODULE"; then
		echo "there is no module ${MODULE}; it is one of ${MODULES[*]}" >&2
		exit 1
	fi
	selected=("$MODULE")
fi

for module in "${selected[@]}"; do
	check "$module" "$@"
done

echo "==> ok"
