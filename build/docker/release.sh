#!/usr/bin/env sh
# Builds the Docker image for both linux/amd64 and linux/arm64 via the
# repo-root Dockerfile, using the same version metadata goreleaser embeds
# into the release binaries. Not wired through goreleaser's own
# `dockers:` block — see the comment at the top of .goreleaser.yaml for
# why — so this is the other half of a release: `goreleaser release
# --clean` for the binaries/GitHub Release, this for the image.
#
# Local/default use (no PUSH env var) builds both platforms and reports
# success/failure without publishing anything — docker's local image
# store can't hold a multi-platform image under one tag without a
# registry behind it, so there's nothing meaningful to --load either. To
# actually run the result locally, load a single platform explicitly:
#   docker buildx build --platform linux/arm64 --load -t marchi:local .
#
# CI sets PUSH=1 (release.yml's docker job) to publish
# ghcr.io/yurydemin/marchi:${VERSION} and :latest instead — the only
# registry this project publishes to (see the Phase 5 plan).
set -eu

RAW_VERSION="${1:-$(git describe --tags --always --dirty)}"
# Strip a leading "v" (a git tag like "v0.2.0") so the embedded
# internal/version.Version and the ghcr.io tag both match goreleaser's
# own {{.Version}} convention, which is already v-stripped in the
# release archive filenames (marchi_0.2.0_linux_amd64.tar.gz, not
# marchi_v0.2.0_...) — the Docker image and the binary tarball should
# report the same version string for the same release.
VERSION="${RAW_VERSION#v}"
COMMIT="$(git rev-parse --short HEAD)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

TAGS="-t marchi:${VERSION}"
PUSH_FLAG=""
if [ "${PUSH:-}" = "1" ]; then
  TAGS="-t ghcr.io/yurydemin/marchi:${VERSION} -t ghcr.io/yurydemin/marchi:latest"
  PUSH_FLAG="--push"
fi

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg "VERSION=${VERSION}" \
  --build-arg "COMMIT=${COMMIT}" \
  --build-arg "BUILD_DATE=${BUILD_DATE}" \
  ${TAGS} \
  ${PUSH_FLAG} \
  .
