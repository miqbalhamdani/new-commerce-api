# syntax=docker/dockerfile:1
# One image, three entrypoints (01-product-requirements.md, P1-001):
#   api, worker, migrate -- plus storage-init for a MinIO stand-in.
# The worker needs libvips (cgo), so every binary shares its base.
FROM golang:1.26-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends libvips-dev \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/ ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/storage-init

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends libvips42 ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --system --uid 10001 app
COPY --from=build /out/ /usr/local/bin/
ARG VERSION=dev
ENV SERVICE_VERSION=$VERSION PORT=8080
USER app
EXPOSE 8080
CMD ["api"]
