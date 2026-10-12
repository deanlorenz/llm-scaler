# Release Process

This guide describes how to cut a release of the llm-scaling-manager: what to
prepare in this repository, how to tag, and what the release workflow does with
the tag.

## Quick reference

| Step | Where | What |
|------|--------|------|
| 1. Pre-release | This repo | Changelog, the kustomize image pin, dependency pins (see below) |
| 2. Tag & release | GitHub | Push tag `vX.Y.Z` (or create it from **Releases**); publish the GitHub Release |
| 3. Automation | GitHub Actions | Lint + unit tests, scan, multi-arch build + push, SBOM, provenance, signature |
| 4. Post-release | This repo | Verify the signature; set `newTag` back to `main` on `main` |

---

## Scope of a release

1. **A version tag** in this repository, `vMAJOR.MINOR.PATCH` or
   `vMAJOR.MINOR.PATCH-PRERELEASE` (e.g. `v0.6.0`, `v0.6.0-rc1`).
2. **A container image** at `ghcr.io/<repository owner>/llm-scaling-manager:<tag>`,
   i.e. `ghcr.io/ev-shindin/llm-scaling-manager:<tag>` for this repository. The
   name is derived from the repository, exactly as
   [`ci-main-image.yaml`](../../.github/workflows/ci-main-image.yaml) derives the
   floating `:main` image, and matches `IMAGE_TAG_BASE` in the
   [Makefile](../../Makefile) and `config/base/manager/kustomization.yaml`.
3. **Release notes and documentation** in this repository.

---

## Prerequisites

- **Permissions:** ability to push tags and to create/publish GitHub Releases.
- **Registry credentials:** none to configure. The workflow pushes with the
  built-in `GITHUB_TOKEN` (`packages: write`); a `GHCR_TOKEN` secret, if one is
  set, is used instead.
- **State of the tagged commit:** the workflow runs the PR checks again on it
  (below), so a commit that does not pass them cannot be released. It does not
  run the kind e2e suites; tag a commit whose PR e2e runs were green.

---

## Pre-release checklist

Do these on the branch you intend to tag (normally `main`).

1. **Changelog.** Add `docs/CHANGELOG-vX.Y.Z.md` summarising user-facing and
   notable changes, and paste it into the GitHub Release notes.
2. **Pin the default install to the release (required).** On the commit you tag,
   set `newTag` in `config/base/manager/kustomization.yaml` to the release tag
   (e.g. `v0.6.0`). The workflow fails before it builds anything if `newTag` does
   not equal the tag: otherwise the release's own manifests deploy the floating
   `:main`, not the image being released. After the tag is pushed, set `newTag`
   back to `main` on `main`; the pinned value lives on in the tag.

   Installing a release then needs no `IMG`: `git checkout vX.Y.Z && make deploy-wva`
   deploys `:vX.Y.Z`, pulled `IfNotPresent` because a release tag does not move.
   Pin any other build with `IMG=<repo>@sha256:<digest>`; the installer warns
   whenever it deploys a floating tag (`main`, `latest`, `dev`, `nightly`).
3. **Dependency pins.** If the release moves an upstream dependency (for example
   [llm-d-inference-sim](https://github.com/llm-d/llm-d-inference-sim)), update
   its pin in the [Makefile](../../Makefile) and the files that reference it
   (`test/e2e/fixtures/model_service_builder.go`, `test/utils/resources/llmdsim.go`).

---

## Creating the release

### 1. Push the tag

```bash
git tag vX.Y.Z
git push origin vX.Y.Z
```

Alternatively create the tag from **Releases → Draft a new release** by typing a
new tag name; publishing creates the tag, which starts the same workflow.

### 2. What the workflow does

[`.github/workflows/ci-release.yaml`](../../.github/workflows/ci-release.yaml) runs
once per tag push (it no longer also runs on `release: published`, which built and
pushed every release twice):

1. **Gate.** Calls [`ci-pr-checks.yaml`](../../.github/workflows/ci-pr-checks.yaml)
   on the tagged commit: `golangci-lint`, `make build`, `make test`,
   `make lint-deploy-scripts`, the docs checks and the kustomize builds. Nothing
   is built for release unless they pass.
2. **Validate the tag and the pin.** Anything other than
   `vMAJOR.MINOR.PATCH[-PRERELEASE]` is refused, and so is a tagged commit whose
   kustomization `newTag` is not the tag.
3. **Scan before push.** Builds the `linux/amd64` image into the runner's Docker
   daemon only and scans it with Trivy. A **fixable CRITICAL** vulnerability fails
   the release (`ignore-unfixed: true`: a CRITICAL with no upstream fix cannot be
   cured by rebuilding, so it is reported but does not block). A second,
   non-blocking scan prints every HIGH and CRITICAL finding.
4. **Build and push once.** One `linux/amd64,linux/arm64` buildx build, pushed with
   an SBOM (`sbom: true`) and a max-mode SLSA provenance attestation
   (`provenance: mode=max`).
5. **Tags.** Always `:<version>`. `:latest` moves only for a final release; a tag
   with a prerelease suffix (`v0.6.0-rc1`) never touches `latest`.
6. **Sign.** Keyless `cosign sign` of the pushed digest, with the workflow's OIDC
   identity as the signer. The run summary prints the digest and the verify
   command.

### 3. Publish the GitHub Release

In **Releases**, choose the tag, paste the changelog, mark it as a pre-release if
the tag has a prerelease suffix, and publish.

---

## Verifying a release

```bash
IMAGE=ghcr.io/ev-shindin/llm-scaling-manager
cosign verify "$IMAGE:vX.Y.Z" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/ev-shindin/llm-scaling-manager/.github/workflows/ci-release.yaml@refs/tags/vX.Y.Z"

# SBOM and provenance attached to the index
docker buildx imagetools inspect "$IMAGE:vX.Y.Z" --format '{{ json .SBOM }}'
docker buildx imagetools inspect "$IMAGE:vX.Y.Z" --format '{{ json .Provenance }}'
```

Deploy by digest (`IMG=$IMAGE@sha256:...`) when you need the exact bytes that
were verified; a tag can be moved after the fact, a digest cannot.

---

## Enabling others to cut releases

Grant them permission to push tags and to publish Releases. No registry secret
needs sharing: the workflow uses the built-in token. Point them at this page and
at [`ci-release.yaml`](../../.github/workflows/ci-release.yaml).
