# Roadmap

Future work only. What exists is documented in [`docs/dsl.md`](docs/dsl.md),
[`docs/cli.md`](docs/cli.md), and the [README](README.md); shipped items move
there.

**Vision:** a single static binary, with the Kubernetes and Helm SDKs
embedded, that takes a freshly created cluster (Terraform, eksctl, kind, ...)
from "API server answers" to "workloads can be deployed": CNI, secrets
management, GitOps controller, then handoff. No `kubectl`, `helm`, or shell
scripts.

## Design principles

1. **One binary, no runtime dependencies.** Everything through SDKs; never
   shell out.
2. **Declarative and idempotent.** Running a spec twice is safe; khook decides
   install vs upgrade vs skip.
3. **Bootstrap, then hand off.** Long-term reconciliation belongs to Argo CD
   or Flux. khook installs the GitOps controller; it does not replace it.
4. **Fail loud, resume cheap.** Errors carry operation context; re-runs skip
   finished work.
5. **The spec is the API.** Versioned (`khook.io/v1`), schema-validated,
   editor-friendly.

---

## Phase 1: delivery and integrations

*Goal: easy to run wherever clusters are created.*

- [ ] **More release channels**: a Homebrew tap and a multi-arch container
      image (GHCR). Tagged `v*` binaries already ship via
      `.github/workflows/release.yml`.
- [ ] **Submit the v1 schema to [JSON Schema Store](https://github.com/SchemaStore/schemastore)**
      with a `fileMatch` pattern. Needs a spec filename convention first
      (`khook.yaml` / `*.khook.yaml`). The schema is already served at its
      `$id`, `https://khook.io/schema/v1/khook.json`.
- [ ] **Broader auth**: GKE and AKS token plugins (behind build tags to keep
      the binary small), generic OIDC/exec plugins, in-cluster
      ServiceAccount auth (running as a Job in the cluster it bootstraps).
- [ ] **GitHub Action**: `uses: dvrkn/khook-action`; validate on PR, apply on
      merge.

## Phase 2: observability (demand-driven)

- [ ] OpenTelemetry traces, one span per step
- [ ] Prometheus-format run metrics / CloudWatch EMF in Lambda mode
- [ ] Webhook/SNS notification on completion or failure

---

## Maybe someday

- **Raw-HTTP Kubernetes/Helm client (drop client-go and the Helm SDK).**
  Measured 2026-07-03: of the ~61 MB release binary, ~40 MB is the typed
  `k8s.io/client-go` clientset, all of `k8s.io/api`, and the Helm SDK's
  dependencies. A probe binary with everything else khook uses (cobra, CEL,
  kustomize, sprig/`text/template`, YAML, JSON Schema, TLS/HTTP) was 20 MB
  (16 MB without kustomize); a raw-HTTP implementation would likely be
  ~22–24 MB. The size is structural: the typed clientset links every type of
  every API group, and `text/template` (Helm's renderer) calls
  `reflect.MethodByName`, which disables linker method pruning.
  The kubectl-like half is simple over raw HTTP (server-side apply, watch,
  kubeconfig and exec-credential auth). Helm is the blocker: chart dependency
  resolution, hooks, release storage, and upgrade/rollback semantics would
  need reimplementing and then tracking upstream. It is also all-or-nothing:
  replacing only the kubectl-like steps saves almost nothing while the Helm
  SDK still links the typed clientset. Revisit only if binary size blocks
  adoption (e.g. Lambda package limits). Build-flag slimming already shipped
  (`make build`, 97 → 61 MB); overlay-stubbing Helm's WASM/SQL paths (~3 MB)
  and UPX were rejected (see AGENTS.md).

## Non-goals

- **Not a GitOps engine.** No watch loops or drift reconciliation; install
  Argo CD/Flux and hand off.
- **No `apply.prune`.** Pruning is drift reconciliation, which Argo/Flux own;
  `delete:` covers explicit removal. kubectl's `--prune` is effectively
  deprecated and its ApplySet successor is still alpha.
- **Not a package manager.** No chart authoring or chart hosting.
- **Not a cluster provisioner.** Terraform/eksctl/CAPI create the cluster.
- **Not a data bus.** Steps don't pass values through khook (no
  `${outputs.*}`, no job output capture): late-bound values would make
  consumer steps unplannable and break load-time substitution. In-cluster, a
  `job` writes a Secret/ConfigMap and consumers reference it by name
  (`secretKeyRef`, `existingSecret`-style chart values). Out-of-cluster values
  are variables. Wiring after bootstrap belongs to the operators khook hands
  off to.
- **Not a secrets fetcher.** khook reads variables (`KHOOK_VAR_*` /
  `KHOOK_SECRET_*`, `--set`, `--var-file`) and never calls SSM, Secrets
  Manager, or Vault. Whatever runs khook (shell, CI, Terraform, a Lambda
  invoker) has better credential context and resolves the values; this also
  keeps cloud SDKs out of the binary. In-cluster, External Secrets owns
  secrets.
- **No templating over the document** (no Go templates or Jinja): a `{{ }}`
  pass would conflict with template syntax that specs embed as data in
  Argo/Helm manifests. The one exception is sprig pipelines inside
  `${NAME|...}` (hermetic functions, see `docs/dsl.md`): only the pipeline is
  templated, values stay data. Anything more complex belongs in Helm values or
  a `job` step.

## Open questions

- **Multi-cluster in one spec**: out of scope, or a `targets:` concept later?
- **Lambda payload vs S3**: specs can exceed the 256 KB invoke limit; accept
  an S3 URI as the spec source?
