#!/usr/bin/env bash

set -e

# Configuration
CLUSTER_NAME="${KIND_CLUSTER_NAME:-vpa-test}"
IMAGE_NAME="${IMAGE_NAME:-prometheus-vpa-recommender}"
IMAGE_TAG="${IMAGE_TAG:-v1.0.0}"
IMAGE_REGISTRY="${IMAGE_REGISTRY:-localhost}"
FULL_IMAGE="${IMAGE_REGISTRY}/${IMAGE_NAME}:${IMAGE_TAG}"
CONTAINER_RUNTIME="${CONTAINER_RUNTIME:-podman}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

check_prerequisites() {
    log_info "Checking prerequisites..."
    
    if ! command -v kind &> /dev/null; then
        log_error "kind is not installed. Please install it from https://kind.sigs.k8s.io/"
        exit 1
    fi
    
    if ! command -v kubectl &> /dev/null; then
        log_error "kubectl is not installed. Please install it."
        exit 1
    fi
    
    if ! command -v ${CONTAINER_RUNTIME} &> /dev/null; then
        log_error "${CONTAINER_RUNTIME} is not installed. Please install it."
        exit 1
    fi
    
    if ! command -v make &> /dev/null; then
        log_error "make is not installed. Please install it."
        exit 1
    fi
    
    log_info "All prerequisites are installed."
}

create_cluster() {
    log_info "Creating Kind cluster: ${CLUSTER_NAME}"
    
    if kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_warn "Cluster ${CLUSTER_NAME} already exists. Deleting it first..."
        kind delete cluster --name "${CLUSTER_NAME}"
    fi
    
    # Create Kind cluster with custom config
    cat <<EOF | kind create cluster --name "${CLUSTER_NAME}" --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 30080
    hostPort: 30080
    protocol: TCP
  - containerPort: 30090
    hostPort: 30090
    protocol: TCP
EOF
    
    log_info "Cluster created successfully!"
    
    # Wait for cluster to be ready
    log_info "Waiting for cluster to be ready..."
    kubectl wait --for=condition=Ready nodes --all --timeout=120s
    
    log_info "Installing VPA CRDs..."
    install_vpa_crds
    
    log_info "Cluster is ready!"
}

install_vpa_crds() {
    # Install VPA CRDs
    kubectl apply -f https://raw.githubusercontent.com/sunya-ch/k8s-autoscaler/refs/heads/dra-autoscaler/vertical-pod-autoscaler/charts/vertical-pod-autoscaler/crds/vpa-v1-crd-gen.yaml || {
        log_warn "Failed to install VPA CRDs from upstream, trying local copy..."
        # Fallback to creating minimal CRD
        cat <<EOF | kubectl apply -f -
apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: verticalpodautoscalers.autoscaling.k8s.io
spec:
  group: autoscaling.k8s.io
  names:
    kind: VerticalPodAutoscaler
    listKind: VerticalPodAutoscalerList
    plural: verticalpodautoscalers
    shortNames:
    - vpa
    singular: verticalpodautoscaler
  scope: Namespaced
  versions:
  - name: v1
    served: true
    storage: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            x-kubernetes-preserve-unknown-fields: true
          status:
            type: object
            x-kubernetes-preserve-unknown-fields: true
EOF
    }
}

delete_cluster() {
    log_info "Deleting Kind cluster: ${CLUSTER_NAME}"
    
    if ! kind get clusters | grep -q "^${CLUSTER_NAME}$"; then
        log_warn "Cluster ${CLUSTER_NAME} does not exist."
        return 0
    fi
    
    kind delete cluster --name "${CLUSTER_NAME}"
    log_info "Cluster deleted successfully!"
}

build_and_load_image() {
    log_info "Building container image using make..."
    
    # Use make to build the image (respects CONTAINER_RUNTIME env var)
    make container-build
    
    log_info "Saving image to tar..."
    ${CONTAINER_RUNTIME} save "${FULL_IMAGE}" -o /tmp/${IMAGE_NAME}.tar
    
    log_info "Loading image to Kind cluster..."
    kind load image-archive /tmp/${IMAGE_NAME}.tar --name "${CLUSTER_NAME}"
    
    log_info "Cleaning up tar file..."
    rm -f /tmp/${IMAGE_NAME}.tar
    
    log_info "Image loaded successfully!"
}

deploy_recommender() {
    log_info "Deploying prometheus-vpa-recommender..."
    
    # Apply RBAC first (ServiceAccount, ClusterRole, ClusterRoleBinding)
    log_info "Applying RBAC..."
    kubectl apply -f deploy/rbac.yaml
    
    # Wait a moment for ServiceAccount to be created
    sleep 2
    
    # Verify ServiceAccount exists
    if ! kubectl get serviceaccount prometheus-vpa-recommender -n kube-system &> /dev/null; then
        log_error "ServiceAccount not created. Checking..."
        kubectl get serviceaccount -n kube-system
        return 1
    fi
    
    log_info "ServiceAccount created successfully"
    
    # Update deployment to use local image and apply
    log_info "Applying deployment..."
    cat deploy/deployment.yaml | \
        sed "s|image:.*|image: ${FULL_IMAGE}|" | \
        sed "s|imagePullPolicy:.*|imagePullPolicy: Never|" | \
        kubectl apply -f -
    
    log_info "Waiting for recommender to be ready..."
    kubectl wait --for=condition=Ready pod -n kube-system -l app=prometheus-vpa-recommender --timeout=120s || {
        log_error "Recommender failed to start. Checking logs..."
        kubectl describe pod -n kube-system -l app=prometheus-vpa-recommender
        kubectl logs -n kube-system -l app=prometheus-vpa-recommender --tail=50 || true
        return 1
    }
    
    log_info "Recommender deployed successfully!"
}

deploy_fake_prometheus() {
    log_info "Deploying metric-replayer as Prometheus endpoint..."

    # Build and load the metric-replayer image
    local mr_image="localhost/metric-replayer:v1.0.0"
    log_info "Building metric-replayer image..."
    ${CONTAINER_RUNTIME} build -t "${mr_image}" demo/metric-replayer/

    log_info "Loading metric-replayer image into Kind cluster..."
    ${CONTAINER_RUNTIME} save "${mr_image}" -o /tmp/metric-replayer.tar
    kind load image-archive /tmp/metric-replayer.tar --name "${CLUSTER_NAME}"
    rm -f /tmp/metric-replayer.tar

    kubectl apply -f demo/metric-replayer/deploy.yaml

    log_info "Waiting for metric-replayer to be ready..."
    kubectl wait --for=condition=Ready pod -l app=metric-replayer --timeout=60s || {
        log_error "metric-replayer failed to start. Checking logs..."
        kubectl describe pod -l app=metric-replayer
        kubectl logs -l app=metric-replayer --tail=50 || true
        return 1
    }

    log_info "metric-replayer deployed successfully!"

    # Smoke-test: query the health endpoint (non-interactive, safe in scripts)
    log_info "Smoke-testing metric-replayer API..."
    kubectl run test-prometheus --image=curlimages/curl --restart=Never --attach=false -- \
        curl -sf http://prometheus.default.svc.cluster.local:9090/health
    kubectl wait --for=condition=Succeeded pod/test-prometheus --timeout=30s \
        && log_info "metric-replayer health check passed" \
        || log_warn "metric-replayer health check did not complete in time"
    kubectl delete pod test-prometheus --ignore-not-found
}

deploy_test_app() {
    log_info "Deploying test application..."
    
    kubectl apply -f demo/test-app.yaml
    
    log_info "Test application deployed!"
}

run_tests() {
    log_info "Running tests..."
    
    # Check if recommender is running
    log_info "Checking recommender status..."
    kubectl get pods -n kube-system -l app=prometheus-vpa-recommender
    
    # Check VPA
    log_info "Checking VPA status..."
    kubectl get vpa test-app-vpa -o yaml
    
    # Check logs
    log_info "Checking recommender logs..."
    kubectl logs -n kube-system -l app=prometheus-vpa-recommender --tail=20
    
    log_info "Tests completed!"
}

show_usage() {
    cat <<EOF
Usage: $0 <command>

Commands:
    create          Create Kind cluster and install VPA CRDs
    delete          Delete Kind cluster
    load-image      Build (using make) and load image to Kind cluster
    deploy          Deploy recommender to cluster (applies RBAC first)
    deploy-prometheus Deploy fake Prometheus service
    test            Run full test (create cluster, deploy, test)
    logs            Show recommender logs
    status          Show cluster and deployment status
    help            Show this help message

Environment Variables:
    KIND_CLUSTER_NAME       Name of the Kind cluster (default: vpa-test)
    IMAGE_NAME              Name of the container image (default: prometheus-vpa-recommender)
    IMAGE_TAG               Tag of the container image (default: latest)
    IMAGE_REGISTRY          Registry for the image (default: localhost)
    CONTAINER_RUNTIME       Container runtime to use (default: podman)

Examples:
    # Create cluster and run full test
    $0 test

    # Create cluster only
    $0 create

    # Build (using make) and load image
    $0 load-image

    # Deploy recommender (applies RBAC first)
    $0 deploy

    # Check status
    $0 status

    # View logs
    $0 logs

    # Clean up
    $0 delete

Note:
    - This script uses 'make container-build' to build the image
    - RBAC is automatically applied before deployment
    - ServiceAccount is created before the deployment
EOF
}

main() {
    case "${1:-}" in
        create)
            check_prerequisites
            create_cluster
            ;;
        delete)
            delete_cluster
            ;;
        load-image)
            check_prerequisites
            build_and_load_image
            ;;
        deploy)
            deploy_recommender
            ;;
        deploy-prometheus)
            deploy_fake_prometheus
            ;;
        test)
            check_prerequisites
            create_cluster
            build_and_load_image
            deploy_fake_prometheus
            deploy_recommender
            deploy_test_app
            sleep 5
            run_tests
            ;;
        logs)
            kubectl logs -n kube-system -l app=prometheus-vpa-recommender -f
            ;;
        status)
            log_info "Cluster status:"
            kubectl get nodes
            echo ""
            log_info "Recommender status:"
            kubectl get pods -n kube-system -l app=prometheus-vpa-recommender
            echo ""
            log_info "VPAs:"
            kubectl get vpa --all-namespaces
            ;;
        help|--help|-h)
            show_usage
            ;;
        *)
            log_error "Unknown command: ${1:-}"
            echo ""
            show_usage
            exit 1
            ;;
    esac
}

main "$@"

