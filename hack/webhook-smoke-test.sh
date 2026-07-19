#!/usr/bin/env bash
#
# Live end-to-end smoke test for Replikate's validating admission webhook.
#
# It stands up (or reuses) a kind cluster, installs cert-manager, builds and
# loads the Replikate image, deploys the chart with the webhook enabled, and
# then proves the webhook actually enforces at apply time:
#
#   * a source with a VALID sync selector is accepted, and
#   * a source with an INVALID sync selector is rejected by our webhook.
#
# This is the one path envtest can't model (real ValidatingWebhookConfiguration
# + cert-manager CA injection over TLS), so it's the last gate before a 1.0.
#
# Requires: kind, kubectl, helm, docker. Override any of the vars below:
#   CLUSTER, IMAGE_REPO, IMAGE_TAG, NS, CERT_MANAGER_VERSION, KEEP_CLUSTER=1
#
set -euo pipefail

CLUSTER="${CLUSTER:-replikate-smoke}"
IMAGE_REPO="${IMAGE_REPO:-replikate}"
IMAGE_TAG="${IMAGE_TAG:-smoke}"
IMAGE="${IMAGE_REPO}:${IMAGE_TAG}"
NS="${NS:-replikate-system}"
CERT_MANAGER_VERSION="${CERT_MANAGER_VERSION:-v1.16.2}"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

log()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
pass() { printf '\033[1;32mPASS:\033[0m %s\n' "$*"; }
fail() { printf '\033[1;31mFAIL:\033[0m %s\n' "$*" >&2; exit 1; }

cleanup() {
  if [[ "${KEEP_CLUSTER:-0}" != "1" && "${CREATED_CLUSTER:-0}" == "1" ]]; then
    log "Deleting kind cluster ${CLUSTER}"
    kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# --- 1. cluster --------------------------------------------------------------
if kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  log "Reusing existing kind cluster ${CLUSTER}"
else
  log "Creating kind cluster ${CLUSTER}"
  kind create cluster --name "${CLUSTER}" --wait 120s
  CREATED_CLUSTER=1
fi

# --- 2. image ----------------------------------------------------------------
log "Building and loading image ${IMAGE}"
docker build -t "${IMAGE}" "${REPO_ROOT}"
kind load docker-image "${IMAGE}" --name "${CLUSTER}"

# --- 3. cert-manager ---------------------------------------------------------
log "Installing cert-manager ${CERT_MANAGER_VERSION}"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
for d in cert-manager cert-manager-webhook cert-manager-cainjector; do
  kubectl -n cert-manager rollout status "deploy/${d}" --timeout=180s
done

# --- 4. install Replikate with the webhook enabled ---------------------------
log "Installing Replikate (webhook enabled)"
helm upgrade --install replikate "${REPO_ROOT}/charts/replikate" \
  --namespace "${NS}" --create-namespace \
  --set image.repository="${IMAGE_REPO}" \
  --set image.tag="${IMAGE_TAG}" \
  --set image.pullPolicy=IfNotPresent \
  --set webhook.enabled=true \
  --wait --timeout 180s
kubectl -n "${NS}" rollout status deploy/replikate --timeout=180s

log "Waiting for cert-manager to inject the webhook CA bundle"
for _ in $(seq 1 30); do
  ca="$(kubectl get validatingwebhookconfiguration replikate \
    -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)"
  [[ -n "${ca}" ]] && break
  sleep 5
done
[[ -n "${ca:-}" ]] || fail "CA bundle was never injected into the ValidatingWebhookConfiguration"
pass "CA bundle injected"

kubectl create namespace smoke-app --dry-run=client -o yaml | kubectl apply -f -

apply_source() { # name, selector
  kubectl apply -f - <<YAML 2>&1
apiVersion: v1
kind: ConfigMap
metadata:
  name: $1
  namespace: smoke-app
  annotations:
    replikate.brainchurts.com/sync: "$2"
data:
  k: v
YAML
}

# --- 5. positive case: a valid selector is accepted --------------------------
# Retry to absorb the brief window before the webhook endpoint serves TLS.
log "Applying a source with a VALID selector (expect: accepted)"
accepted=0
for _ in $(seq 1 24); do
  if out="$(apply_source good 'team=web')"; then accepted=1; break; fi
  case "${out}" in
    *"connection refused"*|*"no endpoints available"*|*"failed to call webhook"*|*"context deadline"*)
      sleep 5 ;;                       # webhook not ready yet — retry
    *) fail "valid selector was rejected: ${out}" ;;
  esac
done
[[ "${accepted}" == "1" ]] || fail "valid selector never got through (webhook never became ready)"
pass "valid selector accepted"

# --- 6. negative case: an invalid selector is rejected by our webhook --------
log "Applying a source with an INVALID selector (expect: denied by webhook)"
if out="$(apply_source bad 'a=b=c')"; then
  kubectl delete configmap bad -n smoke-app >/dev/null 2>&1 || true
  fail "invalid selector was accepted — webhook is not enforcing"
fi
grep -qiE 'invalid .*selector' <<<"${out}" \
  || fail "request was denied, but not by our selector webhook: ${out}"
pass "invalid selector rejected: ${out}"

log "Webhook smoke test succeeded"
