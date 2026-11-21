# CI, releases and Pages

GitHub Actions checks every change and publishes the container image and the GitHub Pages site.

## Decisions

- The default branch is `master`.
- **Image:** `ghcr.io/<owner>/wrench`, where `<owner>` is the repository owner in lower case (GHCR rejects upper
  case), `linux/amd64` and `linux/arm64`, pushed with `GITHUB_TOKEN`.
  - Push to `master`: tags `edge` and `sha-<7 hex>`.
  - Tag `vX.Y.Z`: tags `X.Y.Z`, `X.Y` and `latest`; a tag with a pre-release suffix (`vX.Y.Z-rc.1`) gets only
    `X.Y.Z-rc.1`.
  - Every pushed image gets a build provenance attestation pushed to the registry.
- **Release:** a pushed tag `v*` runs the full checks, then publishes the image and creates a **draft** GitHub
  Release with generated notes that name the image. A person publishes the draft. A pre-release tag makes a
  pre-release draft.
- **Pages:** the contents of `website/site/` are deployed as-is with `actions/deploy-pages`.
- Out of scope: signing with keys (cosign), SBOMs beyond what buildx attaches by default, other registries,
  deploying a server.

## Workflows

| File              | Triggers                                                 | Jobs                                                                                                                                      |
| ----------------- | -------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `ci.yml`          | pull requests, pushes to other branches, `workflow_call` | `checks` (`make lint-workflows`, `make ci`, `make vuln`), `image` (both platforms, no push), `e2e`, `opencode` (`make opencode-contract`) |
| `image.yml`       | push to `master`                                         | calls `ci.yml`, then builds and pushes `edge`/`sha-<7>` with attestation                                                                  |
| `release.yml`     | push of a tag `v*`                                       | calls `ci.yml`; `image` (version tags, attestation); `release` (draft)                                                                    |
| `pages.yml`       | push to `master` changing `website/**`, manual           | `deploy` with `actions/upload-pages-artifact` and `actions/deploy-pages`                                                                  |
| `image-build.yml` | `workflow_call` (`push`, `tags`)                         | the one image build used by the three workflows above, so they cannot drift; scans amd64 with Trivy before the multi-platform build       |

- `image.yml` and `release.yml` run after `ci.yml` passes, not in parallel with it.
- Concurrency: one run per ref for `image.yml` and `pages.yml` (a newer push cancels the older run); `release.yml`
  never cancels.
- Permissions: workflow default `contents: read`; `packages: write`, `id-token: write` and `attestations: write` only
  on jobs that push or attest; `contents: write` only on the job that creates the release; `pages: write` and
  `id-token: write` only on the Pages deploy job.
- Every third-party action is pinned to a full commit SHA with its version in a trailing comment.
- Security checks:
  - `make lint` runs gosec through golangci-lint; exclusions in `.golangci.yml` and `//nolint:gosec` carry their reason.
  - `make vuln` runs govulncheck and `npm audit --audit-level=high` (build tooling included).
  - `make image-scan` runs Trivy (pinned image digest) and fails on HIGH or CRITICAL findings, fixed upstream or not,, so no image
    is pushed with them. Each exception in `.trivyignore.yaml` states why it does not apply and expires.
- `.github/scripts/release-tag.sh` classifies the tag (version, image tags, pre-release); `make lint-workflows`
  tests it.

## Image build

- `docker/setup-qemu-action`, `docker/setup-buildx-action`, `docker/login-action` (GHCR, only when pushing),
  `docker/metadata-action` for tags and OCI labels, `docker/build-push-action` with `cache-from/to: type=gha`.
- Build args `VERSION` (the tag without `v`, or `git describe` on `master`) and `COMMIT` (short SHA).
- The Dockerfile cross-compiles on the build platform; only the `pgtools` stage runs under QEMU
  ([image](01-server-image.md)).

## Release notes

`gh release create "$TAG" --draft --verify-tag --generate-notes --title "Wrench X.Y.Z"`, with `--prerelease` for
a pre-release tag, and a notes header naming the image (`ghcr.io/<owner>/wrench:X.Y.Z`, linux/amd64 and
linux/arm64). The attestation is checked with
`gh attestation verify oci://ghcr.io/<owner>/wrench:X.Y.Z --repo <owner>/<repo>`.

## Verification

- `make lint-workflows` (part of `make lint`) runs `actionlint` through `go run` at a pinned version, checks the SHA
  pins and tests the release tag script.
- The workflows themselves are first exercised on GitHub after a push.
