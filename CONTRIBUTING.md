# Contributing to the scaling manager

Welcome! We're glad you're interested in contributing to llm-scaling-manager.

This project is a fork of [llm-d's workload-variant-autoscaler](https://github.com/llm-d/llm-d-workload-variant-autoscaler),
and much of its design and code comes from that project. Issues and pull
requests for this fork go to
[github.com/ev-shindin/llm-scaling-manager](https://github.com/ev-shindin/llm-scaling-manager).

## Setting Up

### Prerequisites

- Go 1.25.0+
- Docker 17.03+
- kubectl 1.31.0+ (or `oc` for OpenShift)
- Kind (for local development)
- Basic understanding of Kubernetes controllers and operators

### Setting Up Your Development Environment

1. **Fork and clone the repository:**

   ```bash
   git clone https://github.com/<your-username>/llm-scaling-manager.git
   cd llm-scaling-manager
   ```

2. **Add the upstream remote** (this project's repository, not llm-d's):

   ```bash
   git remote add upstream https://github.com/ev-shindin/llm-scaling-manager.git
   ```

3. **Install dependencies:**

   ```bash
   go mod download
   ```

4. **Set up a local Kind cluster with emulated GPUs:**

   ```bash
   CREATE_CLUSTER=true make deploy-e2e-infra
   ```

5. **Run tests to verify setup:**

   ```bash
   make test
   ```

See the [Developer Guide](docs/developer-guide/development.md) for detailed
setup, and its [package map](docs/developer-guide/development.md#project-structure)
for where the code lives.

## Commits

Every commit needs two things, and CI checks the second:

- **A DCO sign-off.** Commit with `git commit -s`, which adds a
  `Signed-off-by:` line with your git name and email. Do not type the line by
  hand; it must match the commit's author.
- **A signature.** The `signed-commits` check
  (`.github/workflows/ci-signed-commits.yaml`) fails a PR with any commit that
  GitHub cannot verify. Set up [GPG or SSH commit signing](https://docs.github.com/en/authentication/managing-commit-signature-verification)
  and commit with `git commit -S -s` (or set `commit.gpgsign=true`).

Commit messages follow [conventional commits](https://www.conventionalcommits.org/).

## Testing Your Changes

**Run unit tests:**

```bash
make test
```

**Run E2E tests (Kind or OpenShift):**

```bash
# Smoke tests (Kind)
make test-e2e-smoke

# Full suite (Kind)
make test-e2e-full

# OpenShift: set KUBECONFIG and ENVIRONMENT, then run
export ENVIRONMENT=openshift
make test-e2e-smoke
# or make test-e2e-full

# Run specific tests
FOCUS="Basic Autoscaling" make test-e2e-smoke
```

A `FOCUS` that matches no spec runs 0 specs and still passes. Read the
`Ran N of M Specs` line in the verbose output and check N is not 0.

**Run linter:**

```bash
make lint

# Auto-fix linting issues
make lint-fix
```

What CI runs, and how to run each job locally, is in
[Testing: Continuous Integration](docs/developer-guide/testing.md#continuous-integration).

### Modifying generated manifests

This project installs no CustomResourceDefinition. A workload is registered by
its KEDA ScaledObject, so there is no API type to version. The only generated
file is the manager's ClusterRole:

1. **Regenerate after changing `+kubebuilder:rbac` markers:**

   ```bash
   make manifests
   ```

   This rewrites only `config/base/rbac/manager-clusterrole.yaml`.

2. **Check what moved:**

   ```bash
   git diff config/base/rbac
   ```

The DeepCopy methods in `internal/variant/deepcopy.go` are hand-maintained. No
type carries a `+kubebuilder:object` marker, so `make generate` produces
nothing; when you change a struct in `internal/variant`, update `deepcopy.go` by
hand.

### Building and Deploying

**Build the controller binary:**

```bash
make build
```

**Run controller locally (connects to configured cluster):**

```bash
make run
```

**Build Docker image:**

```bash
make docker-build IMG=<your-registry>/wva-controller:tag
```

**Deploy to cluster:**

```bash
make deploy-wva-on-k8s IMG=<your-registry>/wva-controller:tag
```

For OpenShift, use

```bash
make deploy-wva-on-openshift IMG=<your-registry>/wva-controller:tag
```

**Deploy with llm-d for testing:**

```bash
make deploy-e2e-infra IMG=<your-registry>/wva-controller:tag
```

## Documentation

Where a page goes is set by [AGENTS.md](AGENTS.md#documentation). Three
conventions that CI enforces:

- **Guides are generated.** The bash blocks in `docs/guides/<task>/README.md`
  come from its `guide.yaml`. Edit the YAML, run `make guides-render`, and
  `make guides-check` (which also checks every doc link).
- **Config file names** under `config/` follow `(<app>-)?<kind>.yaml`, e.g.
  `manager-clusterrole.yaml`.
- **Shell checks are fixtures.** A new check is a `hack/check-*.sh` wired into
  `make lint-deploy-scripts`.

## Code Guidelines

### Controller Development

- Log through `ctrl.Log` (see [AGENTS.md](AGENTS.md)); verbosity levels are in `internal/logging`
- Always use backoff retries for Kubernetes API calls (see `GetResourceWithBackoff` in `internal/resources`)
- Emit metrics for observability

### Analyzers

When modifying an analyzer in `internal/engines/analyzers/`:

- Ensure mathematical correctness
- Add comprehensive unit tests
- Validate against real workload data when possible
- Document assumptions and limitations

### Optimization Algorithms

When modifying optimizers in `internal/engines/allocation/`:

- Consider computational complexity
- Test edge cases (zero load, overload, etc.)
- Ensure feasibility checking
- Document algorithm choices

## Getting Help

- Check the [Developer Guide](docs/developer-guide/development.md)
- Review existing code and tests
- Search [GitHub Issues](https://github.com/ev-shindin/llm-scaling-manager/issues)

## Common Development Tasks

### Running the Controller Locally

```bash
# Option 1: Run outside cluster (connects to KUBECONFIG cluster)
make run

# Option 2: Deploy to Kind cluster
CREATE_CLUSTER=true make deploy-e2e-infra
```

### Debugging

```bash
# View controller logs
kubectl logs -n $NAMESPACE deploy/wva-controller-manager -f

# Check what KEDA was told, and by whom
kubectl describe scaledobject <name> -n <namespace>

# Check the HPA KEDA drives from the decision
kubectl describe hpa -n <namespace> keda-hpa-<scaledobject-name>
```

### Cleaning Up

```bash
# Destroy Kind cluster
make destroy-kind-cluster

# Undeploy from cluster (pass the ENVIRONMENT, SCOPE and namespace you installed with)
make undeploy-wva
```

## Pre-Submission Checklist

Before submitting your PR, ensure:

- [ ] `make test` passes
- [ ] `make lint` passes (fix issues with `make lint-fix`)
- [ ] `make test-e2e-smoke` passes (if controller logic changed; use `make test-e2e-full` for full suite)
- [ ] Documentation updated (if user-facing changes)
- [ ] Every commit is signed off (`-s`) and signed
- [ ] Commit messages follow [conventional commits](https://www.conventionalcommits.org/)
- [ ] PR description clearly explains the change

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0.

---

Thank you for contributing!
