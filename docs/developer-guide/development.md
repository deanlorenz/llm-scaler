# Developer Guide

Guide for developers contributing to the scaling manager.

## Development Environment Setup

### Prerequisites

- Go 1.25.0+
- Docker 17.03+
- kubectl 1.32.0+
- Kind (for local testing)
- Make

### Initial Setup

1. **Clone the repository:**

   ```bash
   git clone https://github.com/ev-shindin/llm-scaling-manager.git
   cd llm-scaling-manager
   ```

2. **Install dependencies:**

   ```bash
   go mod download
   ```

3. **Install development tools:**

   ```bash
   make setup-envtest
   make controller-gen
   make kustomize
   ```

## Project Structure

Top level: `cmd/` (the manager, and `warmpool-proxy/`), `config/` (kustomize
bases, overlays and samples), `deploy/` (install scripts, `kind-emulator/`),
`hack/` (check and render scripts), `test/e2e/` (the e2e suite), `docs/`.

The packages under `internal/`, in the order one scaling decision flows through
them. The [signal dataflow](signal-dataflow.md#the-chain) draws the same chain
with the types that cross each boundary.

| Package | What it does |
|---------|--------------|
| `collector/` | Scrapes engine and EPP metrics (Prometheus or the pods), and attributes each replica to its variant. |
| `signals/` | Stateless arithmetic over those readings: `shape`, `itl`, `capacity`, `fleet`, `floor`. |
| `engines/analyzers/saturation`, `throughput`, `external` | Turn readings into demand and per-replica capacity. Never read a target. |
| `engines/aggregation/` | Supply, from the same per-replica capacity. |
| `engines/allocation/` | The optimizer and the GPU limiters (quota, inventory, Kueue). |
| `engines/steadystate/` | The per-cycle driver: runs the analyzers, applies thresholds, calls the optimizer, records the decision. |
| `actuator/` | Emits the decision as metrics for the scale target. |
| `decision/` | Holds the latest decision per target in memory. |
| `scaler/` | KEDA's ExternalScaler gRPC service: serves the decision to KEDA. |
| `engines/scalefromzero/` | Wakes a model parked at zero replicas from the EPP queue. |
| `warmpool/` | Runs the warm pool: policy, pool Pods, the proxy. |
| `kueue/` | Reads GPU quotas from Kueue. |
| `registry/` | The managed workloads, registered by KEDA calling the scaler. |
| `controller/` | Reconcilers for the ConfigMaps and InferencePools. |
| `config/` | Configuration types, loading and validation. |

Shared types live in `domain/`; the rest (`metrics/`, `logging/`, `utils/`,
`accelerator/`, ...) are support packages.

## Development Workflow

### Running Locally

#### Option 1: Outside the cluster

```bash
# Run the controller on your machine (connects to configured cluster)
make run
```

A controller run this way gets no KEDA calls, so it manages nothing until a
ScaledObject points at it. See [Debugging](debugging.md).

#### Option 2: In a Kind cluster

**One-shot — create cluster and deploy the scaling manager + EPP + monitoring:**

```bash
CREATE_CLUSTER=true make deploy-e2e-infra
```

This creates a Kind cluster with emulated GPUs, then deploys the scaling manager controller, llm-d EPP (GAIE standalone), Prometheus stack, and KEDA (external metrics). No model service is included.

If you already have a cluster running, omit `CREATE_CLUSTER=true`:

```bash
make deploy-e2e-infra
```

**Step 2 (optional) — Deploy a simulator model service:**

The simulator samples are under `config/samples/simulator/` and support three configurations:

```bash
# Decode only
kubectl apply -k config/samples/simulator/nodeSelector/decode/

# Prefill only
kubectl apply -k config/samples/simulator/nodeSelector/prefill/

# Both prefill and decode (disaggregated serving)
kubectl apply -k config/samples/simulator/nodeSelector/disaggregated/
```

Each configuration creates a Deployment (using `llm-d-inference-sim:v0.9.0`), a Service, a ServiceMonitor, and a KEDA `ScaledObject` in the `llm-d-sim` namespace.

The `ScaledObject` is what makes the workload the scaling manager's to manage: The scaling manager has no watch and no listing, and learns a variant exists from the KEDA call its trigger causes. Its `modelID` must match what the container serves (`--model test-model` here) — that is the grouping key for every multi-variant decision. KEDA creates the HPA that actuates; nothing here writes one by hand.

Confirm the chain is live:

```bash
kubectl -n llm-d-sim get scaledobject,hpa
kubectl -n workload-variant-autoscaler-system logs deploy/wva-controller-manager | grep dev-model-decode
```

To clean up the simulator, use the same path you applied:

```bash
kubectl delete -k config/samples/simulator/nodeSelector/decode/
# or
kubectl delete -k config/samples/simulator/nodeSelector/disaggregated/
```

To tear down the cluster entirely:

```bash
make destroy-kind-cluster
```

### Making Changes

1. **Create a feature branch:**

   ```bash
   git checkout -b feature/my-feature
   ```

2. **Make your changes**

3. **Regenerate if needed** (after changing kubebuilder RBAC markers or the
   structs in `internal/variant`):

   ```bash
   make manifests generate
   ```

4. **Run unit tests:**

   ```bash
   make test
   ```

5. **Run linter:**

   ```bash
   make lint
   ```

## Building and Testing

### Build the Binary

```bash
make build
```

The binary will be in `bin/manager`.

### Build Docker Image

```bash
make docker-build IMG=<your-registry>/wva-controller:tag
```

### Push Docker Image

```bash
make docker-push IMG=<your-registry>/wva-controller:tag
```

### Multi-architecture Build

```bash
PLATFORMS=linux/arm64,linux/amd64 make docker-buildx IMG=<your-registry>/wva-controller:tag
```

## Testing

### Unit Tests

```bash
# Run all unit tests
make test

# Run specific package tests
go test ./internal/controller/...

# With coverage -- never `go test ./...`: that also runs test/e2e against
# your current kube context
go test -cover $(go list ./... | grep -v -e /e2e -e /benchmark)
```

### E2E Tests

The scaling manager has a single consolidated E2E suite (`test/e2e/`) that runs on Kind (emulated) or OpenShift/kubernetes. Deploy infrastructure in infra-only mode first, then run tests.

**Location**: `test/e2e/`

```bash
# Smoke tests (Kind, ~5-10 min)
make test-e2e-smoke

# Full suite (Kind)
make test-e2e-full

# OpenShift: set KUBECONFIG and ENVIRONMENT=openshift first
export ENVIRONMENT=openshift
make test-e2e-smoke
# or make test-e2e-full

# Run specific tests
FOCUS="Basic Autoscaling" make test-e2e-smoke
```

A `FOCUS` that matches nothing runs 0 specs and still exits 0. Check the
`Ran N of M Specs` line, and that N is not 0.

See [Testing Guide](testing.md) and [E2E Test Suite README](../../test/e2e/) for infra-only setup and configuration. For OpenShift, set `ENVIRONMENT=openshift` and use the same targets.

### Manual Testing

1. **Deploy infrastructure to Kind cluster:**

   ```bash
   CREATE_CLUSTER=true make deploy-e2e-infra
   ```

2. **Deploy simulator model service:**

   ```bash
   kubectl apply -k config/samples/simulator/nodeSelector/disaggregated/
   ```

3. **Monitor controller logs:**

   ```bash
   kubectl logs -n workload-variant-autoscaler-system deploy/wva-controller-manager -f
   ```

## Debugging

### VSCode Launch Configuration

Create `.vscode/launch.json`:

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Debug Controller",
      "type": "go",
      "request": "launch",
      "mode": "auto",
      "program": "${workspaceFolder}/cmd/main.go",
      "env": {
        "KUBECONFIG": "${env:HOME}/.kube/config"
      },
      "args": []
    }
  ]
}
```

### Debugging in Cluster

```bash
# Build debug image
go build -gcflags="all=-N -l" -o bin/manager cmd/main.go

# Deploy and attach debugger (e.g., Delve)
```

### Viewing Controller Logs

```bash
kubectl logs -n workload-variant-autoscaler-system deploy/wva-controller-manager --tail=100 -f
```

## Common Development Tasks

### Adding a New Metric

1. Define metric in `internal/metrics/metrics.go`
2. Emit metric from appropriate controller location
3. Update Prometheus integration docs
4. Add to Grafana dashboards (if applicable)

### Modifying Optimization Logic

1. Update code in `internal/engines/allocation/` or `internal/engines/analyzers/`
2. Add/update unit tests
3. Run `make test`
4. Update design documentation if algorithm changes

## Documentation

Where a page goes is set by [AGENTS.md](../../AGENTS.md#documentation). Three
conventions that CI enforces:

- **Guides are generated.** The bash blocks in `docs/guides/<task>/README.md`
  come from its `guide.yaml`. Edit the YAML, run `make guides-render`, and
  `make guides-check` (which also checks every doc link).
- **Config file names** under `config/` follow `(<app>-)?<kind>.yaml`, e.g.
  `manager-clusterrole.yaml`.
- **Shell checks are fixtures.** A new check is a `hack/check-*.sh` wired into
  `make lint-deploy-scripts`; a check behind a new target is one nobody runs.

## Release Process

See the [Release Process](release-process.md) guide for how to cut a release. It covers:

- Pre-release checklist (changelog, optional version bumps, upstream pins)
- Creating the tag and GitHub Release (which triggers the image build)
- What runs automatically: Docker image build and push to GHCR
- Enabling other team members to perform releases (permissions, secrets, documentation)

## Getting Help

- Check [CONTRIBUTING.md](../../CONTRIBUTING.md)
- Review existing code and tests
- Search or open [GitHub Issues](https://github.com/ev-shindin/llm-scaling-manager/issues)

## Useful Commands

```bash
# Format code
make fmt

# Vet code
make vet

# Run linter
make lint

# Fix linting issues
make lint-fix

# Clean build artifacts
rm -rf bin/ dist/

# Reset Kind cluster
make destroy-kind-cluster
make create-kind-cluster
```

## Next Steps

- Review [Code Style Guidelines](../../CONTRIBUTING.md#code-guidelines)
- Check out [Good First Issues](https://github.com/ev-shindin/llm-scaling-manager/labels/good%20first%20issue)

---

## Known Setup Issues

### InferencePool CRD not found during `make deploy-e2e-infra`

**Symptom:**
```text
Error: no matches for kind "InferencePool" in version "inference.networking.x-k8s.io/v1alpha2"
ensure CRDs are installed first
```

**Cause:** The Gateway API Inference Extension CRDs are applied by `deploy/install-epp.sh` but
the Kubernetes API server may not have finished registering them before the next `kubectl apply`
runs.

**Fix:** Re-run `make deploy-e2e-infra` — it is idempotent and the CRDs will be registered by
the second run. If the error persists, wait a few seconds and retry:

```bash
make deploy-e2e-infra
```
