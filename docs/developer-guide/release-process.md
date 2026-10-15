# Release Process

How to cut a release of llm-scaling-manager: the tag and the image. Nothing outside this repository needs updating. The llm-d workload-autoscaling guide documents the upstream workload-variant-autoscaler this project forked from, not this fork.

## Quick reference

| Step | Where | What |
|------|--------|------|
| 1. Pre-release | This repo | Changelog, optional version bumps (see below) |
| 2. Tag & release | GitHub | Create tag `vX.Y.Z`, push; create GitHub Release (publish) |
| 3. Automation | GitHub Actions | Image build + push |

---

## Scope of a release

A full release involves:

1. **This repo (the scaling manager)**  
   - A version tag (e.g. `v0.5.2`).  
   - A multi-arch container image built and pushed to `ghcr.io/<owner>/<repo>:<tag>` (and `:latest`); for this repository, `ghcr.io/ev-shindin/llm-scaling-manager:<tag>`.  

2. **Documentation in this repo**  
   - Changelog and, if needed, upstream version pins (`LLM_D_ROUTER_VERSION`, `GAIE_VERSION` in the [Makefile](../../Makefile)).

---

## Prerequisites

- **Permissions:** push tags, and create and publish GitHub Releases.
- **Secrets:** a `GHCR_TOKEN` repository secret with `write:packages` on the target package. The workflow logs in to `ghcr.io` as `github.actor` with it; there is no separate user secret.
- **State of main:** release from a clean, tested commit (main or a release branch). Run the tests, or let CI pass, before tagging.

---

## Pre-release checklist (this repo)

Do these on the branch you intend to tag (e.g. `main`).

1. **Changelog**  
   - Add a release-specific changelog under `docs/` (e.g. `docs/CHANGELOG-v0.5.2.md`) summarizing user-facing and notable changes.  
   - Optionally reference it in release notes when you create the GitHub Release.

2. **Kustomize default image tag (required)**  
   - On the commit you tag, set `newTag` in `config/base/manager/kustomization.yaml` to the release tag (e.g. `v0.5.2`) and commit. The release workflow checks this before building and fails if `newTag` differs from the tag. Otherwise the release's own manifests would deploy the floating `:main`, not the released image.
   - After pushing the tag, set `newTag` back to `main` on `main`, so development installs track the latest build. The tag keeps the pinned value.
   - Installing a release then needs no `IMG`: `git checkout vX.Y.Z && make deploy-wva` deploys `:vX.Y.Z`, pulled `IfNotPresent` because a release tag does not move. Pin any other build with `IMG=<repo>@sha256:<digest>`. The installer warns whenever it deploys a floating tag (`main`, `latest`, `dev`, `nightly`).    

3. **Upstream dependency pins**  
   - If the release pins a new upstream version (e.g. [llm-d-inference-sim](https://github.com/llm-d/llm-d-inference-sim)), update the pins in the [Makefile](../../Makefile) and the files that reference them (e.g. `test/e2e/fixtures/model_service_builder.go`, `test/utils/resources/llmdsim.go`) first.

---

## Creating the release

### 1. Create and push the tag

Use semantic versioning (e.g. `v0.5.2`). Or **create the tag in step 2**, with the GitHub Release, and skip these commands.

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

- Pushing a tag matching `v*` triggers [`.github/workflows/ci-release.yaml`](../../.github/workflows/ci-release.yaml), which builds the image for `linux/amd64` and `linux/arm64`, pushes it as `ghcr.io/<owner>/<repo>:<tag>` and `:latest`, and runs a Trivy scan on it. Publishing a GitHub Release runs the same workflow again.

> **Note:** the registry path is derived, not hardcoded. `ci-release.yaml`
> builds `IMAGE=ghcr.io/${GITHUB_REPOSITORY_OWNER,,}/<repo>`, so a fork pushes
> to its own owner's `ghcr.io` namespace, which is where the kustomization and
> the installer pull from. The `GHCR_TOKEN` secret it logs in with must be able
> to write packages for that owner.

### 2. Create the GitHub Release

- In the repo: **Releases → Create a new release**.
- Choose the tag you pushed (e.g. `vX.Y.Z`), or type a new tag name to **create it there**; GitHub creates it when you publish.
- Add release notes (paste from `docs/CHANGELOG-vX.Y.Z.md`).
- **Publish** the release.

---

## Enabling others to cut releases

For another team member to release:

1. **Permissions:** let them push tags and create and publish GitHub Releases.
2. **Secrets:** make sure the release workflow can read `GHCR_TOKEN` (a repository or organization secret), and record where it is set in your internal runbooks.
3. **Documentation:** point them to this guide and to [`.github/workflows/ci-release.yaml`](../../.github/workflows/ci-release.yaml), the image build on tag push.
4. **Optional runbook:** keep a short internal checklist ("pre-release checklist → tag → release").

---

## Summary

| Item | Action |
|------|--------|
| **the scaling manager release** | Tag `vX.Y.Z` → push → create and publish GitHub Release. CI builds and pushes the image. |
| **Team** | Use this doc plus repo permissions and GHCR secrets so others can run the same process safely.

For workflow details, see [`.github/workflows/ci-release.yaml`](../../.github/workflows/ci-release.yaml).
