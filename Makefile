# Development targets for kagemusha. Steps that need a browser run in Docker.
IMAGE ?= kagemusha
ENGINE ?= chrome
DOCKER_BUILD := docker build

.PHONY: test itest testbase itest-image build image image-slim images up down run-image lint

## Unit tests with the local Go toolchain (no browser needed)
test:
	go vet ./... && go test ./... -cover

## Development image with Go, Chromium and Lightpanda (slow only the first time)
testbase:
	$(DOCKER_BUILD) -f docker/Dockerfile.slim --target testbase -t $(IMAGE):testbase .

## Real-browser integration tests on both engines (source is mounted, no rebuild needed)
itest:
	docker run --rm -v "$(PWD):/src" -w /src $(IMAGE):testbase go test -tags integration -count=1 ./...

## Integration tests inside the production (distroless) image of ENGINE
itest-image:
	$(DOCKER_BUILD) -f docker/Dockerfile --build-arg ENGINE=$(ENGINE) --target test .

## Build the binary locally (run it in a container, which has a browser)
build:
	CGO_ENABLED=0 go build -o bin/kagemusha ./cmd/kagemusha

## Production image (distroless, ENGINE=chrome|lightpanda)
image:
	$(DOCKER_BUILD) -f docker/Dockerfile --build-arg ENGINE=$(ENGINE) -t $(IMAGE):$(ENGINE) .

## Image with a shell (debian:trixie-slim, ENGINE=chrome|lightpanda)
image-slim:
	$(DOCKER_BUILD) -f docker/Dockerfile.slim --build-arg ENGINE=$(ENGINE) -t $(IMAGE):$(ENGINE)-slim .

## All four images
images:
	$(MAKE) image ENGINE=chrome && $(MAKE) image ENGINE=lightpanda
	$(MAKE) image-slim ENGINE=chrome && $(MAKE) image-slim ENGINE=lightpanda

## Run the server with compose (http://localhost:3000)
up:
	docker compose -f docker/compose.yaml up --build

down:
	docker compose -f docker/compose.yaml down

## Run the image built by the image target
run-image:
	docker run --rm -p 3000:3000 --shm-size=512m -e KAGEMUSHA_CACHE_CONTROL="public, max-age=60" $(IMAGE):$(ENGINE)

lint:
	gofmt -l . && go vet ./...
