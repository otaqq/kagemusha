# Development targets for kagemusha. Steps that need a browser run in Docker.
IMAGE ?= kagemusha
ENGINE ?= chrome
DOCKER_BUILD := DOCKER_BUILDKIT=0 docker build -f docker/Dockerfile

.PHONY: test itest testbase itest-lightpanda testbase-lightpanda build image up down run-image lint

## Unit tests with the local Go toolchain (no browser needed)
test:
	go vet ./... && go test ./... -cover

## Build image with Chromium for integration tests (slow only the first time)
testbase:
	$(DOCKER_BUILD) --target testbase -t $(IMAGE):testbase .

## Real-browser integration tests (source is mounted, no rebuild needed)
itest:
	docker run --rm -v "$(PWD):/src" -w /src -e KAGEMUSHA_ENGINE_PATH=/usr/bin/chromium \
	  $(IMAGE):testbase go test -tags integration -count=1 ./...

## Image with Chromium and Lightpanda (Debian) for the engine comparison tests
testbase-lightpanda:
	$(DOCKER_BUILD) --target testbase-lightpanda -t $(IMAGE):testbase-lightpanda .

## Run the render integration tests on both engines (source is mounted)
itest-lightpanda:
	docker run --rm -v "$(PWD):/src" -w /src $(IMAGE):testbase-lightpanda go test -tags integration -count=1 ./internal/render/chrome/

## Build the binary locally (run it in a container, which has Chromium)
build:
	CGO_ENABLED=0 go build -o bin/kagemusha ./cmd/kagemusha

## Runtime image (ENGINE=chrome|lightpanda)
image:
	$(DOCKER_BUILD) --target runtime --build-arg ENGINE=$(ENGINE) -t $(IMAGE):$(ENGINE) .

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
