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

# section LINE...: a body with the section holding those lines.
section() {
    printf '## goldens:\n\n'
    printf '%s\n' "$@"
}

R='- `acceptance/results.csv`'
L='- `acceptance/lines.csv`'

# The four cases the gate exists for.
pr_with "$A_RESULTS"
expect "a moved golden and no section fails" 1 "no goldens: section" "$(printf 'Fixes #1\n\n## Proposed Changes\n\n- a change\n')"
expect "the failure names the file" 1 "not named: acceptance/results.csv" "no section here"
expect "a section naming it passes" 0 "the section names each" "$(section "$R")"
pr_with "$A_RESULTS" "$A_LINES"
expect "a partial section fails" 1 "does not hold up" "$(section "$R")"
expect "a partial section names what is missing" 1 "not named: acceptance/lines.csv" "$(section "$R")"
pr_with "$DOC"
expect "docs only, no section, passes" 0 "none of them a golden" "just docs"
expect "docs only, empty body, passes" 0 "none of them a golden" ""
expect "docs only, section none, passes" 0 "none of them a golden" "$(section none)"
expect "docs only reads no section" 0 "none of them a golden" "$(section 'an old body in prose')"
pr_with cmd/main.go "$DOC"
expect "code with no golden passes" 0 "2 files changed" "no section"
pr_with "$DOC"
expect "the file count is singular" 0 "1 file changed" "no section"

# What counts as a golden, and how each is named.
pr_with "$G/acceptance/recording.jsonl.gz"
expect "a recording is a golden" 1 "not named: acceptance/recording.jsonl.gz" "no section"
expect "and is named like one" 0 "names each" "$(section '- `acceptance/recording.jsonl.gz` - taken again')"
pr_with "$COUNTS"
expect "the engine event counts are a golden" 1 "not named: $COUNTS" "no section"
expect "the event counts by file name" 0 "names each" "$(section '- `engine-run-event-counts.txt`')"
expect "the event counts by full path" 0 "names each" "$(section "- \`$COUNTS\`")"
pr_with "$WHATIF"
expect "a whatif golden is a golden" 1 "not named: $WHATIF" "no section"
expect "a whatif golden by glob" 0 "names each" "$(section '- `cmd/whatif/testdata/ttft-*.golden` - one line each')"
expect "a whatif golden by its file-name glob" 0 "names each" "$(section '- `ttft-*.golden`')"
pr_with "$SCHEMA"
expect "the whatif schema is a golden" 1 "not named: $SCHEMA" "no section"
expect "the whatif schema by path" 0 "names each" "$(section "- \`$SCHEMA\`")"
pr_with "$G/goldens.yaml"
expect "the manifest is a golden" 1 "not named: goldens.yaml" "no section"
expect "the manifest by name" 0 "names each" "$(section '- `goldens.yaml`')"
pr_with "$A_RESULTS"
expect "a learner golden by full path" 0 "names each" "$(section "- \`$A_RESULTS\`")"

# The format, where it holds.
expect "free text after a dash" 0 "names each" "$(section '- `acceptance/results.csv` - decode up on 12 cycles, by `0.5 %`')"
expect "text before the heading is ignored" 0 "names each" "$(printf 'Fixes #1\n\n`x` goldens: here\n\n'; section "$R")"
expect "a line starting # ends the section" 0 "names each" "$(section "$R"; printf '\n### Replay summary\n\nany text `at all`\n```\n')"
expect "CRLF, as the web editor sends it" 0 "names each" "$(printf '## goldens:\r\n\r\n- `acceptance/results.csv`\r\n\r\n## Other\r\ntext\r\n')"
expect "blank lines are allowed" 0 "names each" "$(section '' "$R" '   ' '')"
expect "the section may end the body" 0 "names each" "$(printf '## goldens:\n- `acceptance/results.csv`')"
pr_with "$A_RESULTS" "$A_LINES"
expect "two lines name two" 0 "names each" "$(section "$R" "$L")"
expect "a fixture's outputs by glob" 0 "names each" "$(section '- `acceptance/*.csv`')"
pr_with "$TTFT_A" "$TTFT_B"
expect "a family by glob" 0 "names each" "$(section '- `ttft-staircase-*/lines.csv`')"
expect "a family's outputs by glob" 0 "names each" "$(section '- `ttft-*/*.csv`')"
pr_with "$A_RESULTS" "$OLS_RESULTS"
expect "one output across fixtures" 0 "names each" "$(section '- `*/results.csv`')"
expect "a star does not cross a slash" 1 "does not hold up" "$(section '- `acc*.csv`' '- `ols*.csv`')"
pr_with "$G/e1/lines.csv"
expect "a short fixture name is literal" 0 "names each" "$(section '- `e1/*.csv`')"
pr_with "$OLS_RESULTS"
expect "a fixture name is not a prefix of another" 1 "not named: ols-sweep/results.csv" "$(section '- `ols-sweep-slo/results.csv`')"
pr_with "$OLS_SLO"
expect "one fixture's glob stops at its own name" 1 "not named: ols-sweep-slo/results.csv" "$(section '- `ols-sweep/*.csv`')"

# none.
pr_with "$A_RESULTS"
expect "none fails when a golden moved" 1 "none, and a golden moved" "$(section none)"
expect "none beside the name still fails" 1 "none, and a golden moved" "$(section none "$R")"

# One section, opened by that one line.
expect "two sections fail" 1 "2 goldens: sections" "$(section "$R"; printf '\n'; section "$R")"
expect "two sections fail, the second empty" 1 "2 goldens: sections" "$(section "$R"; printf '\n## goldens:\n')"
for line in '### goldens:' '## goldens' '## Goldens:' ' ## goldens:' '## goldens: ' '##  goldens:' \
    'goldens:' '**goldens:**' '**Goldens**:' '- **Goldens:**' 'goldens: `acceptance/results.csv`'; do
    expect "not the heading: $line" 1 "no goldens: section" "$(printf '%s\n\n%s\n' "$line" "$R")"
done
expect "an empty section fails" 1 "not named: acceptance/results.csv" "$(printf '## goldens:\n\n## Other\n')"
expect "an empty body fails" 1 "no goldens: section" ""
expect "a name past the section does not count" 1 "not named: acceptance/results.csv" "$(printf '## goldens:\n\n# Notes\n\n%s\n' "$R")"
expect "an indented heading does not end the section" 1 "not of the required form" "$(section "$R" '  ## Notes')"

# Everything else in the section fails, beside a good name, so the line is the
# only reason. Each of these got through a Markdown parser written here once, or
# would have: fences of each kind and an indented closer, a span across lines, a
# setext heading, indented code, raw HTML, comments, and the forms earlier PRs
# used. `form` takes the section's lines, and wants the bad line's number.
form() {
    local name="$1"
    shift
    expect "not the form: $name" 1 "line 4 of the body: not of the required form" "$(section "$R" "$@")"
}
form "a fence" '```' "$R" '```'
form "a four-backtick fence" '````' '```' '````'
form "a tilde fence" '~~~' '~~~'
form "an indented closer" '  ```'
form "a one-line triple-backtick span" '``` x ```'
form "a fence in a list item" '- ```'
form "a fence in a quote" '> ```'
form "a span across lines" 'a `b' 'c` d'
form "a setext heading" 'Title' '---'
form "a thematic break" '---'
form "an equals underline" '==='
form "a dash, then indented code" '-     `acceptance/lines.csv`'
form "two spaces after the dash" '-  `acceptance/lines.csv`'
form "an indented list item" '  - `acceptance/lines.csv`'
form "a star bullet" '* `acceptance/lines.csv`'
form "a plus bullet" '+ `acceptance/lines.csv`'
form "a numbered item" '1. `acceptance/lines.csv`'
form "a quoted item" '> - `acceptance/lines.csv`'
form "a bare span" '`acceptance/lines.csv`'
form "a span in a sentence" 'moved `acceptance/lines.csv` up'
form "prose" 'acceptance/lines.csv moves'
form "a colon after the name" '- `acceptance/lines.csv`: up'
form "text with no dash" '- `acceptance/lines.csv` up'
form "a dash with no text" '- `acceptance/lines.csv` -'
form "two names on a line" '- `acceptance/lines.csv` `goldens.yaml`'
form "a double-backtick span" '- ``acceptance/lines.csv``'
form "escaped backticks" '- \`acceptance/lines.csv\`'
form "an unclosed span" '- `acceptance/lines.csv'
form "an empty span" '- ``'
form "a one-line comment" '<!-- `acceptance/lines.csv` -->'
form "a comment over lines" '<!--' 'how to' '-->'
form "raw HTML, a pre" '<pre>' "$R" '</pre>'
form "raw HTML, details" '<details>' '</details>'
form "raw HTML, an inline tag" '<b>acceptance/lines.csv</b>'
form "a table" '| Output | Rows |' '| --- | --- |'
form "none in a sentence" 'none move'
form "None" 'None'
form "none with a dash" '- none'
form "a tab before the dash" "$(printf '\t- `acceptance/lines.csv`')"
form "a trailing space" '- `acceptance/lines.csv` '
expect "the template's old comment fails" 1 "not of the required form" "$(section '<!-- for example `acceptance/results.csv`: up -->')"
expect "a section in a comment fails" 1 "not of the required form" "$(printf '<!--\n## goldens:\n- `acceptance/results.csv`\n-->\n')"
expect "a section in a fence fails" 1 "not of the required form" "$(printf '```\n## goldens:\n- `acceptance/results.csv`\n```\n')"

# Names, beside a good one, so the rule is the only reason.
name() {
    expect "not a name: $1" 1 "line 4 of the body: the name is not allowed" "$(section "$R" "- \`$1\`")"
}
for bad in '*' '**' '*/*' '?/*' '*.csv' '*/*.csv' '*.gz' '*e*' '*e*/*' '*/*e*' '*/re*.csv' 'ac*/*.csv' '???/*.csv' \
    'acceptance/*' 'acceptance/???' '*/*/results.csv' \
    '*.golden' '*.txt' '*.yaml' 'cmd/whatif/testdata/*' 'cmd/whatif/testdata/*.golden' 'cmd/whatif/testdata/*/x.golden' \
    'cmd/*/testdata/ttft-open-loop-short.golden' 'internal/engines/steadystate/testdata/*.txt' \
    "$G/*" "$G/*/*" "$G/*/*.csv" "$G/*/*/lines.csv" \
    'internal/*/*/*/*/*/*/*' 'internal/*/*/*/*/*/*/*/results.csv' 'internal/engines/*/results.csv' \
    'internal/engines/analyzers/learnreplay/testdata/*/acceptance/results.csv' \
    '<!--' '@(acceptance)/results.csv' '[a-z]*/results.csv' 'a b.csv' '$(id)' '../acceptance/results.csv/x'; do
    name "$bad"
done
pr_with "$A_RESULTS" "$A_LINES"
expect "a literal directory lends a glob nothing" 1 "not allowed" "$(section '- `internal/*/*/*/*/*/*/*`')"
expect "a refused name names nothing" 1 "not named: acceptance/lines.csv" "$(section "$R" '- `*/*.csv`')"
expect "a name that is no file is not a pass" 1 "not named: acceptance/results.csv" "$(section '- `acceptance`' '- `acceptance/`')"

# Paths git would C-quote. Unquoted, each is a golden like any other.
for odd in 'a"b.csv' "$(printf 'a\tb.csv')" 'a\b.csv' "$(printf 'a\nb.csv')"; do
    pr_with "$G/acceptance/$odd"
    expect "a path git quotes is a golden: $(printf '%q' "$odd")" 1 "this PR moves 1 golden and" "no section"
    expect "and a glob names it: $(printf '%q' "$odd")" 0 "names each" "$(section '- `acceptance/*.csv`')"
done
pr_with "$G/acceptance/$(printf 'x\n::error::injected.csv')"
CASES=$((CASES + 1))
OUT="$(cd "$BASE" && PR_BODY="none" bash "$GATE" "$BASE_SHA" HEAD 2>&1)"
if printf '%s\n' "$OUT" | grep -q '^::'; then
    FAIL=$((FAIL + 1)); echo "FAIL a path reached a line of its own: $OUT"
else
    PASS=$((PASS + 1)); echo "ok   a path with a newline prints no workflow command"
fi

# Renames and deletions: both ends of a rename move.
pr_with "$A_RESULTS"
git -C "$BASE" reset -q --hard "$BASE_SHA"
git -C "$BASE" mv "$A_RESULTS" "$G/acceptance/renamed.csv"
git -C "$BASE" commit -q -m rename
expect "a rename moves both ends" 1 "not named: acceptance/results.csv" "$(section '- `acceptance/renamed.csv`')"
expect "a rename named at both ends passes" 0 "names each" "$(section '- `acceptance/renamed.csv`' "$R")"
git -C "$BASE" reset -q --hard "$BASE_SHA"
git -C "$BASE" rm -q "$A_LINES"
git -C "$BASE" commit -q -m delete
expect "a deleted golden moved" 1 "not named: acceptance/lines.csv" "no section"

# The body is data.
pr_with "$A_RESULTS"
PWN="$WORK/pwned"
for hostile in "\$(touch $PWN)" "\`touch $PWN\`" "; touch $PWN" "\${IFS}touch $PWN"; do
    expect "a hostile line is not run: $hostile" 1 "FAIL: goldens" "$(section "$hostile" "- \`$hostile\`")"
done
expect "a hostile free text is not run" 0 "names each" "$(section "$R - \$(touch $PWN) \`touch $PWN\`")"
CASES=$((CASES + 1))
if [ -e "$PWN" ]; then
    FAIL=$((FAIL + 1)); echo "FAIL a hostile body ran: $PWN exists"
else
    PASS=$((PASS + 1)); echo "ok   no hostile body ran"
fi
CASES=$((CASES + 1))
OUT="$(cd "$BASE" && PR_BODY="$(section '::error::injected' '- `::warning::injected`' '- `*/*.csv` - ::notice::injected')" bash "$GATE" "$BASE_SHA" HEAD 2>&1)"
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
