# Release Process

This guide describes how to cut a release of the llm-scaling-manager. It covers the tag and the image. Nothing outside this repository needs updating: the llm-d workload-autoscaling guide documents the upstream workload-variant-autoscaler this project forked from, not this fork.

## Quick reference

| Step | Where | What |
|------|--------|------|
| 1. Pre-release | This repo | Changelog, optional version bumps (see below) |
| 2. Tag & release | GitHub | Create tag `vX.Y.Z`, push; create GitHub Release (publish) |
| 3. Automation | GitHub Actions | Image build + push |

---

## Scope of a release

A full release typically involves:

1. **This repo (the scaling manager)**  
   - A version tag (e.g. `v0.5.2`).  
   - A multi-arch container image built and pushed to `ghcr.io/<owner>/<repo>:<tag>` (and `:latest`); for this repository, `ghcr.io/ev-shindin/llm-scaling-manager:<tag>`.  

2. **Documentation in this repo**  
   - Changelog and, if needed, upstream version pins (`LLM_D_ROUTER_VERSION`, `GAIE_VERSION` in the [Makefile](../../Makefile)).

---

## Prerequisites

- **Permissions:** Ability to push tags and create/publish GitHub Releases.
- **Secrets:** a `GHCR_TOKEN` repository secret with `write:packages` on the target package. The workflow logs in to `ghcr.io` as `github.actor` with it; there is no separate user secret.
- **State of main:** Release from a clean, tested state (e.g. main or a release branch). Run tests locally or rely on CI before tagging.

---

## Pre-release checklist (this repo)

Do these on the branch you intend to tag (e.g. `main`).

1. **Changelog**  
   - Add a release-specific changelog under `docs/` (e.g. `docs/CHANGELOG-v0.5.2.md`) summarizing user-facing and notable changes.  
   - Optionally reference it in release notes when you create the GitHub Release.

2. **Kustomize default image tag (required)**  
   - On the commit you tag, set `newTag` in `config/base/manager/kustomization.yaml` to the release tag (e.g. `v0.5.2`) and commit. The release workflow checks this before it builds anything and fails if `newTag` does not equal the tag, because otherwise the release's own manifests deploy the floating `:main` rather than the image being released.  
   - After the tag is pushed, set `newTag` back to `main` on `main`, so development installs keep tracking the latest build. The pinned value lives on in the tag.  
   - Installing a release then needs no `IMG`: `git checkout vX.Y.Z && make deploy-wva` deploys `:vX.Y.Z`, and the installer pulls it `IfNotPresent` because a release tag does not move. Any other build is pinned with `IMG=<repo>@sha256:<digest>`; the installer prints a warning whenever it deploys a floating tag (`main`, `latest`, `dev`, `nightly`).  

3. **Upstream dependency pins**  
   - If this release pins a new version of an upstream dependency (e.g. [llm-d-inference-sim](https://github.com/llm-d/llm-d-inference-sim)), update the version pins in the [Makefile](../../Makefile) and the referenced files (e.g. `test/e2e/fixtures/model_service_builder.go`, `test/utils/resources/llmdsim.go`) before releasing.

---

## Creating the release

### 1. Create and push the tag

Use semantic versioning (e.g. `v0.5.2`). Alternatively, you can **create the tag in step 2** when creating the GitHub Release (no need to run the commands below).

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

- Pushing a tag matching `v*` triggers [`.github/workflows/ci-release.yaml`](../../.github/workflows/ci-release.yaml), which builds the image for `linux/amd64` and `linux/arm64`, pushes it as `ghcr.io/<owner>/<repo>:<tag>` and `:latest`, and runs a Trivy scan on it. Publishing a GitHub Release runs the same workflow again.

> **Note:** the workflow is being fixed. Today `ci-release.yaml` hardcodes the
> registry path as `ghcr.io/llm-d/<repo>`, which this repository cannot push
> to. This page describes the release after that fix, when the path is derived
> from the repository owner. Until it lands, check the workflow's `IMAGE=` line
> before tagging.

### 2. Create the GitHub Release

- In the repo: **Releases → Create a new release**.
- Choose the tag you just pushed (e.g. `vX.Y.Z`), or **create the tag there** by typing a new tag name (e.g. `vX.Y.Z`); GitHub will create the tag when you publish.
- Add release notes (you can paste from your changelog or `docs/CHANGELOG-vX.Y.Z.md`).
- **Publish** the release.

---

## Enabling others to cut releases

To allow other team members to perform releases:

1. **Permissions**  
   - Grant them permission to push tags and to create/publish GitHub Releases.

2. **Secrets**  
   - Ensure the release workflow can read the `GHCR_TOKEN` secret (repository or organization secret). Document where it is set in internal runbooks.

3. **Documentation**  
   - Point them to this guide and to the workflows:
     - [`.github/workflows/ci-release.yaml`](../../.github/workflows/ci-release.yaml) — image build on tag push.

4. **Optional runbook**  
   - Maintain a short internal checklist (e.g. "pre-release checklist → tag → release") so the steps above are easy to follow without re-reading the full doc.

---

## Summary

| Item | Action |
|------|--------|
| **the scaling manager release** | Tag `vX.Y.Z` → push → create and publish GitHub Release. CI builds and pushes the image. |
| **Team** | Use this doc plus repo permissions and GHCR secrets so others can run the same process safely.

For workflow details, see [`.github/workflows/ci-release.yaml`](../../.github/workflows/ci-release.yaml).
