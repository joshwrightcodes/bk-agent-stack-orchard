# syntax=docker/dockerfile:1

# The Orchard controller, from the release of openai/orchard.
#
# Orchard publishes its image to ghcr.io/openai/orchard, and that package does not answer a pull
# without credentials. The last public image is ghcr.io/cirruslabs/orchard:0.55.0, and
# https://github.com/openai/orchard/issues/501 asks for the later ones. This image has the same shape as the Dockerfile of Orchard: the release
# binary on distroless, with its data in /data. It takes the archive of the release, and it checks
# the SHA-256 that mise.toml pins, which scripts/check-deps.py keeps in agreement. The image is for
# the registry of the operator, and it carries the licence of Orchard, FSL-1.1-ALv2. It is not part
# of the image of the controller, which carries no Orchard code (ADR-0010).

ARG ORCHARD_VERSION=0.57.0
ARG ORCHARD_SHA256_AMD64=8e54692d5d38eac228b63fc16f0e801c4d7335287b9f1922743e0ac6e4c382d5
ARG ORCHARD_SHA256_ARM64=5c744e2883ff02b3e3e42b9ecaa42922411843653ea708eb7e37ee393c945d1f

FROM --platform=$BUILDPLATFORM alpine:3.24@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS release
ARG TARGETARCH
ARG ORCHARD_VERSION
ARG ORCHARD_SHA256_AMD64
ARG ORCHARD_SHA256_ARM64
WORKDIR /release
RUN set -eu; \
    case "$TARGETARCH" in \
      amd64) sum="$ORCHARD_SHA256_AMD64" ;; \
      arm64) sum="$ORCHARD_SHA256_ARM64" ;; \
      *) echo "Orchard publishes no Linux release for $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    archive="orchard-linux-${TARGETARCH}.tar.gz"; \
    wget -q "https://github.com/openai/orchard/releases/download/${ORCHARD_VERSION}/${archive}"; \
    echo "${sum}  ${archive}" | sha256sum -c -; \
    tar -xzf "$archive" orchard LICENSE

FROM gcr.io/distroless/base-debian13:nonroot@sha256:0896741ba5bafd3ac87ea025a5f578952f2d238ddc3614cb368acc983a687aa2
ARG ORCHARD_VERSION
LABEL org.opencontainers.image.title="orchard" \
      org.opencontainers.image.version="${ORCHARD_VERSION}" \
      org.opencontainers.image.source="https://github.com/openai/orchard" \
      org.opencontainers.image.licenses="FSL-1.1-ALv2"
COPY --from=release /release/orchard /bin/orchard
COPY --from=release /release/LICENSE /usr/share/doc/orchard/LICENSE
ENV GIN_MODE=release \
    ORCHARD_HOME=/data
EXPOSE 6120
ENTRYPOINT ["/bin/orchard"]
CMD ["controller", "run"]
