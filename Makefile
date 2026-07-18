IMAGE_REPO ?= ghcr.io/cwolsen7905/replikate
IMAGE_TAG  ?= latest
IMG        := $(IMAGE_REPO):$(IMAGE_TAG)
NAMESPACE  ?= replikate

.PHONY: tidy fmt vet build test run docker-build docker-push deploy undeploy

tidy: ## Resolve and pin dependencies (writes go.mod/go.sum).
	go mod tidy

fmt:
	go fmt ./...

vet:
	go vet ./...

build: tidy ## Build the manager binary into ./bin.
	CGO_ENABLED=0 go build -o bin/replikate ./cmd

test:
	go test ./...

run: ## Run against the cluster in your current kubeconfig.
	go run ./cmd --leader-elect=false

docker-build: ## Build the container image.
	docker build -t $(IMG) .

docker-push: ## Push the container image.
	docker push $(IMG)

deploy: ## Install/upgrade the Helm release.
	helm upgrade --install replikate charts/replikate \
		--namespace $(NAMESPACE) --create-namespace \
		--set image.repository=$(IMAGE_REPO) \
		--set image.tag=$(IMAGE_TAG)

undeploy: ## Remove the Helm release.
	helm uninstall replikate --namespace $(NAMESPACE)
