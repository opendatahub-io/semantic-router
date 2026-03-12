#!/bin/bash
# Deploy multi-provider setup to OpenShift
# Model-A = OpenAI compatible (mock-vllm)
# Model-B = Anthropic compatible (mock-anthropic)
# Uses config-openshift-multi-provider.yaml

set -e

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

NAMESPACE="vllm-semantic-router-system"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

log() { echo -e "${BLUE}[INFO]${NC} $*"; }
success() { echo -e "${GREEN}[SUCCESS]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*"; }

if [[ "$1" == "--help" ]] || [[ "$1" == "-h" ]]; then
    echo "Usage: $0"
    echo ""
    echo "Deploys a multi-provider simulator setup on OpenShift:"
    echo "  Model-A = OpenAI compatible (mock-vllm)"
    echo "  Model-B = Anthropic compatible (mock-anthropic)"
    echo ""
    echo "Uses config-openshift-multi-provider.yaml which sets"
    echo "api_format: anthropic for Model-B, enabling body and"
    echo "header translation in the /v1/route HTTP API."
    echo ""
    echo "Prerequisites:"
    echo "  - OpenShift cluster with oc CLI configured"
    echo "  - Run the standard deploy first:"
    echo "    ./deploy-to-openshift.sh --simulator --no-observability"
    echo "  - Then run this script to switch Model-B to Anthropic"
    exit 0
fi

# Check if logged in
if ! oc whoami &>/dev/null; then
    error "Not logged in to OpenShift. Please login first."
    exit 1
fi
success "Logged in as $(oc whoami)"

# Check if base deployment exists
if ! oc get deployment semantic-router -n "$NAMESPACE" &>/dev/null; then
    error "Base deployment not found. Run the standard deploy first:"
    echo "  ./deploy-to-openshift.sh --simulator --no-observability"
    exit 1
fi

# Build mock-anthropic image
log "Building mock-anthropic image..."
MOCK_ANTHROPIC_DIR="$SCRIPT_DIR/../../tools/mock-anthropic"

if ! oc get imagestream mock-anthropic -n "$NAMESPACE" &>/dev/null; then
    if [[ -f "$MOCK_ANTHROPIC_DIR/Dockerfile" ]]; then
        oc new-build --name mock-anthropic --binary --strategy=docker -n "$NAMESPACE"
    else
        error "mock-anthropic Dockerfile not found at: $MOCK_ANTHROPIC_DIR/Dockerfile"
        exit 1
    fi
fi

log "Uploading mock-anthropic source and building..."
oc start-build mock-anthropic --from-dir="$MOCK_ANTHROPIC_DIR" --follow -n "$NAMESPACE" || true

LATEST_BUILD=$(oc get builds -l buildconfig=mock-anthropic -n "$NAMESPACE" -o name --sort-by=.metadata.creationTimestamp | tail -1)
if [[ -n "$LATEST_BUILD" ]]; then
    if ! oc wait --for=condition=Complete "$LATEST_BUILD" -n "$NAMESPACE" --timeout=120s 2>/dev/null; then
        warn "Build may still be in progress. Checking status..."
        oc get builds -n "$NAMESPACE"
    fi
fi
success "mock-anthropic image built"

# Switch Model-B to mock-anthropic image
log "Switching Model-B to mock-anthropic (Anthropic API simulator)..."
ANTHROPIC_IMAGE="image-registry.openshift-image-registry.svc:5000/$NAMESPACE/mock-anthropic:latest"
oc patch deployment/vllm-model-b -n "$NAMESPACE" --type='json' \
    -p="[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/image\",\"value\":\"$ANTHROPIC_IMAGE\"}]" >/dev/null
oc rollout status deployment/vllm-model-b -n "$NAMESPACE" --timeout=120s
success "Model-B now uses mock-anthropic"

# Update config to multi-provider
log "Updating configuration to multi-provider..."
MODEL_A_IP=$(oc get svc vllm-model-a -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}')
MODEL_B_IP=$(oc get svc vllm-model-b -n "$NAMESPACE" -o jsonpath='{.spec.clusterIP}')

TEMP_CONFIG="/tmp/config-openshift-multi-provider-dynamic.yaml"
sed -e "s/DYNAMIC_MODEL_A_IP/$MODEL_A_IP/g" \
    -e "s/DYNAMIC_MODEL_B_IP/$MODEL_B_IP/g" \
    "$SCRIPT_DIR/config-openshift-multi-provider.yaml" > "$TEMP_CONFIG"

# Get existing tools_db.json
oc get configmap semantic-router-config -n "$NAMESPACE" -o jsonpath='{.data.tools_db\.json}' > /tmp/tools_db.json 2>/dev/null || echo '[]' > /tmp/tools_db.json

oc create configmap semantic-router-config \
    --from-file=config.yaml="$TEMP_CONFIG" \
    --from-file=tools_db.json=/tmp/tools_db.json \
    -n "$NAMESPACE" --dry-run=client -o yaml | oc apply -f -

rm -f "$TEMP_CONFIG"
success "Configuration updated to multi-provider"

# Restart semantic-router to pick up new config
log "Restarting semantic-router..."
oc rollout restart deployment/semantic-router -n "$NAMESPACE"
oc rollout status deployment/semantic-router -n "$NAMESPACE" --timeout=300s || warn "Rollout may still be in progress"

success "Multi-provider deployment complete!"
echo ""
echo "  openai-model:    OpenAI compatible (mock-vllm)"
echo "  anthropic-model: Anthropic compatible (mock-anthropic)"
echo ""
echo "Test with:"
echo "  API_ROUTE=\$(oc get route semantic-router-api -n $NAMESPACE -o jsonpath='{.spec.host}')"
echo ""
echo "  # OpenAI route (math → openai-model)"
echo "  curl -s -X POST http://\$API_ROUTE/v1/route -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"What is 2+2?\"}]}' | jq ."
echo ""
echo "  # Anthropic route (law → anthropic-model with body translation)"
echo "  curl -s -X POST http://\$API_ROUTE/v1/route -H 'Content-Type: application/json' \\"
echo "    -d '{\"model\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"Explain contract law\"}]}' | jq ."
echo ""
