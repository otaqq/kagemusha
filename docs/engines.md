# Engines and images

kagemusha drives a browser over CDP. Two engines are supported, each in two image flavors.

| Image | Base | Engine | Size |
|---|---|---|---|
| `kagemusha:chrome` (default) | `gcr.io/distroless/cc-debian13:nonroot` | chrome-headless-shell 156 (Chrome for Testing) | 348 MB |
| `kagemusha:lightpanda` | `gcr.io/distroless/cc-debian13:nonroot` | Lightpanda 1.0.1 | 112 MB |
| `kagemusha:chrome-slim` | `debian:trixie-slim` | Debian Chromium 156 | 809 MB |
| `kagemusha:lightpanda-slim` | `debian:trixie-slim` | Lightpanda 1.0.1 | 167 MB |

- **distroless** images have no shell and no package manager: less to attack, less to patch. Use them in production. Health checks run `kagemusha healthcheck`, which probes `/healthz`.
- **slim** images add a shell (`docker exec -it … bash`) for debugging the same engine you run in production.

## Benchmark

The same 40 renders of the integration-test SPA (`testdata/spa/index.html`: one API call, `window.prerenderReady`), 4 at a time, against each image on one machine:

| Image | Time | Peak memory |
|---|---|---|
| `chrome` | 4.2 s | 340 MB |
| `chrome-slim` | 12.6 s | 432 MB |
| `lightpanda` | 3.5 s | 32–74 MB |
| `lightpanda-slim` | 3.5 s | 32 MB |

Peak memory is the container's cgroup `memory.peak` (driver and browser together); Lightpanda varied between runs. The test SPA is tiny: treat the numbers as relative, and measure your own pages.

Two things stand out:

- **chrome-headless-shell is ~3× faster than Debian's full Chromium** at the same major version. It is Chrome's old headless mode, shipped as a separate build; the slim image runs the full browser in its new headless mode.
- **Lightpanda uses an order of magnitude less memory**: no layout, no rendering, no image loading.

## Chromium

The reference engine. kagemusha uses only four CDP areas — Page navigation, Fetch interception, Runtime evaluation, DOM capture — plus `Browser.getVersion` for health, and runs every render in its own disposable browser context.

### The distroless image

chrome-headless-shell is not self-contained: it links against NSS, fontconfig, X and GLib libraries that distroless does not have. `docker/Dockerfile` builds the image in two steps:

1. In a `debian:trixie-slim` stage, download chrome-headless-shell (version and per-architecture SHA-256 pinned), install its library packages, and collect into `/out`:
   - every shared library of every package that owns a library `ldd` resolves — a whole package rather than single files, because libraries are also loaded at runtime with `dlopen`;
   - `libnss3` and `libsqlite3-0` explicitly: NSS loads its crypto modules and their sqlite database at runtime, and without them Chrome aborts on the first TLS connection;
   - fontconfig and the DejaVu fonts;
   - the `dpkg` record of each copied package into `/var/lib/dpkg/status.d/`, the layout distroless uses, so vulnerability scanners such as Trivy see the copied packages.
2. Copy `/out` and the browser onto distroless.

A library missing from that set only shows up when a page needs it, so **the integration tests run inside the runtime image itself** (`--target test`): the test binary is built statically and executed on distroless, with the same files and user as production. A missing library fails the image build, not a render in production.

Fonts: only DejaVu is included. Glyphs do not matter for captured HTML, but text metrics do feed layout (for example lazy loading driven by `IntersectionObserver`). If your pages are in a script DejaVu does not cover, compare with the slim image, or add fonts (for CJK, Noto CJK adds tens of MB).

### The slim image

Debian's `chromium` package with all its dependencies. Larger and slower than chrome-headless-shell, but it is the regular browser (new headless mode) from the distribution, updated with Debian security releases.

## Lightpanda

[Lightpanda](https://github.com/lightpanda-io/browser) is a browser built for automation: a JavaScript engine and DOM, but no layout and no rendering. It speaks CDP, so kagemusha drives it with the same code, with these differences:

| | Chromium | Lightpanda |
|---|---|---|
| Process | Browser printing its DevTools URL | CDP server: `lightpanda serve --host 127.0.0.1 --port <free port>` |
| Connection | One, shared by all renders | **One per render** (one browser context per connection) |
| User agent marker | Browser UA + ` kagemusha` via `Emulation.setUserAgentOverride` | `--user-agent-suffix kagemusha` (it must not claim to be another browser) |
| Telemetry | — | **Off**: kagemusha sets `LIGHTPANDA_DISABLE_TELEMETRY=true` |
| Health | `Browser.getVersion` over CDP | Process alive and `/json/version` answers |
| Images, layout | Yes | No (`KAGEMUSHA_BLOCK_RESOURCE_TYPES=Image` changes nothing) |

The release binary is a glibc build, so both Lightpanda images are Debian-based. Lightpanda passes the same integration contract as Chromium — completion by `prerenderReady`, idle and timeout, HTTP and declared status codes, header forwarding to the target origin only, the SSRF guard, cross-host redirects, script stripping, concurrent renders — and renders the test SPA to the same text (`internal/render/chrome/engines_integration_test.go`).

**Compare it with Chromium on your own pages before switching.** Its Web API coverage is partial; a page that uses an API it lacks can render without an error but with content missing. A practical check: render a sample of your URLs with both images and compare titles, structured data and body text.

There is no automatic fallback from Lightpanda to Chromium: telling "complete" from "silently incomplete" in general is not reliable, and it would need both browsers in one image. Pick the engine per deployment.

## Choosing

| If | Use |
|---|---|
| You need the widest compatibility | `chrome` |
| Memory or image size dominates, and your pages render correctly | `lightpanda` |
| You are debugging either in place | the `-slim` variant of the same engine |

## Updating the pinned browsers

chrome-headless-shell and Lightpanda are downloaded binaries, not distribution packages: scanners do not report their vulnerabilities, so bump them deliberately.

- Chrome: `CHROME_VERSION` and `CHROME_SHA256_AMD64` / `CHROME_SHA256_ARM64` in `docker/Dockerfile`. Versions are listed in Chrome for Testing's `known-good-versions-with-downloads.json`.
- Lightpanda: `LIGHTPANDA_VERSION` and its checksums, in both Dockerfiles.

Download each archive, take its SHA-256, update the version and checksums together (the build fails on a mismatch), and run `make itest` and `make itest-image ENGINE=…`.
