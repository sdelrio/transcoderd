.DEFAULT_GOAL := help
.PHONY: help

IMAGE_NAME ?= segator/$(APP)
IMAGE_VERSION ?= $(shell git rev-parse --short HEAD)

help: ## Display this help section
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z\$$/]+.*:.*?##\s/ {printf "\033[36m%-38s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

transcoderd-cli:
	go run build.go build worker -m console

transcoderd-docker:
	docker build -t transcoderd -f server/Dockerfile .

transcoderd-gui:

transcoderd:
transcoderd: transcoderd-cli transcoderd-docker transcoderd-gui

transcoderw-cli:
	go run build.go build worker -m console

transcoderw-docker:
	docker build -t transcoderw -f worker/Dockerfile .

transcoderw-gui:

transcoderw: transcoderw-cli transcoderw-docker transcoderw-gui

build: transcoderd transcoderw

docker-build-server: ## Docker build server transcoderd
docker-build-server: APP:=transcoderd
docker-build-server: DOCKERFILE:=server/Dockerfile
docker-build-server: docker-build

docker-build-worker: ## Docker buid client encoder-agent
docker-build-worker: APP:=encoder-agent
docker-build-worker: DOCKERFILE:=worker/Dockerfile.encode

docker-build-psg-agent: ## Docker build psg-agent
docker-build-psg-agent: APP:=psg-agent
docker-build-psg-agent: DOCKERFILE:=worker/Dockerfile.psg .

docker-build: ## Docker build server, worker, psg-agent
docker-build: docker-build-server docker-build-worker docker-build-psg-agent

docker-run-server: ## Docker run server transcoderd
docker-run-server: APP:=transcoderd
docker-run-server: docker-run

docker-run-worker: ## Docker run worker encoder-agent
docker-run-worker: APP:=transcoderd
docker-run-worker: docker-run

docker-run-psg-agent: ## Docker run psg-agent
docker-run-psg-agent: APP:=psg-agent
docker-run-psg-agent: docker-run

docker-build:
	@echo docker build -t $(IMAGE_NAME):$(IMAGE_VERSION) -f $(DOCKERFILE) .

docker-run:
	@echo docker run $(IMAGE_NAME):$(IMAGE_VERSION)

