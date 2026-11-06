#!/usr/bin/env bash
#
# Fails a pull request that moves a golden and does not name it in its body
# (docs/proposals/analyzer-learner.md 2.5, the rule; docs/developer-guide/
# testing.md, "The goldens: section", the format).
#
#   PR_BODY=<the pull request's body> goldens-section.sh <base> <head>
#
# <base> and <head> are any two commits git can diff. Exit 0: no golden moved,
# or the section names every one that did. Exit 1: a moved golden is not named.
# Exit 2: the diff could not be read, or was empty. An unread diff must not
# read as "no golden moved".
#
# PR_BODY is whatever the author typed, so it stays data here: it is held in
# variables, matched against, and never evaluated. Nothing from it is printed
# either, because a line that opens with "::" in a job's output is a workflow
# command. What is printed comes from git. A name is used as a pattern only
# after it passes NAME_CHARS, so the only metacharacters it can hold are * and ?.

set -u

# Where the goldens are. A file under any of these is a golden.
GOLDEN_DIR="internal/engines/analyzers/learnreplay/testdata/goldens"
is_golden() {
    case "$1" in
        "$GOLDEN_DIR"/*) return 0 ;;
        internal/engines/steadystate/testdata/engine-run-event-counts.txt) return 0 ;;
        cmd/whatif/testdata/*.golden) return 0 ;;
        cmd/whatif/testdata/schema-v*.txt) return 0 ;;
    esac
    return 1
}

NAME_CHARS='^[A-Za-z0-9._/*?-]+$'

# section_names reads the body on stdin. It sets NAMES to every code span in the
# goldens: sections and SECTIONS to how many there are. Call it with a here-string,
# not a pipe: a pipe would run it in a subshell, and lose both.
NAMES=()
SECTIONS=0
section_names() {
    local line lower rest span
    local in_fence=0 in_comment=0 in_section=0
    while IFS= read -r line || [ -n "$line" ]; do
        line="${line%$'\r'}"

        # A fenced block is pasted output: it opens no section, ends none and
        # names nothing. An HTML comment is the template's own text.
        if [ "$in_comment" = 0 ] && [[ "$line" =~ ^\ {0,3}(\`\`\`|~~~) ]]; then
            in_fence=$((1 - in_fence))
            continue
        fi
        [ "$in_fence" = 1 ] && continue
        if [ "$in_comment" = 1 ]; then
            case "$line" in
                *'-->'*) line="${line#*-->}"; in_comment=0 ;;
                *) continue ;;
            esac
        fi
        while [[ "$line" == *'<!--'* ]]; do
            rest="${line#*<!--}"
            if [[ "$rest" == *'-->'* ]]; then
                line="${line%%<!--*}${rest#*-->}"
            else
                line="${line%%<!--*}"
                in_comment=1
                break
            fi
        done

        lower="${line,,}"
        if [[ "$lower" =~ ^#{1,6}[[:space:]]+\*{0,2}goldens:?\*{0,2}:?[[:space:]]*$ ]] ||
            [[ "$lower" =~ ^\*{0,2}goldens(:\*{0,2}|\*{2}:)([[:space:]]|$) ]]; then
            in_section=1
            SECTIONS=$((SECTIONS + 1))
        elif [[ "$lower" =~ ^#{1,6}[[:space:]] ]] || [[ "$lower" == '**'* ]]; then
            in_section=0
        fi
        [ "$in_section" = 1 ] || continue

        rest="$line"
        while [[ "$rest" =~ \`([^\`]+)\`(.*)$ ]]; do
            span="${BASH_REMATCH[1]}"
            rest="${BASH_REMATCH[2]}"
            NAMES+=("$span")
        done
    done
}

# slashes prints how many slashes $1 holds.
slashes() {
    local s="${1//[!\/]/}"
    printf '%s' "${#s}"
}

# name_matches: does the name $1 name the file $2? A name matches file by file
# along the path, so * never crosses a slash: `*.csv` is not every CSV of every
# fixture. A name with no slash may be a learner fixture, and then names all of
# it. A name of wildcards alone names nothing.
name_matches() {
    local name="$1" path="$2"
    [[ "$name" =~ $NAME_CHARS ]] || return 1
    [[ "$name" =~ [A-Za-z0-9] ]] || return 1
    case "$name" in
        */) name="${name}*" ;;
    esac
    local short="${path#"$GOLDEN_DIR"/}"
    local candidates=("$path")
    if [ "$short" != "$path" ]; then
        candidates+=("$short")
    else
        candidates+=("${path##*/}")
    fi
    local c
    for c in "${candidates[@]}"; do
        [ "$(slashes "$name")" = "$(slashes "$c")" ] || continue
        # The right side is deliberately unquoted: it is the pattern.
        # shellcheck disable=SC2053
        [[ "$c" == $name ]] && return 0
    done
    if [ "$short" != "$path" ] && [ "$(slashes "$name")" = 0 ] && [ "$(slashes "$short")" = 1 ]; then
        # shellcheck disable=SC2053
        [[ "$short" == $name/* ]] && return 0
    fi
    return 1
}

if [ "$#" -ne 2 ]; then
    echo "usage: PR_BODY=<body> goldens-section.sh <base> <head>" >&2
    exit 2
fi

# Assigned on its own line: `local` or a pipe would hide git's exit status.
if ! changed="$(git -c core.quotePath=false diff --name-only --no-renames "$1" "$2" --)"; then
    echo "FAIL: goldens: could not diff the two commits" >&2
    exit 2
fi
if [ -z "$changed" ]; then
    echo "FAIL: goldens: the diff is empty, so this run checked nothing" >&2
    exit 2
fi

files=0
moved=()
while IFS= read -r path; do
    files=$((files + 1))
    is_golden "$path" && moved+=("$path")
done <<<"$changed"

if [ "${#moved[@]}" -eq 0 ]; then
    echo "ok: goldens: $files files changed, none of them a golden"
    exit 0
fi

section_names <<<"${PR_BODY:-}"

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

if [ "${#missing[@]}" -eq 0 ]; then
    echo "ok: goldens: ${#moved[@]} of $files files changed are goldens, and the section names each"
    exit 0
fi

if [ "$SECTIONS" -eq 0 ]; then
    echo "FAIL: goldens: this PR moves ${#moved[@]} goldens and its body has no goldens: section"
else
    echo "FAIL: goldens: this PR moves ${#moved[@]} goldens and its goldens: section does not name ${#missing[@]}"
fi
for path in "${missing[@]}"; do
    echo "  not named: ${path#"$GOLDEN_DIR"/}"
done
echo "Name each in backticks under a goldens: heading, say which way it moves, and paste the"
echo "replay's per-output summary. Editing the PR body re-runs this check."
echo "Format: docs/developer-guide/testing.md, \"The goldens: section\"."
exit 1
