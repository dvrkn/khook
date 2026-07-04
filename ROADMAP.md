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
5. **Spec is the API.** Schema-validated, versioned (`khook.io/v1`),
   editor-autocomplete friendly.

---

## Phase 1 — DSL v2 (make the spec expressive enough for real clusters)

*Goal: cover the real bootstrap cases (`examples/real-case.yaml` and beyond)
without escape hatches.*

- [ ] **apiVersion `v1` freeze**: publish the JSON schema (raw GitHub URL + JSON
      Schema Store) so editors autocomplete via `# yaml-language-server`.

## Phase 2 — Idempotency, state & resume (make re-runs first-class)

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

## Phase 3 — Delivery & integrations (meet users where clusters are created)

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

## Phase 4 — Observability & scale (nice-to-have, demand-driven)

- [ ] OpenTelemetry traces (one span per step — DAG visualizes for free in any
      trace viewer)
- [ ] Prometheus-format run metrics / CloudWatch EMF in Lambda mode
- [ ] Webhook/SNS notification on completion or failure
- [ ] `graph` subcommand: emit DAG as Mermaid/DOT for docs and review

---

## Explicit non-goals

- **Not a GitOps engine.** No watch loops, no drift reconciliation — install Argo/Flux and hand off.
- **No `apply.prune`.** Pruning is drift reconciliation (see above): Argo/Flux own it, `delete:` owns explicit absence. kubectl's own `--prune` is quasi-deprecated and its ApplySet successor still alpha — nothing worth freezing into the spec.
- **Not a package manager.** No chart authoring, no chart repository hosting.
- **Not a cluster provisioner.** Terraform/eksctl/CAPI create the cluster; we start where they stop.
- **Not a data bus.** Steps do not pass values through khook (no `${outputs.*}`, no job output capture): late-bound data would make consumer steps unplannable and re-substitution would break the load-time model. In-cluster, a `job` writes a Secret/ConfigMap and consumers reference it *by name* (`secretKeyRef`, `existingSecret`-style chart values) — the name is static and plannable, the value flows through the API server. Out-of-cluster values are variables (env-first); cross-resource wiring after bootstrap belongs to the operators khook hands off to.
- **Not a secrets fetcher.** khook consumes variables (`KHOOK_VAR_*` / `KHOOK_SECRET_*`, `--set`, `--var-file`); it never reaches into SSM/Secrets Manager/Vault itself — whatever runs khook (shell, CI, Terraform, the Lambda invoker) resolves values, since it always has better credential context. Keeps cloud SDKs out of the binary. In-cluster, the External Secrets pattern owns secrets end-to-end.
- **No templating language over the document** (no embedded Go templates/Jinja — a `{{ }}` pass would fight the template syntax specs embed as data in Argo/Helm manifests). The sanctioned form is sprig pipelines *inside* `${NAME|...}` references (hermetic function set; see `docs/dsl.md`): only the author-written pipeline is templated, values stay data, the document is never template-parsed. Complexity beyond that belongs in Helm values or a `job` op.

## Suggested order of attack

1. Phase 1 driven by real specs: take `real-case.yaml`, remove every workaround it
   needed, and let that dictate which DSL features land first.
2. Phase 2 before advertising widely — "safe to re-run, resumes on failure" is the
   core promise of a bootstrapper.
3. Phases 3–4 as adoption demands (CI early, though — it is cheap and guards
   everything else).

## Open questions (decide before Phase 1)

- **Multi-cluster in one spec**: out of scope, or a `targets:` concept later?
- **Lambda payload vs S3**: specs can outgrow the 256 KB invoke limit — accept an
  S3 URI as the spec source?
