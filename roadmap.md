# Roadmap — khook (Cluster Bootstrapper)

> **Vision:** "cloud-init for Kubernetes." A single static binary that embeds the
> Kubernetes and Helm SDKs and turns a declarative YAML spec into a fully
> initialized cluster — no `kubectl`, no `helm`, no shell scripts, no external
> dependencies. Point it at a freshly created cluster (Terraform, eksctl, kind,
> anything) and it brings the cluster from "API server answers" to "workloads can
> be deployed": CNI, secrets management, GitOps controller, then hands off.

## Starting point

This is a **from-scratch rewrite**. A v0 prototype proved the concept end-to-end;
its history was cut from this repo and is kept as local-only git bundles in the
gitignored `.dev/v0-backup/` (restore with `git clone <bundle>`). What survives
from it in the tree:

- **`examples/*.yaml`** — kept in the tree. They are the de-facto DSL spec and the
  primary design input; `real-case.yaml` is the benchmark a v1 must handle cleanly.

What v0 proved (worth re-implementing):

- YAML DSL (`ClusterBootstrap` kind) with `helm`, `kubectl`, `exec` operation types
- DAG dependency resolution with cycle detection + level-parallel scheduler
- Embedded Helm SDK: install/upgrade detection, repos, values (inline/files/set), atomic/wait
- Kubectl via dynamic client: apply/delete, multi-doc, inline/files/URLs, server-side apply
- Exec ops: rollout restart/status, wait-for-condition, delete-by-selector
- Variable substitution (`${VAR}`)
- Two entrypoints: local CLI (kubeconfig) and AWS Lambda (STS + EKS token)
- JSON Schema generation from the DSL types

Lessons from v0 to fix in the rewrite:

- Naming was inconsistent (repo `khook`, module `cluster-bootstrapper`, binary
  `bootstrap-local`) — settle identity on day one.
- Tests came last and stayed thin — this time the k3d-based E2E lands with the
  first executor, not after.
- `exec` grew into a grab-bag; `wait` deserves to be first-class and the rest
  should be designed, not accreted.
- Retries existed in the schema but were never fully wired — don't ship schema
  fields the engine ignores.

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

## DSL v1 shape (decided)

**Normative spec: `docs/dsl.md`** — full field reference for the v1 core step
types (`helm`, `apply`, `delete`, `wait`, `rollout`) plus a kubectl/helm command
coverage matrix marking what is v1 core vs roadmap. The `examples/` directory is
the spec-by-example and must stay valid against it. Decisions vs the v0 DSL:

- **`kind: Khook`** (was `ClusterBootstrap`) with `apiVersion: khook.dvrkn.com/v1`.
- **Action key implies the type** — no `type:` discriminator. A step has exactly
  one of `helm:`, `apply:`, `delete:`, `wait:`, `rollout:` (schema: oneOf).
- **`steps:` / `needs:`** replace `operations:` / `dependsOn:`.
- **Top-level `defaults:`** replaces `config.defaults`.
- **v0's `exec` grab-bag is gone** — `wait:` and `rollout:` are first-class.
- **Helm flattened**: `repo:` is just the URL (no repository name — the SDK
  doesn't need a repo cache); `atomic:`/`wait:` sit directly on the op (no
  `flags:` block); `release:` defaults to the step name.
- **`values:` is a plain map** (the common case); `valuesFrom:` is a Flux-style
  list (`- file:` / `- url:`) for external values. `--set`-style overrides live
  on the CLI, not in the spec.
- **`manifests:` is one list** of `- inline:` / `- file:` / `- url:` entries,
  replacing three parallel fields.
- **`${VAR}` / `${VAR:-default}`** substitution (defaults demoed in
  `with-variables.yaml`).
- **Prefixed environment variables**: only env vars starting with `KHOOK_VAR_`
  are consumed (`KHOOK_VAR_APP_NAME=x` → `${APP_NAME}`); prefix is configurable
  via `--var-prefix`. Prevents accidentally injecting unrelated environment
  (PATH, CI secrets) into specs.

```yaml
apiVersion: khook.dvrkn.com/v1
kind: Khook
metadata:
  name: example
defaults:
  timeout: 5m
  onError: fail
steps:
  - name: cilium
    helm:
      chart: cilium
      repo: https://helm.cilium.io/
      version: 1.18.4
      namespace: kube-system
      atomic: true
  - name: all-ready
    needs: [cilium]
    wait:
      for: condition=Ready
      on: pods
      allNamespaces: true
```

---

## Phase 1 — Foundation & identity (make it a real project)

*Goal: solid ground to build on; someone else could clone and contribute.*

- [ ] **Name is `khook`** (domain decided: `khook.dvrkn.com/v1`). Align Go module
      path, binary name, repo README on it from the first commit.
- [ ] **CLI** with cobra subcommands: `apply`, `plan`, `validate`, `schema`, `version`.
- [ ] **Test coverage on the engine**: scheduler edge cases (diamond deps, onError
      + continue mid-level, retries), parser/validator table tests, executor tests
      against fake clients (`k8s.io/client-go/dynamic/fake`, Helm's test storage).
- [ ] **CI**: GitHub Actions — lint (golangci-lint), test, build matrix
      (linux/darwin, amd64/arm64), release via goreleaser (re-enable ARM).
- [ ] **E2E smoke test** against `k3d` in CI: spin up a k3d cluster, apply
      `examples/simple.yaml`, assert resources exist, re-apply to assert idempotency.
      k3d is the standard dev/test platform for this project.
- [ ] **Structured logging cleanup**: one logger, levels, `--log-format json|text`.

## Phase 2 — Operator UX (make it pleasant to run)

*Goal: the day-2 experience of running bootstraps interactively.*

- [ ] **`plan` / dry-run mode**: parse, validate, resolve variables, print the DAG
      execution plan (levels, what would install vs upgrade vs skip) without
      touching the cluster. Helm dry-run + kubectl server-side dry-run for a
      deeper `--diff` later.
- [ ] **Live progress output**: per-operation status lines (pending → running →
      ok/failed/skipped), timing, final summary table. `--output json` for CI.
- [ ] **Better failure reporting**: which operation, which level, what was skipped
      downstream because of it; non-zero exit codes distinguishing validation vs
      execution failures.
- [ ] **Variable UX**: `--set KEY=value` and `--var-file vars.yaml` on the CLI,
      env vars via the `KHOOK_VAR_` prefix (`--var-prefix` to change it),
      `${VAR:-default}` defaults, fail-fast on unresolved variables with a list of
      all missing ones (not just the first). Precedence: `--set` > `--var-file` >
      prefixed env > `${VAR:-default}`.
- [ ] **Spec composability**: multiple `-f` files / a directory of specs merged in
      order; `needs` across files.

## Phase 3 — DSL v2 (make the spec expressive enough for real clusters)

*Goal: cover the real bootstrap cases (`examples/real-case.yaml` and beyond)
without escape hatches.*

- [ ] **Conditionals**: `when:` expression on operations (variable-based, e.g.
      `when: ${ENABLE_ARGOCD} == "true"`), so one spec serves many environments.
- [ ] **Richer variable sources**: environment variables, files, and cloud
      secrets (AWS SSM / Secrets Manager) — pluggable resolver chain.
- [ ] **Helm depth**: OCI registry charts (`oci://`), local chart paths/tarballs,
      values from URLs, `reuseValues`, uninstall action, private repo auth
      (basic + ECR).
- [ ] **Kubectl depth**: prune/patch actions, `waitFor` shorthand on apply (apply
      + wait in one op), kustomize source (`sigs.k8s.io/kustomize` comes in
      transitively with the Helm SDK anyway), `wait.for: jsonpath=...`.
- [ ] **New op types** (`wait:` and `rollout:` are already first-class in the v1 shape;
      see the coverage matrix in `docs/dsl.md`):
      - `job`: run a container to completion in-cluster (escape hatch for
        anything we don't model — replaces "shell scripts" in the cloud-init analogy)
      - `label:` / `annotate:` (e.g. tagging nodes/namespaces during bootstrap)
      - `scale:` (e.g. `kubectl scale` shorthand for sizing system workloads)
- [ ] **Per-operation retries** honoring top-level `defaults:` (`retries` /
      `retryDelay`) — v0 declared these in the schema without wiring them;
      this time they land schema + engine together.
- [ ] **apiVersion `v1` freeze**: document the DSL, publish the JSON schema (raw
      GitHub URL + JSON Schema Store) so editors autocomplete `# yaml-language-server`.

## Phase 4 — Idempotency, state & resume (make re-runs first-class)

*Goal: a failed bootstrap at operation 7/12 is a resume, not a redo.*

- [ ] **Consistent skip semantics** across all op types (`skipIfInstalled` /
      `skipIfExists` today are per-type and partial) → unified `skipIf` policy.
- [ ] **Run state record**: write a ConfigMap/Secret in-cluster (like Helm does)
      recording spec hash + per-operation outcome; `apply` detects a prior partial
      run and continues from the failure point.
- [ ] **Change detection**: hash operation inputs (chart version + values +
      manifests); unchanged + previously-successful → skip. This is what makes
      "run it on every terraform apply" cheap.
- [ ] **`status` subcommand**: read the state record, show last run, per-op outcomes.
- [ ] **`destroy` (stretch)**: reverse-topological teardown for dev clusters.

## Phase 5 — Delivery & integrations (meet users where clusters are created)

*Goal: trivially runnable from every place a cluster gets created.*

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
- [ ] **k3d dev loop**: `k3d cluster create dev && khook apply -f spec.yaml --context k3d-dev`
      documented as the "try it in 60 seconds" quickstart.

## Phase 6 — Observability & scale (nice-to-have, demand-driven)

- [ ] OpenTelemetry traces (one span per operation — DAG visualizes for free in any
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

1. Phase 1 in full — it de-risks everything after (especially the k3d-based E2E,
   which becomes the safety net for all DSL work).
2. Phase 2's `plan` + progress output — biggest day-to-day UX win for the effort.
3. Phase 3 driven by real specs: take `real-case.yaml`, remove every workaround it
   needed, and let that dictate which DSL features land first.
4. Phase 4 before advertising widely — "safe to re-run, resumes on failure" is the
   core promise of a bootstrapper.
5. Phases 5–6 as adoption demands.

## Open questions (decide before Phase 3)

- ~~**Name & domain**~~ — decided: `khook`, `apiVersion: khook.dvrkn.com/v1`.
- **Multi-cluster in one spec**: out of scope, or a `targets:` concept later?
- **Secrets in specs**: recommend External Secrets pattern only, or support
  first-class secret variable sources (SSM/SM) in Phase 3?
- **Lambda payload vs S3**: specs can outgrow the 256 KB invoke limit — accept an
  S3 URI as the spec source?
