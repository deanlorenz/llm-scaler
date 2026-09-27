# Shared ownership and argument handling for the benchmark capture helpers.
#
# Sourced by sample_replicas.sh and tail_wva_logs.sh. It is shared because the
# logic lived twice and had already drifted: one script grew an optional
# namespace on stop that the other did not, so the two stop signatures became
# mirror images and getting them backwards silently left a capture running.
#
# THE DESIGN, and why it is this rather than a lock protocol.
#
# Two runs fighting over one output path was the original problem: it produced a
# samples file with two comma-less writers and a sampler whose pidfile had been
# overwritten, so no stop could reach it. The first attempt at a fix was a mutex
# -- mkdir a lock directory, refuse if someone holds it. That needed four things
# to be right at once (an atomic claim-and-record, pid identity, EEXIST versus
# EACCES, process-group teardown) and got each of them wrong once.
#
# So the contention is removed instead of arbitrated: the Makefile gives every
# invocation its own output path, keyed by a run id that is fixed once per `make`
# run. Two concurrent runs cannot collide because they never share a file.
#
# What remains here is not a mutex. It is:
#   - a record of WHO owns a live capture, so a human finding one in /tmp can see
#     its namespace and cluster, and so stop WARNS when the namespace or context
#     it was given does not match. It warns and proceeds: naming a path by hand is
#     taken as meaning it, and refusing would strand a capture whose owner has
#     gone home. It is not a permission check;
#   - an exclusive CREATE of that record, so the degenerate case of two captures
#     aimed by hand at one path still fails safely rather than interleaving;
#   - identity that survives pid reuse, so stop never kills an unrelated process.

CAPTURE_CTX=""
CAPTURE_FORCE=0
CAPTURE_POSITIONAL=()
CAPTURE_KUBECTL="${KUBECTL_CMD:-kubectl}"
CAPTURE_ADOPTED_PID=""
CAPTURE_OUT=""
CAPTURE_NS=""

# capture_parse_args "$@" -- pull the flags out from ANYWHERE in the argument
# list, leaving the positionals in CAPTURE_POSITIONAL.
#
# Position-independent on purpose. When flags were recognised only before the
# subcommand, `start <ns> <out> --context c` exited 0 with no context reaching
# kubectl: the capture watched whatever KUBECONFIG said while the operator
# believed they had named a cluster -- the exact failure --context was added to
# prevent, produced by the natural word order.
#
# Every spelling that cannot be honoured is an error rather than a default.
# `--context=` used to mean "inherited", and it is the one spelling a wrapper
# produces from an unset variable.
capture_parse_args() {
    CAPTURE_CTX=""
    CAPTURE_FORCE=0
    CAPTURE_POSITIONAL=()
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --context)
                if [ "$#" -lt 2 ] || [ -z "$2" ]; then
                    echo "--context needs a value" >&2
                    return 2
                fi
                CAPTURE_CTX="$2"; shift 2 ;;
            --context=*)
                CAPTURE_CTX="${1#--context=}"
                if [ -z "$CAPTURE_CTX" ]; then
                    echo "--context= needs a value" >&2
                    return 2
                fi
                shift ;;
            --force) CAPTURE_FORCE=1; shift ;;
            --) shift
                while [ "$#" -gt 0 ]; do CAPTURE_POSITIONAL+=("$1"); shift; done ;;
            -?*) echo "unknown flag: $1" >&2; return 2 ;;
            *) CAPTURE_POSITIONAL+=("$1"); shift ;;
        esac
    done
    return 0
}

# capture_kube ... -- every kubectl call goes through this, so the context
# cannot reach some calls and miss others. KUBECTL_CMD stays unquoted so a
# multi-word override keeps working, as it did before.
capture_kube() {
    if [ -n "$CAPTURE_CTX" ]; then
        $CAPTURE_KUBECTL --context "$CAPTURE_CTX" "$@"
    else
        $CAPTURE_KUBECTL "$@"
    fi
}

# _capture_starttime <pid> -- the kernel's start time for a pid, in clock ticks
# since boot. Field 22 of /proc/<pid>/stat, read past the comm field so a process
# name containing spaces or parentheses cannot shift the offset.
#
# A pid alone is not an identity: the number is reused, and a recycled pid made
# stop kill an unrelated process and made start refuse forever. A pid plus its
# start time is unique for the life of the machine.
_capture_starttime() {
    local line rest
    local -a f
    [ -r "/proc/$1/stat" ] || return 1
    # Builtins only: this runs inside the exclusive redirect that creates the
    # ownership record, and every fork widens the window in which another caller
    # sees a record that exists but is still empty. With sed, awk, date and
    # basename that window measured 5.66 ms; a losing racer hit it every time.
    # The default word split, whatever the caller set IFS to. Not reachable from
     # the shipped scripts, but this file is sourceable.
    local IFS=$' \t\n'
    read -r line < "/proc/$1/stat" || return 1
    # Drop "pid (comm) ". The comm may contain spaces and parentheses, so strip
    # through the LAST ') ' rather than the first.
    rest="${line##*') '}"
    f=($rest)
    # f[0] is field 3 (state), so field 22 (starttime) is index 19.
    printf '%s\n' "${f[19]:-}"
}

# capture_owner_file <outfile>
capture_owner_file() {
    printf '%s.owner\n' "$1"
}

# capture_record_path <outfile> -- where the ownership record currently IS.
#
# `<out>.owner` until a stop claims it, `<out>.finalising.<pid>` afterwards. The
# claim is a rename, so the record moves; hard-coding `.owner` in the readers meant
# a recovering stop could not identify the capture it was finalising, declined to
# kill it, and wrote the terminator while the sampler was still appending.
capture_record_path() {
    local v
    [ -n "${1:-}" ] || return 1
    if [ -s "$(capture_owner_file "$1")" ]; then
        printf '%s\n' "$(capture_owner_file "$1")"
        return 0
    fi
    for v in "$1".finalising.*; do
        if [ -s "$v" ]; then
            printf '%s\n' "$v"
            return 0
        fi
    done
    return 1
}

# capture_alive <outfile> -- is the recorded owner still the process we recorded?
#
# Returns 0 only when the pid is alive AND its start time matches what was
# recorded. On a system without /proc the start time is absent and the pid alone
# is trusted, which is the previous behaviour rather than a new risk.
capture_alive() {
    local owner pid recorded now rp
    rp="$(capture_record_path "$1")" || return 1
    owner="$(cat "$rp" 2>/dev/null)" || return 1
    pid="$(printf '%s' "$owner" | sed -n 's/.*[^a-z]pid=\([0-9][0-9]*\).*/\1/p')"
    [ -n "$pid" ] || return 1
    kill -0 "$pid" 2>/dev/null || return 1
    # An ABSENT starttime field means the record predates this check or the
    # system has no /proc, and the pid alone is trusted. An EMPTY one means we
    # tried and failed to read it -- which happens when the process was already
    # gone -- so the record is not evidence that this pid is ours.
    case "$owner" in
        *starttime=*) recorded="$(printf '%s' "$owner" | sed -n 's/.*starttime=\([0-9]*\).*/\1/p')" ;;
        *) return 0 ;;
    esac
    [ -n "$recorded" ] || return 1
    now="$(_capture_starttime "$pid")" || return 0
    [ "$now" = "$recorded" ]
}

# capture_claim <outfile> -- create the ownership record, exclusively.
#
# `set -C` makes the redirect an O_EXCL create, so the file is created and filled
# in one step and two callers cannot both succeed. The first attempt at this used
# mkdir for the claim and wrote the record afterwards, which left a window --
# measured at 4 ms -- in which a second caller saw a claim with no record, read
# it as a crashed run, and reclaimed it. Both starts then succeeded.
#
# An existing record whose owner is alive is refused. One whose owner is gone is
# a crashed run and is replaced, with a note.
capture_claim() {
    local out="$1" ns="$2" of
    of="$(capture_owner_file "$out")"
    # Created AND filled by one redirect. Creating it empty and filling it a
    # moment later left a window in which a second caller read a record with no
    # pid, concluded the owner was gone, and took over a path it should have
    # lost -- then deleted the winner's record on its way out of the data guard.
    if ( set -C; _capture_owner_line "$ns" "$$" > "$of" ) 2>/dev/null; then
        return 0
    fi
    if [ ! -e "$of" ]; then
        echo "cannot create $of -- refusing to start a capture nothing can find" >&2
        return 1
    fi
    # An EMPTY record means another caller is between its create and its write.
    # Treating that as abandoned is what let both callers through.
    if [ ! -s "$of" ]; then
        # Another caller is between its create and its write. Deliberately no
        # advice to remove the record: in a race this is the message the LOSER
        # sees, and the record belongs to the winner.
        echo "refusing to start: $out is being claimed by another process right now" >&2
        echo "  retry, or use a different output path" >&2
        return 1
    fi
    if capture_alive "$out"; then
        echo "refusing to start: a capture already owns $out" >&2
        echo "  holder: $(cat "$of" 2>/dev/null)" >&2
        echo "  stop that capture first, or choose another output path" >&2
        return 1
    fi
    echo "note: replacing a capture of $out whose owner is gone ($(cat "$of" 2>/dev/null))" >&2
    _capture_write_owner "$out" "$ns" "$$"
}

# _capture_owner_line <namespace> <pid> -- the record, on stdout, so it can be
# produced inside the exclusive redirect that creates the file.
_capture_owner_line() {
    local st when
    st="$(_capture_starttime "$2" || true)"
    # printf's %()T and ${0##*/} instead of date and basename, for the reason in
    # _capture_starttime: no forks on the path that creates the record. TZ is set
    # for this one builtin call because %()T formats in local time.
    when="$(TZ=UTC0 printf '%(%Y-%m-%dT%H:%M:%SZ)T' -1 2>/dev/null)" || when=""
    printf 'namespace=%s context=%s started=%s pid=%s starttime=%s cmd=%s\n' \
        "$1" "${CAPTURE_CTX:-<inherited KUBECONFIG>}" \
        "${when:-unknown}" "$2" "${st:-}" "${0##*/}"
}

_capture_write_owner() {
    _capture_owner_line "$2" "$3" > "$(capture_owner_file "$1")"
}

# capture_adopt <outfile> <namespace> <pid> -- hand the claim to the background
# capture once it exists. Safe to do unlocked: the exclusive create above already
# settled who owns this path.
capture_adopt() {
    CAPTURE_ADOPTED_PID="$3"
    _capture_write_owner "$1" "$2" "$3"
}

# capture_guard_data <outfile> -- refuse to destroy measurements already on disk.
#
# Distinct from ownership: this is about a capture eating a FINISHED run, and only
# this is excused by --force.
capture_guard_data() {
    local out="$1"
    if [ "$CAPTURE_FORCE" -eq 1 ] || [ ! -s "$out" ]; then
        return 0
    fi
    echo "refusing to start: $out already holds $(wc -c < "$out" 2>/dev/null || echo "?") bytes of a previous run" >&2
    echo "  pass --force to replace it, or choose another output path" >&2
    return 1
}

# capture_verify_writable <outfile> -- the path must actually accept bytes.
#
# start used to print `replica sampler started (pid )` after three Permission
# denied lines and exit 0, leaving a subshell polling a shared cluster and
# writing nothing, with the Makefile's `|| true` hiding all of it.
capture_verify_writable() {
    local out="$1"
    mkdir -p "$(dirname "$out")" 2>/dev/null || true
    if ! : > "$out" 2>/dev/null; then
        echo "cannot write $out -- refusing to start a capture that collects nothing" >&2
        return 1
    fi
    return 0
}

# capture_owned <outfile> -- is there an ownership record for this path?
#
# stop consults this before touching the file. Without it, stop appended a JSON
# terminator to whatever was at the path -- including a file start had just
# REFUSED to overwrite, turning a protected run into unparseable output.
capture_owned() {
    local v vp
    [ -n "${1:-}" ] || return 1
    if [ -s "$(capture_owner_file "$1")" ]; then
        return 0
    fi
    # No record, but a claim may be in flight. A LIVE holder means another stop is
    # finishing this file and there is nothing for us to do -- not owned. A holder
    # that is GONE means a stop died mid-finalise and the file still needs closing;
    # treating that as nothing-to-stop wedged the path for ever.
    for v in "$1".finalising.*; do
        [ -e "$v" ] || continue
        vp="${v##*.}"
        case "$vp" in ''|*[!0-9]*) continue ;; esac
        kill -0 "$vp" 2>/dev/null || return 0
    done
    return 1
}

# capture_check_owner <outfile> <namespace> <context> -- warn on a mismatch.
#
# Both fields, because comparing the namespace alone made finalising a capture
# belonging to a different cluster silent. An empty argument is "not stated", not
# a mismatch: warning when the caller named nothing is a false alarm.
capture_check_owner() {
    local owner rp
    [ -n "${1:-}" ] || return 0
    rp="$(capture_record_path "$1")" || return 0
    owner="$(cat "$rp" 2>/dev/null)" || return 0
    [ -n "$owner" ] || return 0
    if [ -n "${2:-}" ]; then
        case "$owner" in
            *"namespace=$2 "*) : ;;
            *) echo "warning: $1 was started as [$owner]," >&2
               echo "         but stop names namespace=$2" >&2 ;;
        esac
    fi
    if [ -n "${3:-}" ]; then
        case "$owner" in
            *"context=$3 "*) : ;;
            *) echo "warning: $1 was started as [$owner]," >&2
               echo "         but stop names context=$3" >&2 ;;
        esac
    fi
}

# capture_release <outfile> -- drop OUR record, and only ours.
#
# A caller that lost the claim has no business unlinking anything: one did, on its
# way out of the data guard, and the winner was left running with no record, so
# stop could not find it and the samples file was never terminated. The record is
# removed only when it names this process or the capture it adopted.
capture_release() {
    local owner pid
    [ -n "${1:-}" ] || return 0
    owner="$(cat "$(capture_owner_file "$1")" 2>/dev/null)" || return 0
    pid="$(printf '%s' "$owner" | sed -n 's/.*[^a-z]pid=\([0-9][0-9]*\).*/\1/p')"
    if [ -z "$pid" ] || [ "$pid" = "$$" ] || [ "$pid" = "${CAPTURE_ADOPTED_PID:-}" ]; then
        rm -f "$(capture_owner_file "$1")" 2>/dev/null || true
    fi
    return 0
}

# capture_finish <outfile> -- drop the record when ENDING a capture.
#
# The record has already been renamed to this stop's own `.finalising.$$` by
# capture_claim_finalise, so ending the capture means dropping that. Removing a
# FIXED path here is what let a losing start delete the winner's record and leave
# a live capture nothing could find.
capture_finish() {
    capture_finalised "$1"
}

# capture_stop <outfile> <pidfile> -- stop the capture and everything it spawned.
#
# The capture is started under job control (`set -m` at the fork), so it leads its
# own process group and one signal to the negated pgid reaches every descendant. `pkill -P` reached
# children only: the sampler forks a subshell which runs kubectl and python in a
# command substitution, so those are grandchildren, and they survived a stop --
# which is why a clean stop used to print BrokenPipeError AFTER its success line.
#
# Only signals a group whose recorded owner still matches, so a reused pid is
# never killed. An unrelated `sleep 600` whose pid landed in the file was killed
# by the previous version.
capture_stop() {
    local out="$1" pidfile="$2" pid
    if ! capture_alive "$out"; then
        # Nothing of ours is running. Clear the pidfile so a later stop does not
        # signal a pid that has since been reused by something else.
        rm -f "$pidfile" 2>/dev/null || true
        return 0
    fi
    pid="$(cat "$pidfile" 2>/dev/null)"
    case "${pid:-}" in
        ''|*[!0-9]*) rm -f "$pidfile" 2>/dev/null || true; return 0 ;;
    esac
    # The group first, then the leader, so a child cannot outlive the signal.
    kill -TERM "-$pid" 2>/dev/null || kill -TERM "$pid" 2>/dev/null || true
    # And confirm it. Reporting a stop that did not happen is worse than failing:
    # the caller then writes the JSON terminator and drops the record, leaving a
    # live capture invisible to the tooling and still appending to a finished
    # file. The natural trigger is a kill that cannot be delivered -- a capture
    # started by another user -- which the `|| true` above swallows.
    local waited=0
    while [ "$waited" -lt 50 ]; do
        capture_alive "$out" || { rm -f "$pidfile" 2>/dev/null || true; return 0; }
        sleep 0.1
        waited=$((waited + 1))
    done
    echo "the capture at $out did not stop (pid $pid still matches its record)" >&2
    echo "  leaving its record and pidfile in place rather than losing track of it" >&2
    return 1
}

# capture_claim_finalise <outfile> -- take the right to finalise, by taking the
# ownership record itself.
#
#   0  ours: the record is now at <outfile>.finalising.$$
#   2  another stop has it, or has already finished it -- concede
#   1  the path carries nothing we can take
#
# `mv` is the claim. Exactly one caller can rename a file that exists once, so two
# stops cannot both proceed and there is no test-then-create to make atomic. The
# winner's pid is in the NAME, so liveness needs no file read and no second
# starttime field beside the one the record already carries. A stop that died
# holding it leaves `.finalising.<deadpid>`, and taking that over is another `mv`,
# so the take-over cannot race either.
#
# Three earlier versions of this were a second record kept beside the first -- a
# mkdir mutex, a hardlink, an exclusive create -- and each one could disagree with
# the record it shadowed. Two stops finalised (2 of 25 produced `]}]}]}`), a loser
# reported "no other stop holds it" while one did (19 of 20), and a reused pid
# wedged a path for ever. One record, moved, has none of those states.
capture_claim_finalise() {
    local out="$1" of mine victim vp
    of="$(capture_owner_file "$out")"
    mine="$out.finalising.$$"
    if mv "$of" "$mine" 2>/dev/null; then
        return 0
    fi
    for victim in "$out".finalising.*; do
        [ -e "$victim" ] || continue
        vp="${victim##*.}"
        case "$vp" in
            ''|*[!0-9]*) continue ;;
        esac
        if kill -0 "$vp" 2>/dev/null; then
            return 2
        fi
        # Its holder is gone. One `mv` decides who inherits it.
        if mv "$victim" "$mine" 2>/dev/null; then
            return 0
        fi
    done
    return 1
}

# capture_finalised <outfile> -- release what THIS stop is holding.
#
# Keyed to our own pid, so a stop can only ever drop its own claim. A fixed name
# let a losing start delete the winner's record and leave a capture nothing could
# find.
capture_finalised() {
    [ -n "${1:-}" ] || return 0
    rm -f "$1.finalising.$$" 2>/dev/null || true
}

# capture_resolve_outfile <a> <b> -- which of two arguments is the output path.
#
# The two stop signatures were mirror images -- `stop <outfile> [<ns>]` in one
# script and `stop <ns> <outfile>` in the other -- and getting them backwards
# exited 0, printed "stopped", and left the capture running. Recognise the path
# rather than trusting the position: it is the argument with an ownership record,
# or failing that the one that looks like a path.
# Sets CAPTURE_OUT and CAPTURE_NS rather than returning them in one string: the
# result used to be "<out>|<ns>", so an output path containing a `|` was split in
# the middle and stop reported success against a truncated path while the capture
# kept running.
capture_resolve_outfile() {
    local a="${1:-}" b="${2:-}"
    CAPTURE_OUT="$a"; CAPTURE_NS="$b"
    if [ -n "$a" ] && capture_owned "$a"; then return 0; fi
    if [ -n "$b" ] && capture_owned "$b"; then CAPTURE_OUT="$b"; CAPTURE_NS="$a"; return 0; fi
    case "$a" in */*) return 0 ;; esac
    case "$b" in */*) CAPTURE_OUT="$b"; CAPTURE_NS="$a"; return 0 ;; esac
    return 0
}
