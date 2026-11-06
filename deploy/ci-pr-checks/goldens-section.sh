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
# or GitHub may not render the section the way this script reads it. Exit 2: the
# diff could not be read, or was empty. An unread diff must not read as "no
# golden moved".
#
# PR_BODY is whatever the author typed, so it stays data here: it is held in
# variables, matched against, and never evaluated. Nothing from it is printed
# either, because a line that opens with "::" in a job's output is a workflow
# command. What is printed comes from git, through %q, because a path can hold a
# newline. A name is used as a pattern only after it passes NAME_CHARS, so the
# only metacharacters it can hold are * and ?.

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

# The Markdown this script must read as GitHub renders it (CommonMark). Held in
# variables because a bash regex cannot quote a backtick reliably in place.
FENCE_OPEN='^ {0,3}(`{3,}|~{3,})(.*)$'
FENCE_CLOSE='^ {0,3}(`{3,}|~{3,})[[:space:]]*$'
COMMENT_OPEN='^ {0,3}<!--'
SECTION_HEADING='^#{1,6}[[:space:]]+\*{0,2}goldens:?\*{0,2}:?[[:space:]]*$'
SECTION_LINE='^\*{0,2}goldens(:\*{0,2}|\*{2}:)([[:space:]]|$)'
ANY_HEADING='^ {0,3}#{1,6}([[:space:]]|$)'
NESTED_FENCE='^([[:space:]>]+|[[:space:]>]*([-*+]|[0-9]+[.)])[[:space:]]+)+(`{3}|~{3})'
INDENTED='^(    |[ ]{0,3}	)'
PLAIN_TEXT='^([^\\`<]*)(.*)$'
BACKTICK_RUN='^(`+)(.*)$'
TO_NEXT_RUN='^([^`]*)(`+)(.*)$'

# read_inline reads one line as a paragraph's text, left to right, because a
# code span and a comment each hide whatever opens the other. It sets VISIBLE to
# the line with its comments gone, SPANS to its code spans, UNMATCHED when a
# backtick run has no partner on the line, and OPEN_INLINE when a comment does
# not end on it. Either of those two may be closed on a later line of the same
# paragraph, which one line cannot tell.
read_inline() {
    local rest="$1" run after tail body pre closing span found
    VISIBLE=''
    SPANS=()
    UNMATCHED=0
    OPEN_INLINE=0
    while [ -n "$rest" ]; do
        [[ "$rest" =~ $PLAIN_TEXT ]]
        VISIBLE+="${BASH_REMATCH[1]}"
        rest="${BASH_REMATCH[2]}"
        case "$rest" in
            '') ;;
            '\'*)
                VISIBLE+="${rest:0:2}"
                rest="${rest:2}"
                ;;
            '<!--'*)
                after="${rest#<!--}"
                if [[ "$after" == *'-->'* ]]; then
                    rest="${after#*-->}"
                else
                    OPEN_INLINE=1
                    rest=''
                fi
                ;;
            '<'*)
                VISIBLE+='<'
                rest="${rest:1}"
                ;;
            *)
                [[ "$rest" =~ $BACKTICK_RUN ]]
                run="${BASH_REMATCH[1]}"
                after="${BASH_REMATCH[2]}"
                tail="$after"
                body=''
                found=0
                while [[ "$tail" =~ $TO_NEXT_RUN ]]; do
                    pre="${BASH_REMATCH[1]}"
                    closing="${BASH_REMATCH[2]}"
                    tail="${BASH_REMATCH[3]}"
                    if [ "${#closing}" = "${#run}" ]; then
                        found=1
                        body+="$pre"
                        break
                    fi
                    body+="$pre$closing"
                done
                if [ "$found" = 1 ]; then
                    span="$body"
                    if [[ "$span" == ' '*' ' ]] && [[ "$span" == *[!\ ]* ]]; then
                        span="${span:1:${#span}-2}"
                    fi
                    SPANS+=("$span")
                    VISIBLE+="$run$body$run"
                    rest="$tail"
                else
                    UNMATCHED=1
                    VISIBLE+="$run"
                    rest="$after"
                fi
                ;;
        esac
    done
}

# section_names reads the body on stdin. It sets NAMES to every code span in the
# goldens: sections, SECTIONS to how many there are, and the DOUBT_ flags to what
# GitHub may render otherwise. Call it with a here-string, not a pipe: a pipe
# would run it in a subshell, and lose all of them.
NAMES=()
SECTIONS=0
DOUBT_COMMENT=0
DOUBT_BACKTICK=0
DOUBT_NESTED=0
DOUBT_INDENT=0
section_names() {
    local line lower marker info opened
    local fence_char='' fence_len=0 in_comment=0 in_section=0 prev_unmatched=0
    while IFS= read -r line || [ -n "$line" ]; do
        # Inside a fence only its own closer counts: the same character, at
        # least as long, and nothing after it. So ``` does not close ````, and
        # ~~~ does not close ```.
        if [ "$fence_len" -gt 0 ]; then
            if [[ "$line" =~ $FENCE_CLOSE ]]; then
                marker="${BASH_REMATCH[1]}"
                if [ "${marker:0:1}" = "$fence_char" ] && [ "${#marker}" -ge "$fence_len" ]; then
                    fence_len=0
                fi
            fi
            continue
        fi
        if [ "$in_comment" = 1 ]; then
            [[ "$line" == *'-->'* ]] && in_comment=0
            continue
        fi

        # A fenced block is pasted output: it opens no section, ends none and
        # names nothing. A backtick fence's info string holds no backtick, so
        # "``` x ```" is a code span in a paragraph, not a fence.
        if [[ "$line" =~ $FENCE_OPEN ]]; then
            marker="${BASH_REMATCH[1]}"
            info="${BASH_REMATCH[2]}"
            if [ "${marker:0:1}" = '~' ] || [[ "$info" != *'`'* ]]; then
                fence_char="${marker:0:1}"
                fence_len="${#marker}"
                prev_unmatched=0
                continue
            fi
        fi
        # A comment that opens a line is an HTML block: the template's own text.
        # It runs to the end of the line holding -->, whatever follows that.
        if [[ "$line" =~ $COMMENT_OPEN ]]; then
            [[ "$line" == *'-->'* ]] || in_comment=1
            prev_unmatched=0
            continue
        fi

        read_inline "$line"
        [ "$OPEN_INLINE" = 1 ] && DOUBT_COMMENT=1
        lower="${VISIBLE,,}"
        opened=0
        if [[ "$lower" =~ $SECTION_HEADING ]] || [[ "$lower" =~ $SECTION_LINE ]]; then
            in_section=1
            opened=1
            SECTIONS=$((SECTIONS + 1))
        elif [[ "$lower" =~ $ANY_HEADING ]] || [[ "$lower" == '**'* ]]; then
            in_section=0
        fi
        if [ "$in_section" = 1 ]; then
            [ "$UNMATCHED" = 1 ] && DOUBT_BACKTICK=1
            [ "$opened" = 1 ] && [ "$prev_unmatched" = 1 ] && DOUBT_BACKTICK=1
            if [[ "$line" =~ $NESTED_FENCE ]] && ! [[ "$line" =~ $FENCE_OPEN ]]; then
                DOUBT_NESTED=1
            fi
            if [[ "$line" =~ $INDENTED ]] && [ "${#SPANS[@]}" -gt 0 ]; then
                DOUBT_INDENT=1
            fi
            NAMES+=(${SPANS[@]+"${SPANS[@]}"})
        fi
        prev_unmatched="$UNMATCHED"
    done
}

# slashes prints how many slashes $1 holds.
slashes() {
    local s="${1//[!\/]/}"
    printf '%s' "${#s}"
}

# carries_literal: does the name $1 spell a fixture or an output? One of its
# parts below the goldens' own directory must be literal, or open with at least
# three literal characters, so `ttft-*.golden` and `*/results.csv` do, and
# `*/*.csv`, `*e*` and `cmd/whatif/testdata/*` do not.
carries_literal() {
    local name="${1#"$GOLDEN_DIR"/}" part
    name="${name#"$COUNTS_DIR"/}"
    name="${name#"$WHATIF_DIR"/}"
    local parts=()
    IFS=/ read -r -a parts <<<"$name"
    for part in ${parts[@]+"${parts[@]}"}; do
        [[ "$part" != *[*?]* ]] && return 0
        [[ "$part" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{2} ]] && return 0
    done
    return 1
}

# name_matches: does the name $1 name the file $2? A name matches file by file
# along the path, so * never crosses a slash: `*.csv` is not every CSV of every
# fixture. A name with no slash may be a learner fixture, and then names all of
# it.
name_matches() {
    local name="$1" path="$2"
    [[ "$name" =~ $NAME_CHARS ]] || return 1
    case "$name" in
        */) name="${name}*" ;;
    esac
    carries_literal "$name" || return 1
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

moved_goldens="${#moved[@]} $(plural "${#moved[@]}" golden)"
doubts=$((DOUBT_COMMENT + DOUBT_BACKTICK + DOUBT_NESTED + DOUBT_INDENT))
if [ "${#missing[@]}" -eq 0 ] && [ "$doubts" -eq 0 ]; then
    echo "ok: goldens: $moved_goldens moved in $changed_files changed, and the section names each"
    exit 0
fi

if [ "$SECTIONS" -eq 0 ]; then
    echo "FAIL: goldens: this PR moves $moved_goldens and its body has no goldens: section"
elif [ "${#missing[@]}" -gt 0 ]; then
    echo "FAIL: goldens: this PR moves $moved_goldens and its goldens: section does not name ${#missing[@]}"
else
    echo "FAIL: goldens: this PR moves $moved_goldens and GitHub may not render its goldens: section as this check reads it"
fi
for path in ${missing[@]+"${missing[@]}"}; do
    printf '  not named: %q\n' "${path#"$GOLDEN_DIR"/}"
done
if [ "$doubts" -gt 0 ]; then
    echo "The check cannot tell how GitHub renders this body, so it does not pass it:"
    [ "$DOUBT_COMMENT" = 0 ] || echo "  a comment opens inside a line and does not end on it"
    [ "$DOUBT_BACKTICK" = 0 ] || echo "  a backtick in the section has no partner on its line"
    [ "$DOUBT_NESTED" = 0 ] || echo "  a code fence in the section sits in a list, a quote or an indent"
    [ "$DOUBT_INDENT" = 0 ] || echo "  a name in the section is indented 4 or more, which may be a code block"
    echo "Move the section's names to plain lines or top-level list items."
fi
echo "Name each in backticks under a goldens: heading and say which way it moves. Editing the"
echo "PR body re-runs this check. Format: docs/developer-guide/testing.md, \"The goldens: section\"."
exit 1
