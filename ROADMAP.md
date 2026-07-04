# Roadmap — khook (Cluster Bootstrapper)

> **Vision:** declarative bootstrap for Kubernetes. A single static binary that embeds the
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

## Phase 1 — Delivery & integrations (meet users where clusters are created)

*Goal: trivially runnable from every place a cluster gets created.*

- [ ] **Release channels beyond the binaries**: a Homebrew tap and a multi-arch
      container image (GHCR). Tagged `v*` release binaries already ship via
      `.github/workflows/release.yml`.
- [ ] **Submit the v1 schema to the [JSON Schema Store](https://github.com/SchemaStore/schemastore)**
      with a `fileMatch` pattern — which means picking a spec filename
      convention (`khook.yaml` / `*.khook.yaml`) first. The schema itself is
      already served live at its `$id`, `https://khook.io/schema/v1/khook.json`.
- [ ] **Broader auth**: GKE and AKS token plugins (behind build tags to keep the
      binary lean), generic OIDC/exec-plugin support, in-cluster ServiceAccount
      auth (run as a Job inside the cluster it bootstraps).
- [ ] **GitHub Action**: `uses: dvrkn/khook-action` — validate on PR, apply on merge.

## Phase 2 — Observability & scale (nice-to-have, demand-driven)

- [ ] OpenTelemetry traces (one span per step — DAG visualizes for free in any
      trace viewer)
- [ ] Prometheus-format run metrics / CloudWatch EMF in Lambda mode
- [ ] Webhook/SNS notification on completion or failure

---

## Maybe someday

- **Raw-HTTP Kubernetes/Helm client (drop client-go + Helm SDK)** — measured
  2026-07-03: the release binary is ~61 MB, and ~40 MB of that (two thirds) is
  the typed `k8s.io/client-go` clientset, all of `k8s.io/api`, and the Helm
  SDK's transitive graph. A probe binary keeping everything else khook needs
  (cobra, CEL, kustomize, sprig/`text/template`, YAML, JSON Schema, TLS/HTTP)
  came out at 20 MB (16 MB without kustomize); a raw-HTTP implementation would
  realistically land at ~22–24 MB. The saving is structural: the typed
  clientset links every resource type of every API group, and
  `text/template` (Helm's render engine) calls `reflect.MethodByName`, which
  disables linker method pruning — so none of it dead-code-eliminates. The
  kubectl-ish half is genuinely simple over raw HTTP (server-side apply,
  watch streaming, kubeconfig + exec-credential auth); the blocker is Helm:
  it would mean reimplementing chart dependency resolution, hooks,
  release-storage records, upgrade/rollback semantics — weeks of work plus a
  permanent compatibility-tracking burden. And it is all-or-nothing: going
  raw-HTTP only for kubectl-ish steps saves almost nothing while the Helm SDK
  still links the typed clientset. Revisit only if binary size becomes a real
  adoption problem (e.g. Lambda package limits). Build-flag slimming already
  shipped (`make build`, 97 → 61 MB); overlay-stubbing Helm's WASM/SQL paths
  (~3 MB more) and UPX were evaluated and rejected — see AGENTS.md.

## Explicit non-goals

- **Not a GitOps engine.** No watch loops, no drift reconciliation — install Argo/Flux and hand off.
- **No `apply.prune`.** Pruning is drift reconciliation (see above): Argo/Flux own it, `delete:` owns explicit absence. kubectl's own `--prune` is quasi-deprecated and its ApplySet successor still alpha — nothing worth freezing into the spec.
- **Not a package manager.** No chart authoring, no chart repository hosting.
- **Not a cluster provisioner.** Terraform/eksctl/CAPI create the cluster; we start where they stop.
- **Not a data bus.** Steps do not pass values through khook (no `${outputs.*}`, no job output capture): late-bound data would make consumer steps unplannable and re-substitution would break the load-time model. In-cluster, a `job` writes a Secret/ConfigMap and consumers reference it *by name* (`secretKeyRef`, `existingSecret`-style chart values) — the name is static and plannable, the value flows through the API server. Out-of-cluster values are variables (env-first); cross-resource wiring after bootstrap belongs to the operators khook hands off to.
- **Not a secrets fetcher.** khook consumes variables (`KHOOK_VAR_*` / `KHOOK_SECRET_*`, `--set`, `--var-file`); it never reaches into SSM/Secrets Manager/Vault itself — whatever runs khook (shell, CI, Terraform, the Lambda invoker) resolves values, since it always has better credential context. Keeps cloud SDKs out of the binary. In-cluster, the External Secrets pattern owns secrets end-to-end.
- **No templating language over the document** (no embedded Go templates/Jinja — a `{{ }}` pass would fight the template syntax specs embed as data in Argo/Helm manifests). The sanctioned form is sprig pipelines *inside* `${NAME|...}` references (hermetic function set; see `docs/dsl.md`): only the author-written pipeline is templated, values stay data, the document is never template-parsed. Complexity beyond that belongs in Helm values or a `job` op.

## Open questions

- **Multi-cluster in one spec**: out of scope, or a `targets:` concept later?
- **Lambda payload vs S3**: specs can outgrow the 256 KB invoke limit — accept an
  S3 URI as the spec source?
