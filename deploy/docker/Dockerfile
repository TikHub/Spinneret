# syntax=docker/dockerfile:1.7
# Multi-stage build: web console (Vite) -> Go binaries (web assets embedded) -> distroless runtime.

FROM node:22-alpine AS web
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN --mount=type=cache,id=pnpm-store,target=/root/.local/share/pnpm/store \
    pnpm install --frozen-lockfile
COPY web/ ./
COPY proto/ /src/proto/
RUN pnpm build

FROM golang:1.27-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN --mount=type=cache,id=gomod,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
ARG VERSION=dev
RUN --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gobuild,target=/root/.cache/go-build \
    go build -ldflags "-s -w -X github.com/TikHub/Spinneret/internal/version.Version=${VERSION}" \
      -o /out/spinneret-server ./cmd/spinneret-server && \
    go build -ldflags "-s -w -X github.com/TikHub/Spinneret/internal/version.Version=${VERSION}" \
      -o /out/spnr ./cmd/spnr

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="spinneret" \
      org.opencontainers.image.description="Control plane for crawler and API nodes" \
      org.opencontainers.image.source="https://github.com/TikHub/Spinneret"
COPY --from=build /out/spinneret-server /out/spnr /usr/local/bin/
USER nonroot:nonroot
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=20s --retries=3 \
  CMD ["/usr/local/bin/spnr", "healthcheck", "--url", "http://127.0.0.1:8080/readyz"]
ENTRYPOINT ["/usr/local/bin/spinneret-server"]
