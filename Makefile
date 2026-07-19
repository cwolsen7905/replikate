IMAGE_REPO ?= ghcr.io/cwolsen7905/replikate
IMAGE_TAG  ?= latest
IMG        := $(IMAGE_REPO):$(IMAGE_TAG)
NAMESPACE  ?= replikate

LOCALBIN            := $(CURDIR)/bin
SETUP_ENVTEST       := $(LOCALBIN)/setup-envtest
ENVTEST_VERSION     ?= release-0.19
ENVTEST_K8S_VERSION ?= 1.31.0

.PHONY: tidy fmt vet build test test-integration smoke-test run docker-build docker-push deploy undeploy

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

$(SETUP_ENVTEST):
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@$(ENVTEST_VERSION)

test-integration: $(SETUP_ENVTEST) ## Run envtest-backed integration tests (downloads control-plane binaries into ./bin).
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test -tags integration ./... -count=1

smoke-test: ## Live webhook smoke test on a kind cluster (needs kind, docker, helm, kubectl).
	./hack/webhook-smoke-test.sh

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
