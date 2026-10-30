# Development

The browser lives in Docker; everything else runs with your local Go toolchain.

## Commands

```bash
make test        # unit tests with local Go (no browser needed)
make lint        # gofmt + go vet
make testbase    # one-time: build the dev image (Go, Chromium, Lightpanda)
make itest       # real-browser integration tests on both engines, source mounted (no rebuild)
make itest-image ENGINE=chrome   # the same tests inside the production (distroless) image
make up          # run the server via compose (slim image) on http://localhost:3000
make image ENGINE=chrome && make run-image   # build/run a production image directly
make images      # all four images
```

## Workflow

Typical loop: edit → `make test` → `make itest` → `make up` and `curl "http://localhost:3000/render?url=https://example.com/"`.

## CI

CI ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs on every push and pull request: `unit` (gofmt, go vet, `go test ./...`), `integration` (`docker build -f docker/Dockerfile.slim --target test .` for both engines, then `docker build -f docker/Dockerfile --target test --build-arg ENGINE=…` so each production image tests itself and a library missing from distroless fails the build, then all four images), and `integration-arm64` (the test builds on a native arm64 runner).

Real-browser tests are mandatory in CI: mocks did not catch a single one of the production failures that shaped this design.

## Releasing

Push a version tag (`git tag v0.1.0 && git push origin v0.1.0`). The `release` workflow runs the tests on amd64 and arm64, then publishes the four images for both architectures to `ghcr.io/otaqq/kagemusha` with provenance and SBOM attestations. Tags: `0.1.0`, `0.1`, `latest` and `chrome` for the default image; `0.1.0-lightpanda`, `lightpanda-slim`, … for the others. Pre-releases (`v0.1.0-rc.1`) get only their exact tag.

## Layout

```
cmd/kagemusha/        entry point, wiring, signals
internal/config/      env → Config
internal/server/      HTTP layer, response contract, healthcheck probe
internal/flow/        single-flight, concurrency cap, FIFO queue
internal/lifecycle/   supervisor (watchdog, planned restart), worker exit policy
internal/render/      engine abstraction; chrome/ = chromedp implementation (Chromium and Lightpanda)
internal/netguard/    SSRF guard: target allowlist, non-public address check
internal/cache/       in-process LRU (TTL, entries, bytes)
internal/metrics/     Prometheus text format
docker/               Dockerfile (distroless), Dockerfile.slim (trixie-slim), compose
testdata/spa/         tiny SPAs used by integration tests
```

Updating the pinned browsers (chrome-headless-shell, Lightpanda): see [engines.md](engines.md#updating-the-pinned-browsers).
