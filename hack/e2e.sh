#!/usr/bin/env bash
# khook E2E smoke test against a local k3d cluster.
#
# What it does:
#   1. builds the khook binary
#   2. creates a throwaway k3d cluster (isolated kubeconfig)
#   3. khook validate + plan --offline on every example spec
#   4. cluster-aware plan predicts install and plan --diff renders the new
#      objects, then khook apply examples/simple.yaml, asserts the resources exist
#   5. cluster-aware plan predicts upgrade and plan --diff reports no changes,
#      then re-applies the same spec to assert idempotency (helm upgrade path)
#   6. khook apply hack/testdata/e2e-ops.yaml (wait / rollout / delete / job coverage)
#
# Usage:
#   ./hack/e2e.sh                 # full run, cluster deleted at the end
#   KEEP_CLUSTER=1 ./hack/e2e.sh  # keep the cluster for debugging
#   CLUSTER_NAME=x ./hack/e2e.sh  # custom cluster name
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-khook-e2e}"
KEEP_CLUSTER="${KEEP_CLUSTER:-0}"
KHOOK="${REPO_ROOT}/bin/khook"
KUBECONFIG_FILE="$(mktemp -t khook-e2e-kubeconfig.XXXXXX)"

INGRESS_NS="e2e-ingress"
DEMO_NS="e2e-demo"
OPS_NS="e2e-ops"

log()  { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
fail() { printf '\033[1;31mFAIL: %s\033[0m\n' "$*" >&2; exit 1; }

k() { kubectl --kubeconfig "${KUBECONFIG_FILE}" "$@"; }

cleanup() {
  local code=$?
  if [[ "${KEEP_CLUSTER}" == "1" ]]; then
    log "keeping cluster ${CLUSTER_NAME} (kubeconfig: ${KUBECONFIG_FILE})"
  else
    log "deleting cluster ${CLUSTER_NAME}"
    k3d cluster delete "${CLUSTER_NAME}" >/dev/null 2>&1 || true
    rm -f "${KUBECONFIG_FILE}"
  fi
  if [[ ${code} -eq 0 ]]; then
    printf '\n\033[1;32mE2E PASSED\033[0m\n'
  else
    printf '\n\033[1;31mE2E FAILED (exit %d)\033[0m\n' "${code}"
  fi
  exit "${code}"
}
trap cleanup EXIT

command -v k3d >/dev/null     || fail "k3d is required"
command -v kubectl >/dev/null || fail "kubectl is required"

log "building khook"
(cd "${REPO_ROOT}" && go build -o "${KHOOK}" ./cmd/khook)

log "creating k3d cluster ${CLUSTER_NAME}"
k3d cluster delete "${CLUSTER_NAME}" >/dev/null 2>&1 || true
k3d cluster create "${CLUSTER_NAME}" \
  --kubeconfig-update-default=false \
  --kubeconfig-switch-context=false \
  --wait --timeout 120s >/dev/null
k3d kubeconfig get "${CLUSTER_NAME}" > "${KUBECONFIG_FILE}"
k wait --for=condition=Ready nodes --all --timeout=120s >/dev/null

# --- validate / plan --offline on every example (no cluster access) ---------
log "khook validate + plan --offline on all examples"
EXAMPLE_VARS=(
  --set NAMESPACE_NAME_TO_CREATE="${DEMO_NS}"
  --set NAMESPACE_NAME_FOR_INGRESS="${INGRESS_NS}"
  --set NAMESPACE=e2e-vars --set APP_NAME=e2e-app
)
for spec in "${REPO_ROOT}"/examples/*.yaml; do
  [[ "${spec}" == *lambda* ]] && continue
  "${KHOOK}" validate -f "${spec}" "${EXAMPLE_VARS[@]}" >/dev/null
  "${KHOOK}" plan --offline -f "${spec}" "${EXAMPLE_VARS[@]}" >/dev/null
done

# validation failures must exit 2
set +e
"${KHOOK}" validate -f "${REPO_ROOT}/examples/simple.yaml" >/dev/null 2>&1
rc=$?
set -e
[[ ${rc} -eq 2 ]] || fail "validate with missing variables should exit 2, got ${rc}"

# --- apply examples/simple.yaml ---------------------------------------------
simple_spec() {
  local cmd="$1"
  shift
  "${KHOOK}" "${cmd}" \
    --kubeconfig "${KUBECONFIG_FILE}" \
    -f "${REPO_ROOT}/examples/simple.yaml" \
    --set NAMESPACE_NAME_TO_CREATE="${DEMO_NS}" \
    --set NAMESPACE_NAME_FOR_INGRESS="${INGRESS_NS}" \
    "$@"
}
apply_simple() { simple_spec apply; }

log "khook plan (cluster-aware) predicts install on a fresh cluster"
plan_out="$(simple_spec plan)"
grep -q "plan: install" <<<"${plan_out}" || fail "cluster-aware plan should predict a helm install, got: ${plan_out}"
k get namespace "${DEMO_NS}" >/dev/null 2>&1 && fail "plan must not mutate the cluster (namespace ${DEMO_NS} exists)"

log "khook plan --diff on a fresh cluster renders the objects, mutates nothing"
diff_out="$(simple_spec plan --diff)"
grep -q "+++ planned/namespace/${DEMO_NS}" <<<"${diff_out}" || fail "plan --diff should show the namespace to create"
grep -q "+++ planned/release ingress-nginx (ingress-nginx@4.8.3)" <<<"${diff_out}" || fail "plan --diff should render the chart"
grep -q "+kind: Deployment" <<<"${diff_out}" || fail "plan --diff should show rendered chart objects as additions"
k get namespace "${DEMO_NS}" >/dev/null 2>&1 && fail "plan --diff must not mutate the cluster (namespace ${DEMO_NS} exists)"

log "khook apply examples/simple.yaml (first run: install)"
apply_simple

k get namespace "${DEMO_NS}" >/dev/null    || fail "namespace ${DEMO_NS} was not created"
k get namespace "${INGRESS_NS}" >/dev/null || fail "namespace ${INGRESS_NS} was not created (helm createNamespace)"

log "waiting for ingress-nginx to become Available"
k -n "${INGRESS_NS}" wait --for=condition=Available deployment/ingress-nginx-controller --timeout=300s >/dev/null

svc_type="$(k -n "${INGRESS_NS}" get svc ingress-nginx-controller -o jsonpath='{.spec.type}')"
[[ "${svc_type}" == "ClusterIP" ]] || fail "inline helm values not applied: service type ${svc_type}, want ClusterIP"

revision="$(k -n "${INGRESS_NS}" get secret -l owner=helm,name=ingress-nginx \
  -o jsonpath='{.items[*].metadata.labels.version}' | tr ' ' '\n' | sort -n | tail -1)"
[[ "${revision}" == "1" ]] || fail "expected helm revision 1 after install, got '${revision}'"

log "khook plan (cluster-aware) predicts upgrade after install"
plan_out="$(simple_spec plan)"
grep -q "plan: upgrade" <<<"${plan_out}" || fail "cluster-aware plan should predict a helm upgrade, got: ${plan_out}"

log "khook plan --diff after install reports no changes"
diff_out="$(simple_spec plan --diff)"
grep -q "diff: no changes" <<<"${diff_out}" || fail "plan --diff on an unchanged spec should report no changes, got: ${diff_out}"
grep -q "diff: unavailable" <<<"${diff_out}" && fail "plan --diff should assess every step, got: ${diff_out}"

log "khook apply examples/simple.yaml (second run: idempotent re-apply, --output json)"
json_out="$(simple_spec apply --output json)"
grep -q '"status": "ok"' <<<"${json_out}" || fail "apply --output json should report status ok, got: ${json_out}"
grep -q '"name": "ingress-nginx"' <<<"${json_out}" || fail "apply --output json should list per-step results, got: ${json_out}"

revision="$(k -n "${INGRESS_NS}" get secret -l owner=helm,name=ingress-nginx \
  -o jsonpath='{.items[*].metadata.labels.version}' | tr ' ' '\n' | sort -n | tail -1)"
[[ "${revision}" == "2" ]] || fail "expected helm revision 2 after re-apply (upgrade), got '${revision}'"

status="$(k -n "${INGRESS_NS}" get secret "sh.helm.release.v1.ingress-nginx.v${revision}" \
  -o jsonpath='{.metadata.labels.status}')"
[[ "${status}" == "deployed" ]] || fail "helm release status after re-apply: ${status}, want deployed"

# --- wait / rollout / delete / job / when coverage ----------------------------
log "khook apply hack/testdata/e2e-ops.yaml (wait/rollout/delete/job/when)"
"${KHOOK}" apply \
  --kubeconfig "${KUBECONFIG_FILE}" \
  -f "${REPO_ROOT}/hack/testdata/e2e-ops.yaml" \
  --set OPS_NAMESPACE="${OPS_NS}"

succeeded="$(k -n "${OPS_NS}" get job hello-job -o jsonpath='{.status.succeeded}')"
[[ "${succeeded}" == "1" ]] || fail "job hello-job should have succeeded, got '${succeeded}'"
managed="$(k -n "${OPS_NS}" get job hello-job -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')"
[[ "${managed}" == "khook" ]] || fail "job hello-job should carry the managed-by label, got '${managed}'"
k -n "${OPS_NS}" get configmap doomed >/dev/null 2>&1 && fail "configmap doomed should have been deleted"
k -n "${OPS_NS}" get deployment echo >/dev/null 2>&1 && fail "deployment echo should have been deleted"
k -n "${OPS_NS}" get configmap conditional-extra >/dev/null 2>&1 && fail "configmap conditional-extra should not exist (when: is false)"
k -n "${OPS_NS}" get configmap conditional-after >/dev/null || fail "configmap conditional-after missing (excluded step must satisfy needs)"

# --- failure semantics: a failing step must exit 1 and skip dependents -------
log "asserting failure exit code"
set +e
"${KHOOK}" apply --kubeconfig "${KUBECONFIG_FILE}" \
  -f /dev/stdin --set _unused=1 <<'EOF' >/dev/null 2>&1
apiVersion: khook.dvrkn.com/v1
kind: Khook
metadata:
  name: must-fail
defaults:
  timeout: 20s
steps:
  - name: bad-wait
    wait:
      for: condition=Ready
      on: pods
      namespace: does-not-exist-ns
      selector: app=nothing
EOF
rc=$?
set -e
[[ ${rc} -eq 1 ]] || fail "failing apply should exit 1, got ${rc}"
