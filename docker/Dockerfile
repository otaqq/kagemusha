# syntax=docker/dockerfile:1
# Production images on distroless (no shell, no package manager). For a shell, see Dockerfile.slim.
# Build: docker build -f docker/Dockerfile --build-arg ENGINE=chrome -t kagemusha:chrome .
#        docker build -f docker/Dockerfile --build-arg ENGINE=lightpanda -t kagemusha:lightpanda .
# Test:  docker build -f docker/Dockerfile --build-arg ENGINE=chrome --target test .
#        (the integration tests run inside the runtime image itself, so a missing library fails the build)
ARG ENGINE=chrome
# Browser releases and their checksums (update together; the build fails on a mismatch)
ARG CHROME_VERSION=156.0.8078.73
ARG CHROME_SHA256_AMD64=77eb8d52798efc7a857ab61abcc62a456d8ea9635b02341d06dffee2761768c8
ARG CHROME_SHA256_ARM64=98c084708f0f1fa4ed589ad601cc79b437f0cebe60ffc9794af78a085c01a2a7
ARG LIGHTPANDA_VERSION=1.0.1
ARG LIGHTPANDA_SHA256_AMD64=551747e35c51ba107b5d5277eca4b48e077b2a3b40f09a047bf6670448bc35fd
ARG LIGHTPANDA_SHA256_ARM64=392ba370c12d9f045c36c1477db8b3e2985942c89532732ef8d1610057db7d5c
ARG DISTROLESS=gcr.io/distroless/cc-debian13:nonroot

# Go cross-compiles natively on the build machine, so multi-arch builds do not emulate the compiler
FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOARCH=${TARGETARCH:-$(go env GOARCH)} go build -trimpath -ldflags="-s -w" -o /out/kagemusha ./cmd/kagemusha

# Unit tests, plus the integration test binary that later runs inside the runtime image (test target only)
FROM build AS test-build
RUN go vet ./... && go test -count=1 ./... \
 && CGO_ENABLED=0 GOARCH=${TARGETARCH:-$(go env GOARCH)} go test -c -tags integration -o /out/render.test ./internal/render/chrome/

# chrome-headless-shell (Chrome for Testing) with every shared library it needs, collected into /out.
# Libraries are taken per owning package, so dlopen'ed siblings come along; NSS loads its modules
# and sqlite at runtime, so those packages are added explicitly. glibc, libgcc and libstdc++ are in distroless.
# Their dpkg records go to /var/lib/dpkg/status.d (the distroless layout), so scanners such as Trivy see them.
FROM debian:trixie-slim AS chrome
ARG CHROME_VERSION
ARG CHROME_SHA256_AMD64
ARG CHROME_SHA256_ARM64
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl unzip fontconfig fonts-dejavu-core \
      libnss3 libnspr4 libsqlite3-0 libexpat1 libdbus-1-3 libxkbcommon0 libgbm1 libasound2t64 libatk1.0-0t64 \
      libatk-bridge2.0-0t64 libcups2t64 libdrm2 libxcomposite1 libxdamage1 libxrandr2 libxfixes3 libpango-1.0-0 libcairo2 \
 && case "${TARGETARCH:-amd64}" in \
      amd64) plat=linux64; sum="$CHROME_SHA256_AMD64" ;; \
      arm64) plat=linux-arm64; sum="$CHROME_SHA256_ARM64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && curl -fsSLo /tmp/chrome.zip "https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/${plat}/chrome-headless-shell-${plat}.zip" \
 && echo "${sum}  /tmp/chrome.zip" | sha256sum -c - \
 && unzip -q /tmp/chrome.zip -d /opt && mv "/opt/chrome-headless-shell-${plat}" /opt/chrome && rm /tmp/chrome.zip
RUN fc=$(ldconfig -p | awk '/libfontconfig\.so\.1 /{print $NF; exit}') \
 && libs=$( (ldd /opt/chrome/chrome-headless-shell; ldd "$fc") | awk '/=>/ {print $3}' | sort -u) \
 && pkgs=$(for l in $libs; do dpkg -S "$(basename "$l")" 2>/dev/null | head -1 | cut -d: -f1; done | sort -u | grep -vx 'libc6\|libgcc-s1\|libstdc++6') \
 && mkdir -p /out \
 && for f in $(dpkg -L $pkgs libnss3 libsqlite3-0 | grep -E '\.so(\.[0-9]+)*$|\.chk$'); do \
      [ -e "$f" ] || continue; d="/out$(dirname "$f" | sed 's#^/lib/#/usr/lib/#')"; mkdir -p "$d"; cp -a "$f" "$d/"; \
    done \
 && mkdir -p /out/var/lib/dpkg/status.d \
 && for p in $pkgs libnss3 libsqlite3-0 fontconfig-config fonts-dejavu-core; do dpkg -s "$p" > "/out/var/lib/dpkg/status.d/$p"; done \
 && cp -a --parents /etc/fonts /usr/share/fonts /out/

# The Lightpanda release is a glibc build
FROM debian:trixie-slim AS lightpanda
ARG LIGHTPANDA_VERSION
ARG LIGHTPANDA_SHA256_AMD64
ARG LIGHTPANDA_SHA256_ARM64
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && case "${TARGETARCH:-amd64}" in \
      amd64) arch=x86_64; sum="$LIGHTPANDA_SHA256_AMD64" ;; \
      arm64) arch=aarch64; sum="$LIGHTPANDA_SHA256_ARM64" ;; \
      *) echo "unsupported architecture: $TARGETARCH" >&2; exit 1 ;; \
    esac \
 && curl -fsSLo /lightpanda "https://github.com/lightpanda-io/browser/releases/download/${LIGHTPANDA_VERSION}/lightpanda-${arch}-linux" \
 && echo "${sum}  /lightpanda" | sha256sum -c - \
 && chmod +x /lightpanda

FROM ${DISTROLESS} AS runtime-chrome
COPY --from=chrome /out/ /
COPY --from=chrome /opt/chrome /opt/chrome
ENV KAGEMUSHA_ENGINE=chrome \
    KAGEMUSHA_ENGINE_PATH=/opt/chrome/chrome-headless-shell

FROM ${DISTROLESS} AS runtime-lightpanda
COPY --from=lightpanda /lightpanda /usr/bin/lightpanda
ENV KAGEMUSHA_ENGINE=lightpanda \
    KAGEMUSHA_ENGINE_PATH=/usr/bin/lightpanda

# Integration tests inside the runtime image (same files, same user)
FROM runtime-chrome AS test-chrome
FROM runtime-lightpanda AS test-lightpanda
# The tests pick engines by these variables; KAGEMUSHA_ENGINE_PATH means Chromium to them
ENV KAGEMUSHA_LIGHTPANDA_PATH=/usr/bin/lightpanda \
    KAGEMUSHA_ENGINE_PATH=
FROM test-${ENGINE} AS test
COPY --from=test-build /out/render.test /tests/render.test
COPY testdata /src/testdata
WORKDIR /src/internal/render/chrome
RUN ["/tests/render.test", "-test.count=1"]

FROM runtime-${ENGINE} AS runtime
COPY --from=build /out/kagemusha /usr/local/bin/kagemusha
EXPOSE 3000
HEALTHCHECK --interval=10s --timeout=5s --retries=3 CMD ["/usr/local/bin/kagemusha", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/kagemusha"]
