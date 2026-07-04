# Roadmap — khook (Cluster Bootstrapper)

> **Vision:** "cloud-init for Kubernetes." A single static binary that embeds the
> Kubernetes and Helm SDKs and turns a declarative YAML spec into a fully
> initialized cluster — no `kubectl`, no `helm`, no shell scripts, no external
> dependencies. Point it at a freshly created cluster (Terraform, eksctl, kind,
> anything) and it brings the cluster from "API server answers" to "workloads can
> be deployed": CNI, secrets management, GitOps controller, then hands off.

This file holds **future work only**. What exists is documented in
[`docs/dsl.md`](docs/dsl.md) (spec), [`docs/cli.md`](docs/cli.md) (CLI), and
the [README](README.md); when an item here ships, it moves there.

## Design principles

1. **One binary, zero runtime deps.** Everything through SDKs, never shelling out.
2. **Declarative and idempotent.** Running the same spec twice is always safe; the
   spec describes desired state, the tool figures out install-vs-upgrade / skip.
3. **Bootstrap, then hand off.** We initialize; long-term reconciliation belongs to
   ArgoCD/Flux. We are the thing that installs the GitOps controller, not a
   replacement for it.
4. **Fail loud, resume cheap.** Clear errors with operation context; re-running
   skips what's already done.
5. **Spec is the API.** Schema-validated, versioned (`khook.dvrkn.com/v1`),
   editor-autocomplete friendly.

---

## Phase 1 — Operator UX (make it pleasant to run)

*Goal: the day-2 experience of running bootstraps interactively.*

- [ ] **`plan --diff`**: `plan` already predicts install/upgrade/skip from
      cluster reads; add rendered object diffs on top (kubectl server-side
      dry-run + Helm template diff, kubectl-diff-style output).
- [ ] **Live progress output**: interactive per-step status lines
      (pending → running → ok/failed/skipped) instead of raw log lines;
      `--output json` for the final results in CI.
- [ ] **Spec composability**: multiple `-f` files / a directory of specs merged in
      order; `needs` across files.

## Phase 2 — DSL v2 (make the spec expressive enough for real clusters)

*Goal: cover the real bootstrap cases (`examples/real-case.yaml` and beyond)
without escape hatches.*

- [ ] **Conditionals**: `when:` expression on steps (variable-based, e.g.
      `when: ${ENABLE_ARGOCD} == "true"`), so one spec serves many environments.
- [ ] **Richer variable sources**: files and cloud secrets (AWS SSM / Secrets
      Manager) — pluggable resolver chain.
- [ ] **Helm depth**: OCI registry charts (`oci://`), local chart paths/tarballs,
      `- url:` in `valuesFrom`, `reuseValues`, uninstall action, private repo
      auth (basic + ECR).
- [ ] **Kubectl depth**: prune/patch actions, `waitFor` shorthand on apply (apply
      + wait in one step), kustomize source (`sigs.k8s.io/kustomize` comes in
      transitively with the Helm SDK anyway), `wait.for: jsonpath=...`.
- [ ] **New step types** (see the coverage matrix in `docs/dsl.md`):
      - `job`: run a container to completion in-cluster (escape hatch for
        anything we don't model — replaces "shell scripts" in the cloud-init analogy)
      - `label:` / `annotate:` (e.g. tagging nodes/namespaces during bootstrap)
      - `scale:` (e.g. `kubectl scale` shorthand for sizing system workloads)
- [ ] **apiVersion `v1` freeze**: publish the JSON schema (raw GitHub URL + JSON
      Schema Store) so editors autocomplete via `# yaml-language-server`.

## Phase 3 — Idempotency, state & resume (make re-runs first-class)

*Goal: a failed bootstrap at step 7/12 is a resume, not a redo.*

- [ ] **Unified skip semantics**: `skipIfInstalled` / `skipIfExists` are per-type
      today → one consistent `skipIf` policy across all step types.
- [ ] **Run state record**: write a ConfigMap/Secret in-cluster (like Helm does)
      recording spec hash + per-step outcome; `apply` detects a prior partial
      run and continues from the failure point.
- [ ] **Change detection**: hash step inputs (chart version + values +
      manifests); unchanged + previously-successful → skip. This is what makes
      "run it on every terraform apply" cheap.
- [ ] **`status` subcommand**: read the state record, show last run, per-step outcomes.
- [ ] **`destroy` (stretch)**: reverse-topological teardown for dev clusters.

## Phase 4 — Delivery & integrations (meet users where clusters are created)

*Goal: trivially runnable from every place a cluster gets created.*

- [ ] **CI**: GitHub Actions — lint (golangci-lint), unit tests, the k3d E2E
      (`hack/e2e.sh`), build matrix (linux/darwin, amd64/arm64).
- [ ] **Release channels**: goreleaser → GitHub Releases (static binaries),
      Homebrew tap, multi-arch container image (GHCR).
- [ ] **Terraform**: re-establish the Lambda path v0 proved (invocation contract
      sketched in `examples/lambda/example-payload.json`) as a small Terraform
      module wrapping the Lambda — no `null_resource`/`local-exec` — plus docs
      for EKS access entries / IAM.
- [ ] **Broader auth**: GKE and AKS token plugins (behind build tags to keep the
      binary lean), generic OIDC/exec-plugin support, in-cluster ServiceAccount
      auth (run as a Job inside the cluster it bootstraps).
- [ ] **GitHub Action**: `uses: dvrkn/khook-action` — validate on PR, apply on merge.

## Phase 5 — Observability & scale (nice-to-have, demand-driven)

- [ ] OpenTelemetry traces (one span per step — DAG visualizes for free in any
      trace viewer)
- [ ] Prometheus-format run metrics / CloudWatch EMF in Lambda mode
- [ ] Webhook/SNS notification on completion or failure
- [ ] `graph` subcommand: emit DAG as Mermaid/DOT for docs and review

---

## Explicit non-goals

- **Not a GitOps engine.** No watch loops, no drift reconciliation — install Argo/Flux and hand off.
- **Not a package manager.** No chart authoring, no chart repository hosting.
- **Not a cluster provisioner.** Terraform/eksctl/CAPI create the cluster; we start where they stop.
- **No templating language in the DSL** (no embedded Go templates/Jinja). Variables + `when:` conditionals only; complexity beyond that belongs in Helm values or a `job` op.

## Suggested order of attack

1. Phase 1's progress output (+ `plan --diff`) — biggest day-to-day UX win
   for the effort.
2. Phase 2 driven by real specs: take `real-case.yaml`, remove every workaround it
   needed, and let that dictate which DSL features land first.
3. Phase 3 before advertising widely — "safe to re-run, resumes on failure" is the
   core promise of a bootstrapper.
4. Phases 4–5 as adoption demands (CI early, though — it is cheap and guards
   everything else).

## Open questions (decide before Phase 2)

- **Multi-cluster in one spec**: out of scope, or a `targets:` concept later?
- **Secrets in specs**: recommend External Secrets pattern only, or support
  first-class secret variable sources (SSM/SM) in Phase 2?
- **Lambda payload vs S3**: specs can outgrow the 256 KB invoke limit — accept an
  S3 URI as the spec source?
