#!/usr/bin/env bash
# khook E2E smoke test against a local k3d cluster.
#
# What it does:
#   1. builds the khook binary
#   2. creates a throwaway k3d cluster (isolated kubeconfig)
#   3. khook validate + plan --offline + graph (Mermaid and DOT) on every
#      example spec, with output assertions for graph
#   4. cluster-aware plan predicts install and plan --diff renders the new
#      objects, then khook apply examples/simple.yaml, asserts the resources exist
#   5. cluster-aware plan predicts upgrade and plan --diff reports no changes,
#      then re-applies the same spec to assert idempotency (helm upgrade path)
#   6. khook apply hack/testdata/e2e-ops.yaml (wait / rollout / delete / job coverage)
#   7. helm depth: installs a chart from a local path, then uninstalls the
#      release via delete.release and asserts the re-run is a no-op
#   8. kubectl depth: apply.waitFor, jsonpath wait, patch (strategic + json),
#      kustomize source — then asserts the re-run is idempotent and
#      plan --diff reports no changes
#   9. run-state record (state:): a failed run journals to the record Secret,
#      an identical re-run resumes past the completed step (proven by an
#      out-of-band deletion staying deleted), a changed variable forces a
#      fresh run, khook status reads the record, and a foreign same-name
#      Secret is refused
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

# --- validate / plan --offline / graph on every example (no cluster access) --
log "khook validate + plan --offline + graph on all examples"
EXAMPLE_VARS=(
  --set NAMESPACE_NAME_TO_CREATE="${DEMO_NS}"
  --set NAMESPACE_NAME_FOR_INGRESS="${INGRESS_NS}"
  --set NAMESPACE=e2e-vars --set APP_NAME=e2e-app
  --set AWS_ACCOUNT_ID=123456789012 --set ECR_TOKEN=e2e-token
  --set CHART_USER=e2e-user --set CHART_PASS=e2e-pass
)
for spec in "${REPO_ROOT}"/examples/*.yaml; do
  [[ "${spec}" == *lambda* ]] && continue
  "${KHOOK}" validate -f "${spec}" "${EXAMPLE_VARS[@]}" >/dev/null
  "${KHOOK}" plan --offline -f "${spec}" "${EXAMPLE_VARS[@]}" >/dev/null
  "${KHOOK}" graph -f "${spec}" "${EXAMPLE_VARS[@]}" >/dev/null
  "${KHOOK}" graph --format dot -f "${spec}" "${EXAMPLE_VARS[@]}" >/dev/null
done

log "khook graph emits the DAG as Mermaid and DOT"
graph_out="$("${KHOOK}" graph -f "${REPO_ROOT}/examples/simple.yaml" \
  --set NAMESPACE_NAME_TO_CREATE="${DEMO_NS}" --set NAMESPACE_NAME_FOR_INGRESS="${INGRESS_NS}")"
grep -q "flowchart TD" <<<"${graph_out}" || fail "graph should emit a Mermaid flowchart, got: ${graph_out}"
grep -q 'n0\["create-namespace (apply)"\]' <<<"${graph_out}" || fail "graph should label nodes with name and type, got: ${graph_out}"
grep -q "n0 --> n1" <<<"${graph_out}" || fail "graph should emit the needs edge, got: ${graph_out}"

graph_out="$("${KHOOK}" graph --format dot -f "${REPO_ROOT}/examples/simple.yaml" \
  --set NAMESPACE_NAME_TO_CREATE="${DEMO_NS}" --set NAMESPACE_NAME_FOR_INGRESS="${INGRESS_NS}")"
grep -q 'digraph "local-demo"' <<<"${graph_out}" || fail "graph --format dot should emit a digraph, got: ${graph_out}"
grep -q '"create-namespace" -> "ingress-nginx";' <<<"${graph_out}" || fail "graph --format dot should emit the needs edge, got: ${graph_out}"

# a step excluded by when: is drawn as skipped
graph_out="$("${KHOOK}" graph -f "${REPO_ROOT}/hack/testdata/e2e-ops.yaml" --set OPS_NAMESPACE="${OPS_NS}")"
grep -q '"conditional-extra (apply, skipped)"\]:::skipped' <<<"${graph_out}" || fail "graph should mark the when:-excluded step skipped, got: ${graph_out}"

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

# --- helm depth: local chart path + release uninstall ------------------------
HELM_DEPTH_NS="e2e-helm-depth"

log "khook apply hack/testdata/e2e-helm-depth.yaml (local chart path)"
"${KHOOK}" apply --kubeconfig "${KUBECONFIG_FILE}" \
  -f "${REPO_ROOT}/hack/testdata/e2e-helm-depth.yaml" \
  --set CHART_PATH="${REPO_ROOT}/hack/testdata/e2e-chart" \
  --set HELM_NS="${HELM_DEPTH_NS}"
greeting="$(k -n "${HELM_DEPTH_NS}" get configmap e2e-local-cm -o jsonpath='{.data.greeting}')"
[[ "${greeting}" == "from-khook" ]] || fail "local chart values not applied, got '${greeting}'"

log "delete.release uninstalls the release; the re-run is a no-op"
uninstall_spec() {
  "${KHOOK}" "$1" --kubeconfig "${KUBECONFIG_FILE}" \
    -f "${REPO_ROOT}/hack/testdata/e2e-helm-uninstall.yaml" \
    --set HELM_NS="${HELM_DEPTH_NS}"
}
plan_out="$(uninstall_spec plan)"
grep -q 'uninstalls release "e2e-local"' <<<"${plan_out}" || fail "plan should predict the uninstall, got: ${plan_out}"
uninstall_spec apply
k -n "${HELM_DEPTH_NS}" get configmap e2e-local-cm >/dev/null 2>&1 && fail "configmap e2e-local-cm should be gone after uninstall"
uninstall_spec apply
plan_out="$(uninstall_spec plan)"
grep -q 'already absent' <<<"${plan_out}" || fail "plan after uninstall should report already absent, got: ${plan_out}"

# --- kubectl depth: waitFor, jsonpath wait, patch, kustomize -----------------
KD_NS="e2e-kubectl-depth"

kd_spec() {
  local cmd="$1"
  shift
  "${KHOOK}" "${cmd}" --kubeconfig "${KUBECONFIG_FILE}" \
    -f "${REPO_ROOT}/hack/testdata/e2e-kubectl-depth.yaml" \
    --set KD_NAMESPACE="${KD_NS}" \
    --set KUSTOMIZE_DIR="${REPO_ROOT}/hack/testdata/e2e-kustomize" \
    "$@"
}

log "khook plan reports the patch target as not-yet-existing"
plan_out="$(kd_spec plan)"
grep -q "must exist by the time this step runs" <<<"${plan_out}" || fail "plan should flag the missing patch target, got: ${plan_out}"

log "khook apply hack/testdata/e2e-kubectl-depth.yaml (waitFor/jsonpath/patch/kustomize)"
kd_spec apply

available="$(k -n "${KD_NS}" get deployment depth-echo -o jsonpath='{.status.availableReplicas}')"
[[ "${available}" == "1" ]] || fail "waitFor: condition=Available passed but availableReplicas is '${available}'"
annot="$(k -n "${KD_NS}" get deployment depth-echo -o jsonpath='{.metadata.annotations.khook\.io/patched}')"
[[ "${annot}" == "yes" ]] || fail "strategic patch did not land, got '${annot}'"
env_val="$(k -n "${KD_NS}" get configmap prod-e2e-settings -o jsonpath='{.data.env}')"
[[ "${env_val}" == "prod" ]] || fail "kustomize namePrefix/patch not applied, got env '${env_val}'"
color="$(k -n "${KD_NS}" get configmap prod-e2e-settings -o jsonpath='{.data.color}')"
[[ "${color}" == "blue" ]] || fail "kustomize base data lost, got color '${color}'"
added="$(k -n "${KD_NS}" get configmap prod-e2e-settings -o jsonpath='{.data.added}')"
[[ "${added}" == "yes" ]] || fail "json patch did not land, got '${added}'"

log "kubectl-depth re-run is idempotent; plan --diff reports no changes"
kd_spec apply
diff_out="$(kd_spec plan --diff)"
grep -q "diff: no changes" <<<"${diff_out}" || fail "plan --diff on the applied kubectl-depth spec should report no changes, got: ${diff_out}"
grep -q "diff: unavailable" <<<"${diff_out}" && fail "plan --diff should assess every kubectl-depth step, got: ${diff_out}"
grep -q "already holds" <<<"${diff_out}" || fail "plan should report the jsonpath wait as already met, got: ${diff_out}"

# --- run-state record: fail, resume, hash mismatch, ownership guard ----------
STATE_NS="e2e-state"
STATE_SECRET="khook-state-e2e-state"

state_spec() {
  local cmd="$1"
  shift
  "${KHOOK}" "${cmd}" --kubeconfig "${KUBECONFIG_FILE}" \
    -f "${REPO_ROOT}/hack/testdata/e2e-state.yaml" \
    --set STATE_NS="${STATE_NS}" \
    "$@"
}

state_record() {
  k -n "${STATE_NS}" get secret "${STATE_SECRET}" -o jsonpath='{.data.record\.json}' | base64 -d
}

log "state: status before any apply reports no record (exit 0)"
k create namespace "${STATE_NS}" >/dev/null
status_out="$(state_spec status -o json)"
grep -q '"found": false' <<<"${status_out}" || fail "status before any run should report found:false, got: ${status_out}"

log "state: first run fails at step-b and journals the outcome"
set +e
state_spec apply >/dev/null 2>&1
rc=$?
set -e
[[ ${rc} -eq 1 ]] || fail "state run with the gate absent should exit 1, got ${rc}"

managed="$(k -n "${STATE_NS}" get secret "${STATE_SECRET}" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}')"
[[ "${managed}" == "khook" ]] || fail "state secret should carry the managed-by label, got '${managed}'"
record="$(state_record)"
grep -q '"runStatus":"failed"' <<<"${record}" || fail "record should mark the run failed, got: ${record}"
grep -q '"name":"step-a","type":"apply","status":"ok"' <<<"${record}" || fail "record should mark step-a ok, got: ${record}"
grep -q '"name":"step-b","type":"wait","status":"failed"' <<<"${record}" || fail "record should mark step-b failed, got: ${record}"
grep -q '"name":"step-c","type":"apply","status":"skipped"' <<<"${record}" || fail "record should mark step-c skipped, got: ${record}"

status_out="$(state_spec status)"
grep -q "run:     failed" <<<"${status_out}" || fail "status should report the failed run, got: ${status_out}"
grep -q "unchanged since this run" <<<"${status_out}" || fail "status should report the spec unchanged, got: ${status_out}"

log "state: identical re-run resumes past step-a (journal wins over the cluster)"
# Delete step-a's output out-of-band: the resume must skip step-a anyway —
# this is the documented staleness tradeoff, asserted here on purpose.
k -n "${STATE_NS}" delete configmap state-marker >/dev/null
# Open the gate so step-b succeeds this time.
k -n "${STATE_NS}" create configmap state-gate --from-literal=ready=yes >/dev/null
k -n "${STATE_NS}" label configmap state-gate khook-e2e=state-gate >/dev/null

apply_out="$(state_spec apply)"
grep -q "succeeded in a previous run" <<<"${apply_out}" || fail "re-run should resume-skip step-a, got: ${apply_out}"
k -n "${STATE_NS}" get configmap state-marker >/dev/null 2>&1 && fail "state-marker exists — step-a ran despite the record (resume did not happen)"
k -n "${STATE_NS}" get configmap state-final >/dev/null || fail "step-c did not run on the resumed attempt"
record="$(state_record)"
grep -q '"runStatus":"ok"' <<<"${record}" || fail "record should mark the resumed run ok, got: ${record}"
grep -q '"name":"step-a","type":"apply","status":"ok"' <<<"${record}" || fail "record must keep step-a ok after the resume, got: ${record}"

log "state: a changed variable (hash mismatch) forces a fresh run"
apply_out="$(state_spec apply --set MARKER=changed)"
grep -q "succeeded in a previous run" <<<"${apply_out}" && fail "hash mismatch must not resume, got: ${apply_out}"
made_by="$(k -n "${STATE_NS}" get configmap state-marker -o jsonpath='{.data.made-by}')"
[[ "${made_by}" == "changed" ]] || fail "fresh run should recreate state-marker with the new value, got '${made_by}'"

log "state: a foreign same-name secret is refused"
FOREIGN_NS="e2e-state-foreign"
k create namespace "${FOREIGN_NS}" >/dev/null
k -n "${FOREIGN_NS}" create secret generic "${STATE_SECRET}" --from-literal=x=y >/dev/null
set +e
foreign_out="$(state_spec apply --set STATE_NS="${FOREIGN_NS}" 2>&1)"
rc=$?
set -e
[[ ${rc} -eq 1 ]] || fail "apply against a foreign state secret should exit 1, got ${rc}"
grep -q "not managed by khook" <<<"${foreign_out}" || fail "error should name the ownership problem, got: ${foreign_out}"

# --- failure semantics: a failing step must exit 1 and skip dependents -------
log "asserting failure exit code"
set +e
"${KHOOK}" apply --kubeconfig "${KUBECONFIG_FILE}" \
  -f /dev/stdin --set _unused=1 <<'EOF' >/dev/null 2>&1
apiVersion: khook.io/v1
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
