# MinIO, standing in for R2 on a developer machine. Its community edition is
# published as source only now, so it is built from the release Homebrew uses.
FROM golang:1.26-bookworm AS build
RUN CGO_ENABLED=0 GOBIN=/out go install github.com/minio/minio@RELEASE.2025-10-15T17-29-55Z

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends curl \
    && rm -rf /var/lib/apt/lists/* \
    && printf '#!/bin/sh\nexec curl -fsS -o /dev/null http://localhost:9000/minio/health/ready\n' > /usr/local/bin/minio-ready \
    && chmod +x /usr/local/bin/minio-ready
COPY --from=build /out/minio /usr/local/bin/minio
EXPOSE 9000
CMD ["minio", "server", "/data"]
