# SPDX-License-Identifier: Apache-2.0
FROM golang:1.27.2-alpine AS build

WORKDIR /src

# go.mod must not carry a `replace` line here: the path it points at does not
# exist inside the build context. CI refuses such a commit.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/FreshLabDev/quoto/internal/buildinfo.Version=${VERSION} \
      -X github.com/FreshLabDev/quoto/internal/buildinfo.Commit=${COMMIT} \
      -X github.com/FreshLabDev/quoto/internal/buildinfo.Date=${DATE}" \
    -o /out/quoto ./cmd/bot

FROM alpine:3.24

# ffmpeg and ffprobe shrink video and audio before they go to OpenRouter.
RUN apk add --no-cache ca-certificates ffmpeg \
    && addgroup -g 1000 quoto \
    && adduser -D -H -u 1000 -G quoto quoto \
    && mkdir -p /app/logs \
    && chown quoto:quoto /app/logs

WORKDIR /app

COPY --from=build /out/quoto /app/quoto

# A soft limit for the Go heap only; ffmpeg runs as a child process and is not
# counted. It keeps the collector working before the container limit is near.
ENV GOMEMLIMIT=128MiB

# docker inspect answers which build is running without starting it.
ARG VERSION=dev
ARG COMMIT=none
LABEL org.opencontainers.image.title="Quoto" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.source="https://github.com/FreshLabDev/quoto" \
      org.opencontainers.image.licenses="Apache-2.0"

# uid/gid 1000 own the host's ./logs directory that the stack mounts.
USER quoto

EXPOSE 8080

ENTRYPOINT ["/app/quoto"]
