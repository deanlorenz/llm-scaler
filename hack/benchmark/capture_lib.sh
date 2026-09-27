# Shared ownership and argument handling for the benchmark capture helpers.
#
# Sourced by sample_replicas.sh and tail_wva_logs.sh. It exists because this
# logic lived twice, once per script, and the two copies were already drifting --
# one grew an optional namespace on stop that the other did not, so the two stop
# signatures became mirror images and getting them backwards silently left a
# capture running against a shared cluster.
#
# The model is OWNERSHIP, not file size. An earlier attempt decided whether a
# path was free by asking whether the file was empty, which is wrong in both
# directions: a live capture that has not yet written a byte reads as free -- and
# tail_wva_logs truncates its output at start, so that window is every capture's
# first seconds -- while a crashed run's leftovers read as occupied forever.
#
# A capture CLAIMS its output with mkdir, which is atomic on every POSIX
# filesystem. Test-then-write is not, and two runs racing on one path is the case
# that produced invalid JSON and an orphaned sampler in 5 of 5 trials. The claim
# records the namespace, the context and the pid; a claim whose pid is gone is
# reclaimed rather than honoured forever.

CAPTURE_CTX=""
CAPTURE_FORCE=0
CAPTURE_POSITIONAL=()
CAPTURE_KUBECTL="${KUBECTL_CMD:-kubectl}"

# capture_parse_args "$@" -- pull the flags out from ANYWHERE in the argument
# list, leaving the positionals in CAPTURE_POSITIONAL.
#
# Position-independent on purpose. When flags were recognised only before the
# subcommand, `start <ns> <out> --context c` exited 0 with no context reaching
# kubectl: the capture watched whatever KUBECONFIG said while the operator
# believed they had named a cluster. That is the exact failure --context was
# added to prevent, produced by the natural word order.
#
# Every spelling that cannot be honoured is an error rather than a default.
# `--context=` in particular used to mean "inherited", and it is the one spelling
# a wrapper produces from an unset variable.
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
                CAPTURE_CTX="$2"
                shift 2
                ;;
            --context=*)
                CAPTURE_CTX="${1#--context=}"
                if [ -z "$CAPTURE_CTX" ]; then
                    echo "--context= needs a value" >&2
                    return 2
                fi
                shift
                ;;
            --force)
                CAPTURE_FORCE=1
                shift
                ;;
            --)
                shift
                while [ "$#" -gt 0 ]; do
                    CAPTURE_POSITIONAL+=("$1")
                    shift
                done
                ;;
            -?*)
                echo "unknown flag: $1" >&2
                return 2
                ;;
            *)
                CAPTURE_POSITIONAL+=("$1")
                shift
                ;;
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

# capture_claim <outfile> <namespace> -- take exclusive ownership, or fail.
#
# Exactly one of two concurrent callers can create a directory, which is why the
# claim is a mkdir. A claim held by a live process is refused; one whose pid is
# gone is a crashed run and is reclaimed with a note, so a machine does not
# accumulate paths nobody may use again.
capture_claim() {
    local out="$1" lock="$1.lock" holder="" hpid=""
    if mkdir "$lock" 2>/dev/null; then
        return 0
    fi
    [ -f "$lock/owner" ] && holder="$(cat "$lock/owner" 2>/dev/null)"
    hpid="$(printf '%s' "$holder" | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p')"
    if [ -n "$hpid" ] && kill -0 "$hpid" 2>/dev/null; then
        echo "refusing to start: a capture already owns $out" >&2
        echo "  holder: $holder" >&2
        echo "  stop that capture first, or choose another output path" >&2
        return 1
    fi
    echo "note: reclaiming a stale capture of $out (holder gone: ${holder:-unknown})" >&2
    return 0
}

# capture_guard_data <outfile> -- refuse to destroy measurements already on disk.
#
# Separate from the claim: the claim is about two captures colliding, this is
# about a capture eating a finished run. Only the second is excused by --force.
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
# writing nothing, with the Makefile's `|| true` hiding all of it. A status line
# has to report what the code measured.
capture_verify_writable() {
    local out="$1"
    mkdir -p "$(dirname "$out")" 2>/dev/null || true
    if ! : > "$out" 2>/dev/null; then
        echo "cannot write $out -- refusing to start a capture that collects nothing" >&2
        return 1
    fi
    return 0
}

capture_record_owner() {
    printf 'namespace=%s context=%s started=%s pid=%s cmd=%s\n' \
        "$2" "${CAPTURE_CTX:-<inherited KUBECONFIG>}" \
        "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$3" "$(basename "$0")" \
        > "$1.lock/owner"
}

capture_owner() {
    [ -f "$1.lock/owner" ] || return 1
    cat "$1.lock/owner" 2>/dev/null
}

# capture_owned <outfile> -- true when this path is a capture we may finalise.
#
# stop consults this before touching the file. Without it, stop appended a JSON
# terminator to whatever was at the path -- including a file start had just
# REFUSED to overwrite, turning a protected run into unparseable output two
# function calls after protecting it.
capture_owned() {
    [ -d "$1.lock" ]
}

# capture_check_owner <outfile> <namespace> <context> -- warn on a mismatch.
#
# BOTH fields are compared. Comparing the namespace alone made finalising a
# capture that belongs to a different cluster silent, and
# same-namespace-different-cluster is a case this library newly makes usable.
# An empty argument is "not stated", not a mismatch.
capture_check_owner() {
    local owner
    owner="$(capture_owner "$1")" || return 0
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

capture_release() {
    rm -rf "$1.lock" 2>/dev/null || true
}

# capture_kill_tree <pidfile> -- stop the capture and everything it spawned.
#
# By recorded pid and its children only. The previous version swept with
# `pkill -f "logs -n $NS -l <label>"`, a substring match that killed any other
# session capturing the same namespace -- including one on a different cluster,
# since the pattern carries no context. Measured: a stop for cluster B killed
# cluster A's tail, and because the file had also been truncated the survivor
# resumed from --since=1s and lost everything before that moment.
capture_kill_tree() {
    local pidfile="$1" pid
    [ -f "$pidfile" ] || return 0
    pid="$(cat "$pidfile" 2>/dev/null)"
    case "$pid" in
        ''|*[!0-9]*) rm -f "$pidfile"; return 0 ;;
    esac
    pkill -P "$pid" 2>/dev/null || true
    kill "$pid" 2>/dev/null || true
    rm -f "$pidfile"
}

# capture_resolve_outfile <a> <b> -- which of two arguments is the output path.
#
# The two stop signatures were mirror images: `stop <outfile> [<ns>]` in one
# script and `stop <ns> <outfile>` in the other. Getting them backwards exited 0,
# printed "stopped", wrote a junk file named after the namespace, and left the
# sampler running. Rather than break either caller, recognise the path: it is the
# argument that owns a claim, or failing that the one that looks like a path.
# Echoes "<outfile>|<namespace>".
capture_resolve_outfile() {
    local a="${1:-}" b="${2:-}"
    if [ -n "$a" ] && capture_owned "$a"; then
        printf '%s|%s\n' "$a" "$b"
        return 0
    fi
    if [ -n "$b" ] && capture_owned "$b"; then
        printf '%s|%s\n' "$b" "$a"
        return 0
    fi
    case "$a" in
        */*) printf '%s|%s\n' "$a" "$b"; return 0 ;;
    esac
    case "$b" in
        */*) printf '%s|%s\n' "$b" "$a"; return 0 ;;
    esac
    printf '%s|%s\n' "$a" "$b"
}
