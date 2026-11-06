#!/usr/bin/env bash
#
# Runs deploy/ci-pr-checks/goldens-section.sh against fixture pull requests, and
# holds .github/workflows/ci-goldens-gate.yaml to the shape that makes it safe.
#
# Each case is a real two-commit git repository and a PR body. A case asserts
# the exit code AND a line of the output, so a gate that exits 0 having looked at
# nothing does not pass. The workflow itself runs only on a pull_request event,
# which nothing here can raise: what this file proves is the script, and that
# the workflow hands it the body as data.

set -u

ROOT="${CHECK_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
GATE="$ROOT/deploy/ci-pr-checks/goldens-section.sh"
WORKFLOW="$ROOT/.github/workflows/ci-goldens-gate.yaml"
[ -f "$GATE" ] || { printf 'FATAL: %s not found\n' "$GATE" >&2; exit 1; }
[ -f "$WORKFLOW" ] || { printf 'FATAL: %s not found\n' "$WORKFLOW" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

FAIL=0
PASS=0
CASES=0

G=internal/engines/analyzers/learnreplay/testdata/goldens
A_RESULTS="$G/acceptance/results.csv"
A_LINES="$G/acceptance/lines.csv"
OLS_RESULTS="$G/ols-sweep/results.csv"
OLS_SLO="$G/ols-sweep-slo/results.csv"
TTFT_A="$G/ttft-staircase-a/lines.csv"
TTFT_B="$G/ttft-staircase-b/lines.csv"
COUNTS=internal/engines/steadystate/testdata/engine-run-event-counts.txt
WHATIF=cmd/whatif/testdata/ttft-open-loop-short.golden
SCHEMA=cmd/whatif/testdata/schema-v1.txt
DOC=docs/reference/metrics.md

# The base commit every fixture PR moves files of.
BASE="$WORK/base"
mkdir -p "$BASE"
git -C "$BASE" init -q
git -C "$BASE" config user.email fixture@example.invalid
git -C "$BASE" config user.name fixture
for f in "$A_RESULTS" "$A_LINES" "$OLS_RESULTS" "$OLS_SLO" "$TTFT_A" "$TTFT_B" "$G/goldens.yaml" \
    "$G/acceptance/recording.jsonl.gz" "$G/e1/lines.csv" \
    "$COUNTS" "$WHATIF" "$SCHEMA" "$DOC" cmd/main.go; do
    mkdir -p "$BASE/$(dirname "$f")"
    echo base >"$BASE/$f"
done
git -C "$BASE" add -A
git -C "$BASE" commit -q -m base
BASE_SHA="$(git -C "$BASE" rev-parse HEAD)"

# pr_with FILE...: leaves $BASE at a commit moving each FILE on top of the base.
pr_with() {
    git -C "$BASE" reset -q --hard "$BASE_SHA"
    local f
    for f in "$@"; do
        echo moved >>"$BASE/$f"
    done
    git -C "$BASE" add -A
    git -C "$BASE" commit -q -m pr --allow-empty
}

# expect NAME WANT_RC WANT_SUBSTRING BODY [BASE_REV HEAD_REV]
expect() {
    local name="$1" want_rc="$2" want="$3" body="$4" rev_a="${5:-$BASE_SHA}" rev_b="${6:-HEAD}" out rc=0
    CASES=$((CASES + 1))
    out="$(cd "$BASE" && PR_BODY="$body" bash "$GATE" "$rev_a" "$rev_b" 2>&1)" || rc=$?
    if [ "$rc" -ne "$want_rc" ]; then
        FAIL=$((FAIL + 1)); echo "FAIL $name: exit $rc, want $want_rc: $(printf '%s' "$out" | tr '\n' ' ')"
    elif [ "${out#*"$want"}" = "$out" ]; then
        FAIL=$((FAIL + 1)); echo "FAIL $name: want \"$want\" in: $(printf '%s' "$out" | tr '\n' ' ')"
    else
        PASS=$((PASS + 1)); echo "ok   $name: $(printf '%s' "$out" | head -1)"
    fi
}

# The four cases the gate exists for.
pr_with "$A_RESULTS"
expect "a moved golden and no section fails" 1 "no goldens: section" "$(printf 'Fixes #1\n\n## Proposed Changes\n\n- a change\n')"
expect "the failure names the file" 1 "not named: acceptance/results.csv" "no section here"
expect "a section naming it passes" 0 "the section names each" "$(printf '## goldens:\n\n- `acceptance/results.csv`: decode demand up on 12 cycles\n')"
pr_with "$A_RESULTS" "$A_LINES"
expect "a partial section fails" 1 "does not name 1" "$(printf '## goldens:\n\n- `acceptance/results.csv`: up\n')"
expect "a partial section names what is missing" 1 "not named: acceptance/lines.csv" "$(printf '## goldens:\n\n- `acceptance/results.csv`: up\n')"
pr_with "$DOC"
expect "docs only, no section, passes" 0 "none of them a golden" "just docs"
expect "docs only, empty body, passes" 0 "none of them a golden" ""
pr_with cmd/main.go "$DOC"
expect "code with no golden passes" 0 "none of them a golden" "no section"

# What counts as a golden.
pr_with "$G/acceptance/recording.jsonl.gz"
expect "a recording is a golden" 1 "not named: acceptance/recording.jsonl.gz" "no section"
expect "and is named like one" 0 "names each" 'goldens: `acceptance/recording.jsonl.gz` taken again'
pr_with "$COUNTS"
expect "the engine event counts are a golden" 1 "not named: $COUNTS" "no section"
expect "the event counts by file name" 0 "names each" "$(printf 'goldens:\n\n- `engine-run-event-counts.txt`: one line\n')"
pr_with "$WHATIF"
expect "a whatif golden is a golden" 1 "not named: $WHATIF" "no section"
expect "a whatif golden by glob" 0 "names each" 'goldens: `cmd/whatif/testdata/ttft-*.golden` move by one line'
expect "a whatif golden by its file-name glob" 0 "names each" 'goldens: `ttft-*.golden`'
expect "the whatif directory is not a name" 1 "does not name 1" 'goldens: `cmd/whatif/testdata/*`'
expect "nor is every golden" 1 "does not name 1" 'goldens: `*.golden`'
pr_with "$SCHEMA"
expect "the whatif schema is a golden" 1 "not named: $SCHEMA" "no section"
pr_with "$G/goldens.yaml"
expect "the manifest is a golden" 1 "not named: goldens.yaml" "no section"
expect "the manifest by name" 0 "names each" 'goldens: `goldens.yaml` gains a fixture'

# Every way an earlier PR opened the section.
pr_with "$A_RESULTS"
BODY='`acceptance/results.csv` up'
expect "form: ## goldens:" 0 "names each" "$(printf '## goldens:\n\n%s\n' "$BODY")"
expect "form: ### goldens" 0 "names each" "$(printf '### goldens\n\n%s\n' "$BODY")"
expect "form: ### Goldens" 0 "names each" "$(printf '### Goldens\n\n%s\n' "$BODY")"
expect "form: plain goldens: then a list" 0 "names each" "$(printf 'goldens:\n\n- %s\n' "$BODY")"
expect "form: goldens: on one line" 0 "names each" "goldens: $BODY"
expect "form: **goldens:**" 0 "names each" "**goldens:** $BODY"
expect "form: **Goldens:**" 0 "names each" "**Goldens:** $BODY"
expect "form: **Goldens**:" 0 "names each" "**Goldens**: $BODY"
expect "form: a table" 0 "names each" "$(printf 'goldens:\n\n| Output | Rows |\n| --- | --- |\n| `acceptance/results.csv` | 83 |\n')"
expect "form: a full path" 0 "names each" "goldens: \`$A_RESULTS\`"
expect "form: CRLF, as the web editor sends it" 0 "names each" "$(printf '## goldens:\r\n\r\n- %s\r\n\r\n## Other\r\n' "$BODY")"

# Globs, and what they may not do.
pr_with "$A_RESULTS" "$A_LINES"
expect "a fixture's files by glob" 0 "names each" 'goldens: `acceptance/*`'
expect "a fixture by directory" 0 "names each" 'goldens: `acceptance/`'
expect "a fixture by its name" 0 "names each" 'goldens: new fixture `acceptance`'
pr_with "$G/e1/lines.csv"
expect "a short fixture name is literal" 0 "names each" 'goldens: `e1/*`'
pr_with "$A_RESULTS" "$A_LINES"
pr_with "$OLS_RESULTS"
expect "a fixture name is not a prefix of another" 1 "not named: ols-sweep/results.csv" 'goldens: `ols-sweep-slo/results.csv`'
pr_with "$OLS_SLO"
expect "one fixture's glob stops at its own name" 1 "not named: ols-sweep-slo/results.csv" 'goldens: `ols-sweep/*`'
expect "one fixture's name stops at its own name" 1 "not named: ols-sweep-slo/results.csv" 'goldens: `ols-sweep`'
pr_with "$TTFT_A" "$TTFT_B"
expect "a family by glob" 0 "names each" 'goldens: `ttft-staircase-*/`'
expect "a family with a literal output" 0 "names each" 'goldens: `ttft-*/lines.csv`'
pr_with "$A_RESULTS" "$OLS_RESULTS"
expect "one output across fixtures" 0 "names each" 'goldens: `*/results.csv`'
expect "a bare star names nothing" 1 "does not name 2" 'goldens: `*`'
expect "stars and slashes name nothing" 1 "does not name 2" 'goldens: `*/*` and `**` and `?/*`'
expect "an extension alone names nothing" 1 "does not name 2" 'goldens: `*/*.csv`'
expect "a letter between stars names nothing" 1 "does not name 2" 'goldens: `*e*` and `*e*/*` and `*/*e*`'
expect "under three leading characters is not literal" 1 "does not name 2" 'goldens: `ac*/*` and `*/re*`'
expect "a full path does not lend its directories" 1 "does not name 2" "goldens: \`$G/*/*\`"
expect "a question mark is not literal" 1 "does not name 2" 'goldens: `???/*.csv`'
expect "a star does not cross a slash" 1 "does not name 2" 'goldens: `*.csv` and `acc*.csv`'
expect "a bare file name is not a learner golden" 1 "does not name 2" 'goldens: `results.csv`'
pr_with "$G/goldens.yaml"
expect "a fixture name is not the manifest" 1 "not named: goldens.yaml" 'goldens: `acceptance`'

# Names that do not count.
pr_with "$A_RESULTS"
expect "prose is not a name" 1 "does not name 1" 'goldens: acceptance/results.csv moves'
expect "a name outside the section does not count" 1 "no goldens: section" 'Moved `acceptance/results.csv`.'
expect "a name after the section does not count" 1 "does not name 1" "$(printf '## goldens:\n\nnone\n\n## Notes\n\n`acceptance/results.csv`\n')"
expect "a bold paragraph ends the section" 1 "does not name 1" "$(printf 'goldens: none\n\n**Verified.** `acceptance/results.csv`\n')"
expect "the template's comment names nothing" 1 "does not name 1" "$(printf '## goldens:\n\n<!-- for example `acceptance/results.csv`: up -->\n')"
expect "a comment over several lines names nothing" 1 "does not name 1" "$(printf '## goldens:\n\n<!--\n`acceptance/results.csv`\n-->\n')"
expect "the line a block comment ends on is raw, as GitHub renders it" 1 "does not name 1" "$(printf '## goldens:\n\n<!--\nhow to\n--> `acceptance/results.csv`\n')"
expect "so is a one-line block comment" 1 "does not name 1" "$(printf '## goldens:\n\n<!-- how to --> `acceptance/results.csv`\n')"
expect "a name on the line after a comment counts" 0 "names each" "$(printf '## goldens:\n\n<!-- how to -->\n`acceptance/results.csv`\n')"
expect "a name after a comment over several lines counts" 0 "names each" "$(printf '## goldens:\n\n<!--\nhow to\n-->\n`acceptance/results.csv`\n')"
expect "a comment does not open a section" 1 "no goldens: section" "$(printf '<!--\ngoldens: `acceptance/results.csv`\n-->\n')"
expect "a fence does not open a section" 1 "no goldens: section" "$(printf '```\ngoldens: `acceptance/results.csv`\n```\n')"
expect "a fence names nothing" 1 "does not name 1" "$(printf '## goldens:\n\n```\n`acceptance/results.csv`\n```\n')"
expect "a heading in a fence does not end the section" 0 "names each" "$(printf '## goldens:\n\n```\n# output\n```\n\n`acceptance/results.csv`\n')"
expect "the word in a sentence opens nothing" 1 "no goldens: section" 'The goldens: `acceptance/results.csv` moved.'
expect "a heading about goldens opens nothing" 1 "no goldens: section" "$(printf '## How goldens work\n\n`acceptance/results.csv`\n')"
expect "an empty section fails" 1 "does not name 1" "$(printf '## goldens:\n\n## Other\n')"
expect "an empty body fails" 1 "no goldens: section" ""

# Fences, as GitHub closes them. A wrong close here is the fail-open case: the
# pasted text reads as the section.
LEDE='## goldens:\n\n'
NAME='`acceptance/results.csv`'
expect "a four-backtick fence stays one block over a three-backtick line" 1 "does not name 1" "$(printf "$LEDE"'````\n```\n%s\n```\n%s\n````\n' "$NAME" "$NAME")"
expect "a name after a four-backtick fence counts" 0 "names each" "$(printf "$LEDE"'````\n```\n````\n\n%s\n' "$NAME")"
expect "tildes do not close a backtick fence" 1 "does not name 1" "$(printf "$LEDE"'```\n~~~\n%s\n```\n' "$NAME")"
expect "backticks do not close a tilde fence" 1 "does not name 1" "$(printf "$LEDE"'~~~\n```\n%s\n~~~\n' "$NAME")"
expect "a shorter run does not close a tilde fence" 1 "does not name 1" "$(printf "$LEDE"'~~~~\n~~~\n%s\n~~~~\n' "$NAME")"
expect "a closer takes no text after it" 1 "does not name 1" "$(printf "$LEDE"'```\n``` x\n%s\n```\n' "$NAME")"
expect "a longer run closes a fence" 0 "names each" "$(printf "$LEDE"'```\n`````\n%s\n' "$NAME")"
expect "an unclosed fence runs to the end" 1 "does not name 1" "$(printf "$LEDE"'```\n%s\n' "$NAME")"
expect "a one-line triple-backtick span is not a fence" 0 "names each" "$(printf "$LEDE"'``` x ```\n\n%s\n' "$NAME")"
expect "an opener with a backtick in its info is not a fence" 1 "may not render" "$(printf "$LEDE"'``` a`b
%s
' "$NAME")"
expect "a tilde fence may hold a backtick in its info" 1 "does not name 1" "$(printf "$LEDE"'~~~ a`b\n%s\n~~~\n' "$NAME")"
expect "a fence in a list item is a doubt" 1 "may not render" "$(printf "$LEDE"'%s\n\n- ```\n  x\n  ```\n' "$NAME")"
expect "a fence in a quote is a doubt" 1 "may not render" "$(printf "$LEDE"'%s\n\n> ```\n> x\n> ```\n' "$NAME")"
expect "an indented fence is a doubt" 1 "may not render" "$(printf "$LEDE"'%s\n\n    ```\n' "$NAME")"
expect "and says what it doubts" 1 "sits in a list, a quote or an indent" "$(printf "$LEDE"'%s\n\n- ```\n  x\n  ```\n' "$NAME")"

# Spans and comments, read left to right: whichever opens first hides the other.
expect "a comment opener in a span opens nothing" 0 "names each" "$(printf "$LEDE"'- `<!--` opens a comment\n- %s\n' "$NAME")"
expect "a comment opener in a long span opens nothing" 0 "names each" "$(printf "$LEDE"'- ``<!--`` opens a comment\n- %s\n' "$NAME")"
expect "a span in an inline comment names nothing" 1 "does not name 1" "$(printf 'goldens: none <!-- %s --> here\n' "$NAME")"
expect "a name after an inline comment counts" 0 "names each" "$(printf 'goldens: <!-- x --> %s\n' "$NAME")"
expect "an inline comment with no end on its line is a doubt" 1 "may not render" "$(printf 'goldens: %s <!-- x\n\ny -->\n' "$NAME")"
expect "a less-than sign is text" 0 "names each" "$(printf 'goldens: 1 < 2 %s\n' "$NAME")"
expect "an escaped backtick opens no span" 1 "does not name 1" 'goldens: \`acceptance/results.csv\`'
expect "an escaped backtick pairs with nothing" 0 "names each" 'goldens: \` then `acceptance/results.csv`'
expect "a double-backtick span names" 0 "names each" 'goldens: ``acceptance/results.csv``'
expect "a span's padding is not the name" 0 "names each" 'goldens: `` acceptance/results.csv ``'
expect "a span of another length does not close one" 1 "cannot tell how GitHub renders" 'goldens: ``acceptance/results.csv`'
expect "a stray backtick is a doubt" 1 "may not render" "$(printf 'goldens: %s and one ` more\n' "$NAME")"
expect "and says what it doubts" 1 "has no partner on its line" "$(printf 'goldens: %s and one ` more\n' "$NAME")"
expect "a span across lines is a doubt" 1 "may not render" "$(printf "$LEDE"'%s `a\nb`\n' "$NAME")"
expect "a stray backtick just above the section is a doubt" 1 "may not render" "$(printf 'one ` here\ngoldens: %s\n' "$NAME")"
expect "a stray backtick elsewhere is no doubt" 0 "names each" "$(printf 'one ` here\n\ngoldens: %s\n\n## Other\n\none ` there\n' "$NAME")"
expect "an indented name is a doubt" 1 "may not render" "$(printf "$LEDE"'    %s\n' "$NAME")"
expect "a tab-indented name is a doubt" 1 "may not render" "$(printf "$LEDE"'\t%s\n' "$NAME")"
expect "a nested list item is not indented enough to doubt" 0 "names each" "$(printf "$LEDE"'- moved:\n  - %s\n' "$NAME")"
expect "a heading indented up to three ends the section" 1 "does not name 1" "$(printf 'goldens: none\n\n  ## Notes\n\n%s\n' "$NAME")"

# Paths git would C-quote. Unquoted, each is a golden like any other.
for odd in 'a"b.csv' "$(printf 'a\tb.csv')" 'a\b.csv' "$(printf 'a\nb.csv')"; do
    pr_with "$G/acceptance/$odd"
    expect "a path git quotes is a golden: $(printf '%q' "$odd")" 1 "this PR moves 1 golden and" "no section"
    expect "and a glob names it: $(printf '%q' "$odd")" 0 "names each" 'goldens: `acceptance/*`'
done
pr_with "$G/acceptance/$(printf 'x\n::error::injected.csv')"
CASES=$((CASES + 1))
OUT="$(cd "$BASE" && PR_BODY="none" bash "$GATE" "$BASE_SHA" HEAD 2>&1)"
if printf '%s\n' "$OUT" | grep -q '^::'; then
    FAIL=$((FAIL + 1)); echo "FAIL a path reached a line of its own: $OUT"
else
    PASS=$((PASS + 1)); echo "ok   a path with a newline prints no workflow command"
fi
pr_with cmd/main.go "$DOC"
expect "the file count is plural" 0 "2 files changed" "no section"
pr_with "$DOC"
expect "the file count is singular" 0 "1 file changed" "no section"

# Renames and deletions: both ends of a rename move.
pr_with "$A_RESULTS"
git -C "$BASE" reset -q --hard "$BASE_SHA"
git -C "$BASE" mv "$A_RESULTS" "$G/acceptance/renamed.csv"
git -C "$BASE" commit -q -m rename
expect "a rename moves both ends" 1 "does not name 1" 'goldens: `acceptance/renamed.csv`'
expect "a rename named at both ends passes" 0 "names each" 'goldens: `acceptance/renamed.csv` and `acceptance/results.csv`'
git -C "$BASE" reset -q --hard "$BASE_SHA"
git -C "$BASE" rm -q "$A_LINES"
git -C "$BASE" commit -q -m delete
expect "a deleted golden moved" 1 "not named: acceptance/lines.csv" "no section"

# The body is data.
pr_with "$A_RESULTS"
PWN="$WORK/pwned"
for hostile in "\$(touch $PWN)" "\`touch $PWN\`" "; touch $PWN" "\${IFS}touch $PWN"; do
    expect "a hostile body is not run: $hostile" 1 "FAIL: goldens" "$(printf 'goldens:\n\n%s\n`%s`\n' "$hostile" "$hostile")"
done
expect "a hostile span beside a real name is not run" 0 "names each" "$(printf 'goldens: `acceptance/results.csv` `$(touch %s)`\n' "$PWN")"
CASES=$((CASES + 1))
if [ -e "$PWN" ]; then
    FAIL=$((FAIL + 1)); echo "FAIL a hostile body ran: $PWN exists"
else
    PASS=$((PASS + 1)); echo "ok   no hostile body ran"
fi
expect "an extended glob is not a pattern" 1 "does not name 1" 'goldens: `@(acceptance)/results.csv`'
expect "a bracket is not a pattern" 1 "does not name 1" 'goldens: `[a-z]*/results.csv`'
CASES=$((CASES + 1))
OUT="$(cd "$BASE" && PR_BODY="$(printf 'goldens:\n::error::injected\n`::warning::injected`\n')" bash "$GATE" "$BASE_SHA" HEAD 2>&1)"
if [ "${OUT#*injected}" != "$OUT" ]; then
    FAIL=$((FAIL + 1)); echo "FAIL the body reached the job's output: $OUT"
else
    PASS=$((PASS + 1)); echo "ok   nothing of the body reaches the job's output"
fi

# An unread diff is not "no golden moved".
pr_with "$A_RESULTS"
expect "an unknown base fails" 2 "could not diff" "" 0000000000000000000000000000000000000000 HEAD
expect "an unresolved parent fails" 2 "could not diff" "" 'HEAD~99' HEAD
expect "an empty diff fails" 2 "the diff is empty" "" HEAD HEAD
CASES=$((CASES + 1))
if out="$(cd "$BASE" && bash "$GATE" 2>&1)"; then
    FAIL=$((FAIL + 1)); echo "FAIL no arguments exits 0: $out"
else
    PASS=$((PASS + 1)); echo "ok   no arguments is a usage error"
fi

# hold NAME GREP_PATTERN: the workflow must match; refuse NAME GREP_PATTERN: must
# not. Its comments say what it must not do, so they are left out.
WF_CODE="$WORK/workflow-code"
grep -Ev '^[[:space:]]*#' "$WORKFLOW" >"$WF_CODE"
hold() {
    CASES=$((CASES + 1))
    if grep -Eq "$2" "$WF_CODE"; then
        PASS=$((PASS + 1)); echo "ok   workflow: $1"
    else
        FAIL=$((FAIL + 1)); echo "FAIL workflow: $1: no line matches $2"
    fi
}
refuse() {
    CASES=$((CASES + 1))
    if grep -Eq "$2" "$WF_CODE"; then
        FAIL=$((FAIL + 1)); echo "FAIL workflow: $1: $(grep -En "$2" "$WF_CODE" | head -1)"
    else
        PASS=$((PASS + 1)); echo "ok   workflow: $1"
    fi
}
hold "it runs on a pull request" '^  pull_request:$'
hold "correcting the section re-runs it" '^    types: \[.*\bedited\b.*\]'
hold "a new commit re-runs it" '^    types: \[.*\bsynchronize\b.*\]'
hold "the body arrives in an env var" '^ +PR_BODY: \$\{\{ github\.event\.pull_request\.body \}\}$'
hold "the token reads and no more" '^  contents: read$'
hold "it runs the script, on the merge and its base" '^ +run: bash deploy/ci-pr-checks/goldens-section\.sh HEAD\^1 HEAD$'
hold "the checkout keeps the merge's parent" '^ +fetch-depth: 2$'
hold "the checkout leaves no token behind" '^ +persist-credentials: false$'
refuse "no pull_request_target" 'pull_request_target'
refuse "nothing writes" ':[[:space:]]*write\b'
refuse "no permission beyond contents" '^ +(actions|checks|deployments|issues|packages|pull-requests|security-events|statuses|id-token):'
refuse "the event never reaches a shell" '^ +run:.*\$\{\{'

# A multi-line run block would hide an expression from the one-line check above.
CASES=$((CASES + 1))
if awk '
    /^[ ]+(- )?run:[ ]*[|>]/ { match($0, /^[ ]*/); indent = RLENGTH; inblock = 1; next }
    inblock { match($0, /^[ ]*/); if (length($0) && RLENGTH <= indent) inblock = 0 }
    inblock && /\$\{\{/ { bad = 1 }
    END { exit bad }
' "$WF_CODE"; then
    PASS=$((PASS + 1)); echo "ok   workflow: no expression in a run block"
else
    FAIL=$((FAIL + 1)); echo "FAIL workflow: an expression sits in a run block"
fi

echo "$CASES cases: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ] && [ "$PASS" -eq "$CASES" ] && [ "$CASES" -gt 0 ]
