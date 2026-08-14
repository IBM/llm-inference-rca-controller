.PHONY: help build test docker-build docker-push deploy clean

# Variables
IMAGE_NAME ?= prometheus-vpa-recommender
IMAGE_TAG ?= v1.0.0
IMAGE_REGISTRY ?= localhost
FULL_IMAGE = $(IMAGE_REGISTRY)/$(IMAGE_NAME):$(IMAGE_TAG)

# Container runtime (docker or podman)
CONTAINER_RUNTIME ?= podman

# Kind cluster name
KIND_CLUSTER_NAME ?= vpa-test

# Recommender tuning — override on the command line as needed:
#   make deploy CAPACITY_METRIC_NAME=wva_desired_capacity_per_device
#   make deploy PROMETHEUS_URL=https://... PROMETHEUS_INSECURE_SKIP_VERIFY=true
#
# Defaults match the metric-replayer dev setup (./scripts/kind-setup.sh deploy-prometheus).
# For WVA production override PROMETHEUS_URL and set PROMETHEUS_INSECURE_SKIP_VERIFY=true.
CAPACITY_METRIC_NAME ?= desired_capacity
PROMETHEUS_URL ?= http://prometheus.default.svc.cluster.local:9090
PROMETHEUS_INSECURE_SKIP_VERIFY ?= false
RECOMMENDER_NAME ?= prometheus
UPDATE_INTERVAL ?= 30s

help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

build: ## Build the binary
	@echo "Building binary..."
	go build -o bin/prometheus-vpa-recommender main.go

test: ## Run tests
	@echo "Running tests..."
	go test -v ./...

fmt: ## Format code
	@echo "Formatting code..."
	go fmt ./...

vet: ## Run go vet
	@echo "Running go vet..."
	go vet ./...

tidy: ## Tidy go modules
	@echo "Tidying go modules..."
	go mod tidy

##@ Container Images (Podman/Docker)

container-build: tidy ## Build container image with podman/docker
	@echo "Building container image with $(CONTAINER_RUNTIME): $(FULL_IMAGE)"
	$(CONTAINER_RUNTIME) build -t $(FULL_IMAGE) .

container-push: container-build ## Push container image
	@echo "Pushing container image: $(FULL_IMAGE)"
	$(CONTAINER_RUNTIME) push $(FULL_IMAGE)

# Legacy aliases for docker
docker-build: container-build ## Alias for container-build
docker-push: container-push ## Alias for container-push

##@ Kind Cluster Testing

kind-create: ## Create Kind cluster for testing
	@echo "Creating Kind cluster: $(KIND_CLUSTER_NAME)"
	@./scripts/kind-setup.sh create

kind-delete: ## Delete Kind cluster
	@echo "Deleting Kind cluster: $(KIND_CLUSTER_NAME)"
	@./scripts/kind-setup.sh delete

kind-load-image: container-build ## Build and load image to Kind cluster
	@echo "Loading image to Kind cluster..."
	@./scripts/kind-setup.sh load-image

kind-deploy: kind-load-image ## Deploy to Kind cluster
	@echo "Deploying to Kind cluster..."
	kubectl apply -f deploy/rbac.yaml
	$(call apply-deployment)

kind-undeploy: ## Undeploy from Kind cluster
	@echo "Undeploying from Kind cluster..."
	kubectl delete -f deploy/rbac.yaml
	kubectl delete -f deploy/deployment.yaml

kind-test: ## Run full test on Kind cluster
	@echo "Running full test on Kind cluster..."
	@./scripts/kind-setup.sh test

# apply-deployment patches the deployment manifest on-the-fly with the current
# Make variable values before applying, so no manual YAML editing is needed.
define apply-deployment
	sed \
		-e 's|--capacity-metric-name=.*|--capacity-metric-name=$(CAPACITY_METRIC_NAME)|' \
		-e 's|--prometheus-url=.*|--prometheus-url=$(PROMETHEUS_URL)|' \
		-e 's|--prometheus-insecure-skip-verify=.*|--prometheus-insecure-skip-verify=$(PROMETHEUS_INSECURE_SKIP_VERIFY)|' \
		-e 's|--recommender-name=.*|--recommender-name=$(RECOMMENDER_NAME)|' \
		-e 's|--update-interval=.*|--update-interval=$(UPDATE_INTERVAL)|' \
		-e 's|image: .*/$(IMAGE_NAME):.*|image: $(FULL_IMAGE)|' \
		deploy/deployment.yaml | kubectl apply -f -
endef

##@ Deployment

deploy: ## Deploy to Kubernetes (override vars: CAPACITY_METRIC_NAME, PROMETHEUS_URL, RECOMMENDER_NAME, UPDATE_INTERVAL, FULL_IMAGE)
	@echo "Deploying to Kubernetes..."
	@echo "  capacity-metric-name=$(CAPACITY_METRIC_NAME)"
	kubectl apply -f deploy/rbac.yaml
	$(call apply-deployment)

undeploy: ## Remove from Kubernetes
	@echo "Removing from Kubernetes..."
	kubectl delete -f deploy/deployment.yaml --ignore-not-found
	kubectl delete -f deploy/rbac.yaml --ignore-not-found

##@ Utilities

clean: ## Clean build artifacts
	@echo "Cleaning..."
	rm -rf bin/
	go clean

run-local: ## Run locally with kubeconfig (override vars: CAPACITY_METRIC_NAME, PROMETHEUS_URL, PROMETHEUS_INSECURE_SKIP_VERIFY, RECOMMENDER_NAME, UPDATE_INTERVAL)
	@echo "Running locally..."
	go run main.go \
		--kubeconfig=$(HOME)/.kube/config \
		--recommender-name=$(RECOMMENDER_NAME) \
		--prometheus-url=$(PROMETHEUS_URL) \
		--prometheus-insecure-skip-verify=$(PROMETHEUS_INSECURE_SKIP_VERIFY) \
		--update-interval=$(UPDATE_INTERVAL) \
		--capacity-metric-name=$(CAPACITY_METRIC_NAME) \
		--v=2

logs: ## Show recommender logs
	kubectl logs -n kube-system -l app=prometheus-vpa-recommender -f

status: ## Check deployment status
	@echo "Checking deployment status..."
	@kubectl get pods -n kube-system -l app=prometheus-vpa-recommender
	@echo ""
	@kubectl get vpa --all-namespaces

.DEFAULT_GOAL := help
