# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

# CGO_ENABLED=0 works because modernc.org/sqlite (this project's only
# SQLite driver) is pure Go — no libsqlite3/gcc needed, and the result
# is a static binary distroless's non-glibc runtime can actually execute.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags "-s -w \
      -X github.com/yurydemin/marchi/internal/version.Version=${VERSION} \
      -X github.com/yurydemin/marchi/internal/version.Commit=${COMMIT} \
      -X github.com/yurydemin/marchi/internal/version.BuildDate=${BUILD_DATE}" \
    -o /out/marchi ./cmd/marchi

# /data is created and chowned here (this stage has a shell; the
# distroless runtime stage below doesn't) so the final image can COPY it
# in pre-owned by the nonroot user — Docker seeds a fresh named volume
# mounted at /data from whatever's already at that path in the image,
# ownership included, the first time the volume is created.
RUN mkdir -p /data && chown 65532:65532 /data

# distroless/static's nonroot variant: no shell, no package manager, runs
# as uid/gid 65532 by default — the smallest attack surface available
# for a self-hosted service handling IMAP credentials and email content.
# The "static" (not "base") variant still ships /etc/ssl/certs, needed
# for outbound TLS to real IMAP/S3 servers.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /out/marchi /usr/local/bin/marchi
COPY --from=builder /data /data
COPY build/docker/config.yaml /etc/marchi/config.yaml

VOLUME /data
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/marchi", "--config", "/etc/marchi/config.yaml"]
