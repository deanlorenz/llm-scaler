#!/usr/bin/env bash
#
# Fails a pull request that moves a golden and does not name it in its body
# (docs/proposals/analyzer-learner.md 2.5, the rule; docs/developer-guide/
# testing.md, "The goldens: section", the format).
#
#   PR_BODY=<the pull request's body> goldens-section.sh <base> <head>
#
# <base> and <head> are any two commits git can diff. Exit 0: no golden moved,
# or the section names every one that did. Exit 1: a moved golden is not named,
# or the section is not of the format. Exit 2: the diff could not be read, or was
# empty. An unread diff must not read as "no golden moved".
#
# The check catches honest mistakes, not an adversary: a PR runs its own copy of
# it. So the body is not parsed as Markdown. The section is a strict line format,
# and a line that is not of it fails, whatever it would render as.
#
# PR_BODY is whatever the author typed, so it stays data here: it is held in
# variables, matched against, and never evaluated. Nothing from it is printed,
# because a line that opens with "::" in a job's output is a workflow command:
# a bad line is reported by its number. Paths come from git, through %q, because
# a path can hold a newline. A name is used as a pattern only after it passes
# name_allowed, so the only metacharacters it can hold are * and ?.

set -u

# Where the goldens are. A file under any of these is a golden.
GOLDEN_DIR="internal/engines/analyzers/learnreplay/testdata/goldens"
COUNTS_DIR="internal/engines/steadystate/testdata"
WHATIF_DIR="cmd/whatif/testdata"
is_golden() {
    case "$1" in
        "$GOLDEN_DIR"/*) return 0 ;;
        "$COUNTS_DIR"/engine-run-event-counts.txt) return 0 ;;
        "$WHATIF_DIR"/*.golden) return 0 ;;
        "$WHATIF_DIR"/schema-v*.txt) return 0 ;;
    esac
    return 1
}

NAME_CHARS='^[A-Za-z0-9._/*?-]+$'
NAME_LINE='^- `([^`]+)`( - .*)?$'

# relative sets REL to the name or path $1 without its golden root, and DEPTH to
# how many path components may be left: fixture and file, or the file alone.
relative() {
    DEPTH=2
    case "$1" in
        "$GOLDEN_DIR"/*) REL="${1#"$GOLDEN_DIR"/}" ;;
        "$COUNTS_DIR"/*) REL="${1#"$COUNTS_DIR"/}"; DEPTH=1 ;;
        "$WHATIF_DIR"/*) REL="${1#"$WHATIF_DIR"/}"; DEPTH=1 ;;
        *) REL="$1" ;;
    esac
}

# literals prints how many characters of $1 are not wildcards.
literals() {
    local s="${1//[*?]/}"
    printf '%s' "${#s}"
}

# slashes prints how many slashes $1 holds.
slashes() {
    local s="${1//[!\/]/}"
    printf '%s' "${#s}"
}

# name_allowed: a literal path, or a glob whose file part has at least three
# literal characters and that is not the whole of a golden root. So no wildcard
# stands above the fixture, and `*/*.csv` and `*.golden` are refused while
# `ttft-*/*.csv`, `*/results.csv` and `ttft-*.golden` are not.
name_allowed() {
    local file fixture stem
    [[ "$1" =~ $NAME_CHARS ]] || return 1
    relative "$1"
    [ "$(slashes "$REL")" -lt "$DEPTH" ] || return 1
    [[ "$REL" == *[*?]* ]] || return 0
    file="${REL##*/}"
    stem="${file%.*}"
    [ "$(literals "$file")" -ge 3 ] || return 1
    [ "$(literals "$stem")" -ge 3 ] && return 0
    [ "$REL" != "$file" ] || return 1
    fixture="${REL%/*}"
    [[ "$fixture" != *[*?]* ]] || [ "$(literals "$fixture")" -ge 3 ]
}

# name_matches: does the allowed name $1 name the golden $2? A golden outside the
# learner directory goes by its file name. The slashes must agree, so * never
# crosses one.
name_matches() {
    local want path="${2##*/}"
    relative "$1"
    want="$REL"
    case "$2" in
        "$GOLDEN_DIR"/*) relative "$2"; path="$REL" ;;
    esac
    [ "$(slashes "$want")" = "$(slashes "$path")" ] || return 1
    # The right side is deliberately unquoted: it is the pattern.
    # shellcheck disable=SC2053
    [[ "$path" == $want ]]
}

# read_section reads the body on stdin and sets what it found. Call it with a
# here-string: a pipe would run it in a subshell, and lose all of it.
SECTIONS=0
NAMES=()
NONE_LINES=()
BAD_LINES=()
BAD_NAMES=()
read_section() {
    local line name number=0 in_section=0
    while IFS= read -r line || [ -n "$line" ]; do
        number=$((number + 1))
        line="${line%$'\r'}"
        if [ "$line" = '## goldens:' ]; then
            SECTIONS=$((SECTIONS + 1))
            in_section=1
        elif [ "$in_section" = 0 ]; then
            continue
        elif [[ "$line" == '#'* ]]; then
            in_section=0
        elif [[ "$line" =~ ^[[:space:]]*$ ]]; then
            continue
        elif [ "$line" = none ]; then
            NONE_LINES+=("$number")
        elif ! [[ "$line" =~ $NAME_LINE ]]; then
            BAD_LINES+=("$number")
        else
            name="${BASH_REMATCH[1]}"
            if name_allowed "$name"; then
                NAMES+=("$name")
            else
                BAD_NAMES+=("$number")
            fi
        fi
    done
}

# plural prints the noun $2 for the count $1.
plural() {
    if [ "$1" = 1 ]; then
        printf '%s' "$2"
    else
        printf '%ss' "$2"
    fi
}

if [ "$#" -ne 2 ]; then
    echo "usage: PR_BODY=<body> goldens-section.sh <base> <head>" >&2
    exit 2
fi

# -z, because without it git C-quotes a path that holds a quote, a tab, a newline
# or a backslash, and the quoted form starts with a " that no golden's path does.
list="$(mktemp)" || exit 2
trap 'rm -f "$list"' EXIT
if ! git diff -z --name-only --no-renames "$1" "$2" -- >"$list"; then
    echo "FAIL: goldens: could not diff the two commits" >&2
    exit 2
fi
if [ ! -s "$list" ]; then
    echo "FAIL: goldens: the diff is empty, so this run checked nothing" >&2
    exit 2
fi

files=0
moved=()
while IFS= read -r -d '' path; do
    files=$((files + 1))
    is_golden "$path" && moved+=("$path")
done <"$list"

changed_files="$files $(plural "$files" file)"
if [ "${#moved[@]}" -eq 0 ]; then
    echo "ok: goldens: $changed_files changed, none of them a golden"
    exit 0
fi

read_section <<<"${PR_BODY:-}"

missing=()
for path in "${moved[@]}"; do
    found=0
    for name in ${NAMES[@]+"${NAMES[@]}"}; do
        if name_matches "$name" "$path"; then
            found=1
            break
        fi
    done
    [ "$found" = 1 ] || missing+=("$path")
done

moved_goldens="${#moved[@]} $(plural "${#moved[@]}" golden)"
problems=$((${#missing[@]} + ${#NONE_LINES[@]} + ${#BAD_LINES[@]} + ${#BAD_NAMES[@]}))
if [ "$SECTIONS" -eq 1 ] && [ "$problems" -eq 0 ]; then
    echo "ok: goldens: $moved_goldens moved in $changed_files changed, and the section names each"
    exit 0
fi

if [ "$SECTIONS" -eq 0 ]; then
    echo "FAIL: goldens: this PR moves $moved_goldens and its body has no goldens: section"
elif [ "$SECTIONS" -gt 1 ]; then
    echo "FAIL: goldens: this PR moves $moved_goldens and its body has $SECTIONS goldens: sections, not one"
else
    echo "FAIL: goldens: this PR moves $moved_goldens and its goldens: section does not hold up"
fi
for number in ${BAD_LINES[@]+"${BAD_LINES[@]}"}; do
    echo "  line $number of the body: not of the required form"
done
for number in ${BAD_NAMES[@]+"${BAD_NAMES[@]}"}; do
    echo "  line $number of the body: the name is not allowed"
done
for number in ${NONE_LINES[@]+"${NONE_LINES[@]}"}; do
    echo "  line $number of the body: none, and a golden moved"
done
for path in ${missing[@]+"${missing[@]}"}; do
    printf '  not named: %q\n' "${path#"$GOLDEN_DIR"/}"
done
echo 'Required: one line that is exactly ## goldens: at column 0, then one line per golden,'
echo '- `name` or - `name` - what moved and which way'
echo 'up to the next line that starts with #. A name is a path, or a glob with 3 literal'
echo 'characters in its file part. Editing the PR body re-runs this check.'
echo 'Format: docs/developer-guide/testing.md, "The goldens: section".'
exit 1
