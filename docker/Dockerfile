# syntax=docker/dockerfile:1
# Build: docker build -f docker/Dockerfile --build-arg ENGINE=chrome -t kagemusha .
#        docker build -f docker/Dockerfile --build-arg ENGINE=lightpanda -t kagemusha:lightpanda .
# Test:  docker build -f docker/Dockerfile --target test .
ARG ENGINE=chrome

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

FROM alpine:3.24 AS runtime-chrome
RUN apk add --no-cache chromium ca-certificates tzdata \
 && adduser -D -u 10001 kagemusha
ENV KAGEMUSHA_ENGINE=chrome \
    KAGEMUSHA_ENGINE_PATH=/usr/bin/chromium

FROM alpine:3.24 AS runtime-lightpanda
# Experimental: fetch the nightly binary (URL follows the lightpanda-io/browser releases)
RUN apk add --no-cache ca-certificates tzdata wget \
 && wget -qO /usr/bin/lightpanda https://github.com/lightpanda-io/browser/releases/download/nightly/lightpanda-x86_64-linux \
 && chmod +x /usr/bin/lightpanda \
 && adduser -D -u 10001 kagemusha
ENV KAGEMUSHA_ENGINE=lightpanda \
    KAGEMUSHA_ENGINE_PATH=/usr/bin/lightpanda

FROM runtime-${ENGINE} AS runtime
COPY --from=build /out/kagemusha /usr/local/bin/kagemusha
USER kagemusha
EXPOSE 3000
ENTRYPOINT ["/usr/local/bin/kagemusha"]
