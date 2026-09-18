# syntax=docker/dockerfile:1
#
# mocktarget: scriptable fake crawl target plus an authenticating HTTP forward proxy, used by the
# Spinneret e2e and load tests. Build with the repository root as context:
#
#   docker build -f deploy/docker/mocktarget.Dockerfile -t spinneret-mocktarget:local .
#
# Ports: 9090 target site + admin API, 9091 forward proxy. See test/mocktarget/README.md.

ARG GO_VERSION=1.27

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
# mocktarget depends only on the Go standard library: the module files and its own package are the
# whole build input, which keeps the transferred context small and the layer cache stable.
COPY go.mod go.sum ./
COPY test/mocktarget ./test/mocktarget
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -trimpath -ldflags="-s -w" -o /out/mocktarget ./test/mocktarget

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mocktarget /usr/local/bin/mocktarget
USER nonroot:nonroot
EXPOSE 9090 9091
HEALTHCHECK --interval=5s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/mocktarget", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/mocktarget"]
CMD ["serve"]
