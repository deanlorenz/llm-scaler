"""
Patch: process_epp_logs.py — handle numeric (epoch float) timestamps.

EPP logs carry timestamps as JSON numbers (epoch seconds), not ISO strings.
The upstream code passes them to re.sub() which raises TypeError, causing the
entire log file to be silently dropped.

Idempotent: checks for MARK before applying.
"""
import io
import sys

path = sys.argv[1]
src = io.open(path, encoding="utf-8").read()

MARK = "# wva-patch: numeric ts"
if MARK in src:
    print("fix1 (EPP float ts): already applied")
    sys.exit(0)

IMPORT_OLD = "from datetime import datetime\n"
IMPORT_NEW = "from datetime import datetime, timezone\n"
ANCHOR = "    # Handle nanosecond timestamps by truncating to 6 decimal places\n"
BRANCH = (
    "    " + MARK + ": EPP logs carry epoch seconds as a JSON number, not an ISO\n"
    "    # string. re.sub() then raises TypeError on the first entry and the whole\n"
    "    # log is dropped.\n"
    "    if isinstance(ts_str, (int, float)) and not isinstance(ts_str, bool):\n"
    "        return datetime.fromtimestamp(ts_str, timezone.utc).replace(tzinfo=None)\n"
)

if IMPORT_OLD not in src:
    sys.exit("fix1: anchor missing: %r" % IMPORT_OLD)
if ANCHOR not in src:
    sys.exit("fix1: anchor missing: %r" % ANCHOR)

src = src.replace(IMPORT_OLD, IMPORT_NEW, 1)
src = src.replace(ANCHOR, BRANCH + ANCHOR, 1)
io.open(path, "w", encoding="utf-8", newline="\n").write(src)
print("fix1 (EPP float ts): applied")
