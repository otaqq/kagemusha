# syntax=docker/dockerfile:1
# Build: docker build -f docker/Dockerfile --build-arg ENGINE=chrome -t kagemusha .
#        docker build -f docker/Dockerfile --build-arg ENGINE=lightpanda -t kagemusha:lightpanda .
# Test:  docker build -f docker/Dockerfile --target test .
#        docker build -f docker/Dockerfile --target test-lightpanda .
ARG ENGINE=chrome
# Lightpanda release and its checksums (update together; the build fails on a mismatch)
ARG LIGHTPANDA_VERSION=1.0.1
ARG LIGHTPANDA_SHA256_AMD64=551747e35c51ba107b5d5277eca4b48e077b2a3b40f09a047bf6670448bc35fd
ARG LIGHTPANDA_SHA256_ARM64=392ba370c12d9f045c36c1477db8b3e2985942c89532732ef8d1610057db7d5c

FROM golang:1.27-alpine3.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kagemusha ./cmd/kagemusha

# Real-browser integration tests (mocks do not catch real browser failures)
# testbase is the build environment with Chromium. Reuse it during development: `docker run -v $PWD:/src kagemusha:testbase go test ...`
FROM build AS testbase
RUN apk add --no-cache chromium
ENV KAGEMUSHA_ENGINE_PATH=/usr/bin/chromium

FROM testbase AS test
RUN go vet ./... && go test -tags integration -count=1 ./...

# The Lightpanda release is a glibc build: it runs on Debian, not on Alpine
FROM alpine:3.24 AS lightpanda
ARG LIGHTPANDA_VERSION
ARG LIGHTPANDA_SHA256_AMD64
ARG LIGHTPANDA_SHA256_ARM64
ARG TARGETARCH
RUN apk add --no-cache wget \
 && case "${TARGETARCH:-amd64}" in \
      amd64) arch=x86_64; sum="$LIGHTPANDA_SHA256_AMD64" ;; \
      arm64) arch=aarch64; sum="$LIGHTPANDA_SHA256_ARM64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && wget -qO /lightpanda "https://github.com/lightpanda-io/browser/releases/download/${LIGHTPANDA_VERSION}/lightpanda-${arch}-linux" \
 && echo "${sum}  /lightpanda" | sha256sum -c - \
 && chmod +x /lightpanda

# Both engines on Debian, for the engine comparison tests
FROM golang:1.27-trixie AS testbase-lightpanda
RUN apt-get update && apt-get install -y --no-install-recommends chromium && rm -rf /var/lib/apt/lists/*
COPY --from=lightpanda /lightpanda /usr/bin/lightpanda
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV KAGEMUSHA_ENGINE_PATH=/usr/bin/chromium \
    KAGEMUSHA_LIGHTPANDA_PATH=/usr/bin/lightpanda

FROM testbase-lightpanda AS test-lightpanda
RUN go test -tags integration -count=1 ./internal/render/chrome/

FROM alpine:3.24 AS runtime-chrome
RUN apk add --no-cache chromium ca-certificates tzdata \
 && adduser -D -u 10001 kagemusha
ENV KAGEMUSHA_ENGINE=chrome \
    KAGEMUSHA_ENGINE_PATH=/usr/bin/chromium

FROM debian:trixie-slim AS runtime-lightpanda
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/* \
 && useradd -u 10001 -m -s /usr/sbin/nologin kagemusha
COPY --from=lightpanda /lightpanda /usr/bin/lightpanda
ENV KAGEMUSHA_ENGINE=lightpanda \
    KAGEMUSHA_ENGINE_PATH=/usr/bin/lightpanda

FROM runtime-${ENGINE} AS runtime
COPY --from=build /out/kagemusha /usr/local/bin/kagemusha
USER kagemusha
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/kagemusha"]
