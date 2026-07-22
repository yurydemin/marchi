#!/usr/bin/env sh
# Builds the Docker image for both linux/amd64 and linux/arm64 via the
# repo-root Dockerfile, using the same version metadata goreleaser embeds
# into the release binaries. Not wired through goreleaser's own
# `dockers:` block — see the comment at the top of .goreleaser.yaml for
# why — so this is the other half of a release: `goreleaser release
# --clean` for the binaries/GitHub Release, this for the image.
#
# No --push (this project doesn't publish to a registry yet, see the
# Phase 4 plan) and no --load either: docker's local image store can't
# hold a multi-platform image under one tag without a registry behind
# it, so without either flag buildx just builds both platforms and
# reports success/failure — exactly the "verify it builds everywhere,
# don't publish anything" this script exists for. To actually run the
# result locally, load a single platform explicitly:
#   docker buildx build --platform linux/arm64 --load -t marchi:local .
set -eu

VERSION="${1:-$(git describe --tags --always --dirty)}"
COMMIT="$(git rev-parse --short HEAD)"
BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --build-arg "VERSION=${VERSION}" \
  --build-arg "COMMIT=${COMMIT}" \
  --build-arg "BUILD_DATE=${BUILD_DATE}" \
  -t "marchi:${VERSION}" \
  .
