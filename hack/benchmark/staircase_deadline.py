"""In-cluster deadline: delete the namespace's ScaledObjects, LeaderWorkerSets and Deployments
when the runner's lease lapses, or at the hard cap, whichever comes first.

The runner writes an epoch into ConfigMap staircase-lease (key until) every few minutes. If the
runner dies, the lease lapses and the GPUs are freed within minutes of it. A read that fails keeps
the last lease: the API being unreachable is not the runner dying. Runs as a ServiceAccount whose
Role is scoped to its own namespace. Uses the Kubernetes API directly, so the image only needs
python3.
"""
import json
import os
import ssl
import sys
import time
import urllib.request

SA = "/var/run/secrets/kubernetes.io/serviceaccount"
LEASE = "staircase-lease"
PATHS = ("/apis/keda.sh/v1alpha1/namespaces/%s/scaledobjects",
         "/apis/leaderworkerset.x-k8s.io/v1/namespaces/%s/leaderworkersets",
         "/apis/apps/v1/namespaces/%s/deployments")


def stamp(t=None):
    return time.strftime("%FT%TZ", time.gmtime(t))


def expired(now, until, hard):
    """Whether the run's time is up: the lease, or the cap the lease cannot extend."""
    return now >= min(until, hard)


def next_lease(read, last):
    """The lease to hold: what read returns, or last when the read fails."""
    try:
        return read()
    except Exception as e:  # noqa: BLE001 -- the API being down is not the runner dying
        print(stamp(), "lease read failed:", e, flush=True)
        return last


def delete_all(ns, delete):
    """Delete every path; keep going past a failure, to free what can be freed. Returns 1 on any."""
    rc = 0
    for path in PATHS:
        try:
            print(stamp(), "DELETE", path % ns, delete(path % ns), flush=True)
        except Exception as e:  # noqa: BLE001 -- see above
            print(stamp(), "DELETE", path % ns, "failed:", e, flush=True)
            rc = 1
    return rc


def main():
    hard = int(os.environ["DEADLINE_EPOCH"])
    with open(SA + "/namespace", encoding="utf8") as f:
        ns = f.read().strip()
    ctx = ssl.create_default_context(cafile=SA + "/ca.crt")
    host = "https://%s:%s" % (os.environ["KUBERNETES_SERVICE_HOST"], os.environ["KUBERNETES_SERVICE_PORT"])

    def token():
        with open(SA + "/token", encoding="utf8") as f:
            return f.read().strip()

    def read():
        req = urllib.request.Request(host + "/api/v1/namespaces/%s/configmaps/%s" % (ns, LEASE),
                                     headers={"Authorization": "Bearer " + token()})
        with urllib.request.urlopen(req, context=ctx, timeout=30) as r:
            return int(json.load(r)["data"]["until"])

    def delete(path):
        req = urllib.request.Request(host + path, method="DELETE", headers={
            "Authorization": "Bearer " + token(), "Content-Type": "application/json"},
            data=json.dumps({"kind": "DeleteOptions", "apiVersion": "v1", "propagationPolicy": "Background"}).encode())
        with urllib.request.urlopen(req, context=ctx, timeout=60) as r:
            return "%d items %d" % (r.status, len(json.load(r).get("items", [])))

    print(stamp(), "deadline at", stamp(hard), "or the lease, namespace", ns, flush=True)
    until = hard
    while True:
        new = next_lease(read, until)
        if new != until:
            print(stamp(), "lease until", stamp(new), flush=True)
        until = new
        if expired(time.time(), until, hard):
            break
        time.sleep(60)
    print(stamp(), "deadline reached: lease", until, "hard", hard, flush=True)
    sys.exit(delete_all(ns, delete))


if __name__ == "__main__":
    main()
