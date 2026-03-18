#!/bin/bash
# Deploy multi-vendor authz routing to OpenShift (modelless)
#
# Deploys vSR with authorization-based routing between two model vendors:
#   openai-model    = OpenAI compatible (mock-vllm simulator)
#   anthropic-model = Anthropic compatible (mock-anthropic simulator)
#
# Routing is based on user group membership (authz), not semantic classification.
# No ML models are deployed — fast startup, small footprint.
#
# Usage:
#   ./deploy-multi-vendor-authz.sh
#   ./deploy-multi-vendor-authz.sh --help

set -e

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

NAMESPACE="vllm-semantic-router-system"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$SCRIPT_DIR/../.."

log() { echo -e "${BLUE}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }

if [[ "$1" == "--help" ]] || [[ "$1" == "-h" ]]; then
    echo "Usage: $0"
    echo ""
    echo "Deploys a multi-vendor modelless setup on OpenShift with authz-based routing:"
    echo ""
    echo "  openai-model:    OpenAI compatible  (mock-vllm simulator)"
    echo "  anthropic-model: Anthropic compatible (mock-anthropic simulator)"
    echo ""
    echo "Routing is by user group membership:"
    echo "  team-a → openai-model    (no body translation)"
    echo "  team-b → anthropic-model (body + header translation)"
    echo "  unknown → openai-model   (default fallback)"
    echo ""
    echo "No ML models are deployed. vSR starts in seconds."
    echo ""
    echo "Test with:"
    echo "  curl -s POST http://\$API_ROUTE/v1/route \\"
    echo "    -H 'Content-Type: application/json' \\"
    echo "    -d '{\"model\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],"
    echo "         \"metadata\":{\"headers\":{\"x-authz-user-groups\":\"team-a\"}}}'"
    exit 0
fi

# Check login
if ! oc whoami &>/dev/null; then
    error "Not logged in to OpenShift. Please login first:"
    echo "  oc login <your-openshift-server-url>"
    exit 1
fi
success "Logged in as $(oc whoami)"

# Create namespace
log "Creating namespace: $NAMESPACE"
oc create namespace "$NAMESPACE" --dry-run=client -o yaml | oc apply -f -
for i in {1..30}; do
    if oc get namespace "$NAMESPACE" -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null | grep -q .; then
        warn "Namespace $NAMESPACE is terminating, waiting..."
        sleep 2
        continue
    fi
    break
done
success "Namespace ready"

# Build mock-vllm image (OpenAI simulator)
log "Building mock-vllm image (openai-model simulator)..."
MOCK_VLLM_DIR="$REPO_ROOT/tools/mock-vllm"

if ! oc get imagestream mock-vllm -n "$NAMESPACE" &>/dev/null; then
    oc new-build --name mock-vllm --binary --strategy=docker -n "$NAMESPACE"
fi
oc start-build mock-vllm --from-dir="$MOCK_VLLM_DIR" --follow -n "$NAMESPACE" || true

LATEST_BUILD=$(oc get builds -l buildconfig=mock-vllm -n "$NAMESPACE" -o name --sort-by=.metadata.creationTimestamp | tail -1)
if [[ -n "$LATEST_BUILD" ]]; then
    oc wait --for=condition=Complete "$LATEST_BUILD" -n "$NAMESPACE" --timeout=120s 2>/dev/null || warn "Build may still be in progress"
fi
success "mock-vllm image built"

# Build mock-anthropic image (Anthropic simulator)
log "Building mock-anthropic image (anthropic-model simulator)..."
MOCK_ANTHROPIC_DIR="$REPO_ROOT/tools/mock-anthropic"

if ! oc get imagestream mock-anthropic -n "$NAMESPACE" &>/dev/null; then
    oc new-build --name mock-anthropic --binary --strategy=docker -n "$NAMESPACE"
fi
oc start-build mock-anthropic --from-dir="$MOCK_ANTHROPIC_DIR" --follow -n "$NAMESPACE" || true

LATEST_BUILD=$(oc get builds -l buildconfig=mock-anthropic -n "$NAMESPACE" -o name --sort-by=.metadata.creationTimestamp | tail -1)
if [[ -n "$LATEST_BUILD" ]]; then
    oc wait --for=condition=Complete "$LATEST_BUILD" -n "$NAMESPACE" --timeout=120s 2>/dev/null || warn "Build may still be in progress"
fi
success "mock-anthropic image built"

# Deploy simulators
log "Deploying model simulators..."

MOCK_VLLM_IMAGE="image-registry.openshift-image-registry.svc:5000/$NAMESPACE/mock-vllm:latest"
MOCK_ANTHROPIC_IMAGE="image-registry.openshift-image-registry.svc:5000/$NAMESPACE/mock-anthropic:latest"

# Apply deployment-simulator.yaml as base (uses mock-vllm for both)
oc apply -f "$SCRIPT_DIR/deployment-simulator.yaml" -n "$NAMESPACE"

# Patch Model-B to use mock-anthropic image
oc patch deployment/vllm-model-b -n "$NAMESPACE" --type='json' \
    -p="[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"$MOCK_ANTHROPIC_IMAGE\"}]" >/dev/null
log "Patched model-b to use mock-anthropic image"

# Wait for services
log "Waiting for services to get ClusterIPs..."
for i in {1..30}; do
    MODEL_A_IP=$(oc get svc vllm-model-a -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}' 2>/dev/null || echo "")
    MODEL_B_IP=$(oc get svc vllm-model-b -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}' 2>/dev/null || echo "")

    if [[ -n "$MODEL_A_IP" ]] && [[ -n "$MODEL_B_IP" ]]; then
        success "Got ClusterIPs: openai-model=$MODEL_A_IP, anthropic-model=$MODEL_B_IP"
        break
    fi

    if [[ $i -eq 30 ]]; then
        error "Timeout waiting for service ClusterIPs"
        exit 1
    fi
    sleep 2
done

# Generate config with actual ClusterIPs
log "Generating configuration with ClusterIPs..."
TEMP_CONFIG="/tmp/config-multi-vendor-authz-dynamic.yaml"

sed -e "s/DYNAMIC_MODEL_A_IP/$MODEL_A_IP/g" \
    -e "s/DYNAMIC_MODEL_B_IP/$MODEL_B_IP/g" \
    "$SCRIPT_DIR/config-openshift-multi-vendor-authz.yaml" > "$TEMP_CONFIG"

# Create ConfigMaps
oc create configmap semantic-router-config \
    --from-file=config.yaml="$TEMP_CONFIG" \
    -n "$NAMESPACE" --dry-run=client -o yaml | oc apply -f -

oc create configmap envoy-config \
    --from-file=envoy.yaml="$SCRIPT_DIR/envoy-openshift.yaml" \
    -n "$NAMESPACE" --dry-run=client -o yaml | oc apply -f -

rm -f "$TEMP_CONFIG"
success "ConfigMaps created"

# Create routes
log "Creating OpenShift routes..."
cat <<EOF | oc apply -n "$NAMESPACE" -f -
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: semantic-router-api
  labels:
    app: semantic-router
spec:
  to:
    kind: Service
    name: semantic-router
  port:
    targetPort: api
---
apiVersion: route.openshift.io/v1
kind: Route
metadata:
  name: envoy-http
  labels:
    app: semantic-router
spec:
  to:
    kind: Service
    name: semantic-router
  port:
    targetPort: envoy-http
EOF
success "Routes created"

# Wait for deployments
log "Waiting for deployments to be ready..."
oc rollout status deployment/vllm-model-a -n "$NAMESPACE" --timeout=120s || warn "model-a may still be starting"
oc rollout status deployment/vllm-model-b -n "$NAMESPACE" --timeout=120s || warn "model-b may still be starting"
oc rollout status deployment/semantic-router -n "$NAMESPACE" --timeout=300s || warn "semantic-router may still be starting"

success "Multi-vendor authz routing deployment complete!"
echo ""
echo "  openai-model:    OpenAI compatible  (mock-vllm)"
echo "  anthropic-model: Anthropic compatible (mock-anthropic)"
echo ""
echo "  Routing: team-a → openai-model, team-b → anthropic-model"
echo ""

API_ROUTE=$(oc get route semantic-router-api -n "$NAMESPACE" -o jsonpath='{.spec.host}' 2>/dev/null || echo "pending")
echo "  API Route: http://$API_ROUTE"
echo ""
echo "Test commands:"
echo ""
echo "  # team-a → openai-model"
echo "  curl -s -X POST http://$API_ROUTE/v1/route \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],"
echo "         \"metadata\":{\"headers\":{\"x-authz-user-id\":\"alice\",\"x-authz-user-groups\":\"team-a\"}}}' | jq ."
echo ""
echo "  # team-b → anthropic-model (with body translation)"
echo "  curl -s -X POST http://$API_ROUTE/v1/route \\"
echo "    -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hello\"}],"
echo "         \"metadata\":{\"headers\":{\"x-authz-user-id\":\"bob\",\"x-authz-user-groups\":\"team-b\"}}}' | jq ."
echo ""
