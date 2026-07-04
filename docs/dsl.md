---
title: DSL specification
layout: docs
permalink: /dsl/
description: Normative spec for apiVersion khook.io/v1, kind Khook — step types, variables, pipelines, and conditions.
---
<!-- {% raw %} — body is Liquid-free on the website build; invisible on GitHub -->
# khook DSL v1 — specification

Normative spec for `apiVersion: khook.io/v1`, `kind: Khook`.
`examples/*.yaml` must always validate against this document; where they
disagree, this document wins. Anything marked **(roadmap)** is not part of v1
core — see the command coverage matrix at the bottom and `roadmap.md`.

A machine-readable JSON Schema of this spec is committed at
[`schema/v1/khook.json`](../schema/v1/khook.json) (also printed by
`khook schema`). For editor validation and autocomplete, point
yaml-language-server at it from the first line of a spec:

```yaml
# yaml-language-server: $schema=<path or URL to khook.json>
```

The examples use the canonical URL,
`https://khook.io/schema/v1/khook.json` (the schema's `$id`, served by the
website); a repo-relative path to the committed artifact works too — offline,
or before the site is reachable.

## Document envelope

```yaml
apiVersion: khook.io/v1   # required, fixed
kind: Khook                      # required, fixed
metadata:
  name: my-bootstrap             # required; used in logs/state record
defaults: { ... }                # optional
steps: [ ... ]                   # required, at least one
```

## Variables

`${NAME}` and `${NAME:-default}` are substituted textually across the whole
document **before** YAML parsing (v0-proven approach; a variable can therefore
hold any scalar). Sources, by precedence:

1. CLI `--set NAME=value`
2. CLI `--var-file vars.yaml` (flat `NAME: value` map)
3. Environment variables prefixed `KHOOK_SECRET_` (`--secret-prefix` to
   override) — same substitution as below, but the value is redacted from all
   khook output (logs, plan, diff, errors); see `docs/cli.md`
4. Environment variables prefixed `KHOOK_VAR_` (`KHOOK_VAR_FOO` → `${FOO}`);
   prefix configurable with `--var-prefix`
5. `${NAME:-default}` fallback written in the spec

A `${NAME}` with no source and no default is a validation error; all missing
variables are reported at once.

### Pipelines — sprig functions on values

A reference can pipe the resolved value through
[sprig](https://github.com/Masterminds/sprig) functions, helm-template style:

```yaml
metadata:
  name: ${APP | lower | trunc 63}
stringData:
  tokenB64: ${TOKEN | b64enc}
timeout: ${WINDOW:-5|printf "%sm"}     # default applies first, then the pipes
```

Grammar: `${NAME}`, `${NAME:-default}`, `${NAME|pipeline}`,
`${NAME:-default|pipeline}`. The pipeline is ordinary sprig — `fn`, `fn arg`,
chained with `|` — applied to the value (helm's `.` | pipeline form). Rules:

- **Only the pipeline text is templated** — it is spec-authored. Values are
  data, never parsed as templates, and the document itself never goes through
  a template engine (`{{ }}` in embedded Argo/Helm manifests stays untouched).
- **Hermetic function set**: sprig's hermetic map — no `env`/`expandenv`
  (would bypass the `KHOOK_VAR_` isolation), no network, and nothing
  nondeterministic (`now`, `rand*`, `uuidv4`, certificate/password
  generators): substitution must produce the same spec for `plan` and
  `apply`.
- **Strict missing still applies**: `${NAME|b64enc}` with `NAME` unset is a
  missing-variable error, pipeline or not. Use `:-` for fallbacks
  (`${NAME:-dev|upper}`); sprig's `default` only sees empty strings, not
  unset names.
- A pipeline cannot contain `}`, and a `:-default` combined with a pipeline
  cannot contain `|` (use sprig `default`/`replace` inside the pipeline for
  such values).
- Pipeline outputs of secret variables (`KHOOK_SECRET_*`) are registered for
  output redaction alongside the raw values — `${TOKEN|b64enc}` is masked in
  logs, plan, and diff just like `${TOKEN}`.

## `defaults:`

Fallbacks for the per-step fields of the same name.

| Field | Type | Default | Notes |
|---|---|---|---|
| `timeout` | duration | `5m` | per-step execution timeout |
| `retries` | int | `0` | retry attempts after a failed try |
| `retryDelay` | duration | `10s` | pause between tries |
| `onError` | `fail` \| `continue` | `fail` | `fail` stops scheduling new steps; `continue` marks the step failed and keeps going |

## `state:` — the run-state record

Optional and off by default. When present, khook journals the run in an
in-cluster Secret — the spec's hash plus every step's outcome — and a re-run
of the *same* spec resumes: steps the record proves complete are skipped
(reported `skipped` with reason `succeeded in a previous run (state
record)`) and still satisfy `needs`. The record is a journal, not an
ownership ledger: delete the Secret and nothing breaks — the next run just
re-converges everything.

```yaml
state:
  enabled: true          # optional; writing `state: {}` already opts in.
                         # `enabled: ${USE_STATE:-false}` toggles per env.
  namespace: kube-system # optional; default "default"
  name: my-bootstrap     # optional; default "khook-state-<metadata.name>"
```

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `true` *when the block is present* | absent `state:` block = disabled |
| `namespace` | string | `default` | where the record Secret lives (RFC 1123 label) |
| `name` | string | `khook-state-<metadata.name>` | record Secret name (RFC 1123 subdomain) |

Semantics, precisely:

- The record keys on a **whole-spec hash** taken after variable substitution
  and parsing. Cosmetic YAML edits (comments, key order, quoting) do not
  change it; any effective change — a value, a version, a rotated secret
  that is substituted into the document — does, and a changed hash means the
  record is ignored and the run starts fresh.
- Only steps recorded `ok` resume. Failed and skipped steps re-run. `when:`
  is re-evaluated every run and wins over the record. Per-op checks
  (`skipIfInstalled`, `skipIfExists`, `skipIfSucceeded`) are unchanged and
  still guard the steps that actually execute.
- The journal is written incrementally after every step, so an interrupted
  run (crash, Ctrl-C) resumes from the last completed step.
- khook refuses to touch a Secret of the record's name that it does not own
  (no `app.kubernetes.io/managed-by: khook` label).
- What is stored: the spec **hash**, khook's version, timestamps, and
  per-step outcomes with redacted, truncated error text. The rendered spec —
  which can contain secret values — is never written to the cluster.

### When to enable it

**Enable it** when a re-run costs real time or real disruption:

- **Long bootstraps.** A realistic sequence — CNI, ingress, cert-manager,
  secrets tooling, GitOps controller, each with waits — easily runs 10+
  minutes. A failure at step 9 of 12 without state means redoing the nine;
  with state it means those nine skip in about a second each.
- **Specs applied automatically on every `terraform apply` / CI run.** The
  record turns "khook runs again" into a cheap no-op: unchanged spec, all
  steps resume-skip, exit 0. Without it every run re-executes each step's
  idempotent path (helm history lookups, server-side applies, waits).
- **Steps that are disruptive or costly to repeat even when idempotent** — a
  `job:` that re-runs a data migration, a helm upgrade that restarts
  workloads, charts pulled from rate-limited registries.

**Optional** for mid-size specs where the per-op `skipIf*` checks already
make re-runs cheap. State still adds two things: resume decisions without
any cluster probing, including for step types with no natural existence
check (`patch:`, `wait:`, `rollout:`), and `khook status` visibility into
what the last run did.

**Skip it** when:

- the spec is small and fast — re-running everything costs seconds;
- the cluster is throwaway (k3d/kind recreated more often than re-applied);
- khook's credentials cannot get Secret write access in the state namespace;
- the spec's inputs change on every run (the hash would never match, so the
  record would never resume — pure overhead).

It is **never required**: khook without state is already idempotent per
step. State is an optimization for resume speed and run observability, not
a correctness requirement.

### Tradeoffs of enabling it

- **RBAC**: the runner needs `get`/`create`/`update` on Secrets in the state
  namespace — a write grant you may not otherwise need.
- **Resume trusts the journal, not the cluster.** A step recorded `ok` is
  skipped even if its resources were deleted out-of-band since the last run.
  khook does no drift detection — by design, that's the GitOps controller's
  job. Mitigation: delete the record Secret (the next run re-converges
  everything) or change the spec (hash mismatch forces a fresh run).
- **Any effective spec change means a full re-run** — including rotated
  secret values that are substituted into the document. Per-step change
  detection is on the roadmap, not in the record today.
- **Single runner assumed.** No locking, no leader election; two concurrent
  applies against the same record are last-write-wins.
- **A run that succeeds but cannot write its record exits 1.** Opting into
  state makes the journal part of the contract; silent state loss would be
  worse than a loud exit code.
- **A crash can leave `runStatus: running` behind.** It is a marker for
  `status` output, not a lock; the next apply overwrites it.
- **Size** is a non-issue: the record is one small JSON blob (error text is
  truncated), far below the ~1 MiB object cap.

## Steps — common fields

```yaml
steps:
  - name: cilium          # required, unique, DNS-label-ish ([a-z0-9-])
    needs: [other-step]   # optional; DAG edges, must reference existing names
    when: vars.ENV == "prod"   # optional; CEL condition, see below
    timeout: 10m          # optional, overrides defaults
    retries: 2            # optional, overrides defaults
    retryDelay: 30s       # optional, overrides defaults
    onError: continue     # optional, overrides defaults
    helm: { ... }         # exactly ONE action key per step:
                          #   helm | apply | delete | patch | wait | rollout | job
```

The action key determines the step type — there is no `type:` field. Zero or
two+ action keys is a validation error.

Execution: steps are topologically sorted (cycle → validation error) and run
in parallel levels; a step runs only when all `needs` succeeded. If a step
fails with `onError: fail`, running steps finish, nothing new starts, and every
not-yet-run step is reported `skipped`.

## `when:` — conditional steps

`when:` holds a [CEL](https://cel.dev) expression; the step runs only when it
evaluates to `true`. Conditions see the merged variable map and nothing else —
they are decided once, at spec load time, before anything touches the cluster
(`plan` shows the outcome, offline included).

The expression environment, on top of the CEL standard library (`&&`, `||`,
`!`, `==`, `in`, `startsWith`, `endsWith`, `contains`, `matches`, ternaries):

| Expression | Meaning |
|---|---|
| `vars` | the merged variable map; every value is a string |
| `vars.NAME` | the variable's value — an error if unset (like a bare `${NAME}`) |
| `vars.get("NAME", "default")` | the variable's value, or the default when unset |
| `has(vars.NAME)` | whether the variable is set |

The expression must type-check to a bool; anything else is a validation
error.

```yaml
steps:
  - name: argocd
    when: vars.get("ENABLE_ARGOCD", "false") == "true"
    helm: { ... }
```

Semantics:

- **All variable values are strings** — compare against `"true"`, not `true`.
- **Write `vars.NAME`, not `${NAME}`**: `${NAME}` is substituted textually
  before parsing, so it would splice the raw value into the expression as
  bare tokens instead of a string.
- A step excluded by `when:` is reported `skipped` but **satisfies `needs`** —
  `needs` expresses ordering, and the exclusion is deliberate (a *failed*
  step, by contrast, does block its dependents). Dependents that should be
  excluded together need their own `when:`.

## `helm:` — install or upgrade a chart release

Declarative install-or-upgrade (the release history decides which; v0-proven).

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `chart` | string | yes | | chart source — bare name, `oci://` reference, or local path; see [Chart sources](#chart-sources) |
| `repo` | URL | bare-name charts | | HTTP(S) chart repository URL; no repo "name" needed |
| `version` | string | no | latest | exact chart version (recommended: always pin); may instead be written inline — `chart: name:1.2.3` |
| `auth` | map | no | | `username:` + `password:` for a private repo/registry; see [Private sources](#private-sources--auth) |
| `release` | string | no | step `name` | Helm release name |
| `namespace` | string | no | `default` | target namespace |
| `createNamespace` | bool | no | `false` | create namespace if missing |
| `skipIfInstalled` | bool | no | `false` | skip (success) if the release already exists, regardless of version/values |
| `atomic` | bool | no | `false` | roll back on failure |
| `wait` | bool | no | `false` | wait for resources ready before step succeeds |
| `values` | map | no | | inline values (merged over chart defaults) |
| `valuesFrom` | list | no | | ordered list of sources, each exactly one of `- file: path` / `- url: https://...`; later entries and `values` override earlier ones |

```yaml
helm:
  chart: cilium
  repo: https://helm.cilium.io/
  version: 1.18.4
  namespace: kube-system
  atomic: true
  valuesFrom:
    - file: ./values/cilium.yaml
  values:
    hubble:
      enabled: true
```

### Chart sources

The shape of `chart:` implies where the chart comes from — there is no
discriminator field:

```yaml
chart: cilium                                  # bare name — resolved in repo: (required)
chart: cilium:1.18.4                           # same, version inline (names cannot contain ":")
chart: oci://ghcr.io/org/charts/my-app:1.2.3   # OCI registry reference; repo: forbidden
chart: ./charts/my-app                         # local directory
chart: ./charts/my-app-1.2.3.tgz               # packaged chart
```

Rules:

- A **bare name** requires `repo:`; the other forms forbid it.
- An **`oci://` reference** versions itself with an inline `:tag` (or
  `@digest`), or via `version:` — setting both is an error.
- A **local path** must start with `./`, `../`, or `/` and forbids
  `version:` (the path already pins the chart) and any auth.
- The inline `name:version` sugar and the `version:` field are mutually
  exclusive in every form.

### Private sources — auth

Credentials come either inline as URL userinfo or as an `auth:` block —
never both. Pass secrets as variables (`KHOOK_SECRET_*` values are redacted
from all output); khook strips inline credentials from every log, plan,
diff, and error line regardless.

```yaml
# auth: block — raw values, no encoding needed
helm:
  chart: oci://123456789.dkr.ecr.us-east-1.amazonaws.com/charts/my-app:1.2.3
  auth:
    username: AWS
    password: ${ECR_TOKEN}

# inline userinfo — URL-encode anything special: ${VAR|urlquery}
helm:
  chart: my-app
  repo: https://${CHART_USER}:${CHART_PASS|urlquery}@charts.corp.example
```

The inline form must be a full `<username>:<password>@` pair, URL-encoded
where the values contain URL-special characters (`|urlquery` does this; ECR
tokens, being base64, always need it inline — prefer the `auth:` block for
such tokens).

**ECR recipe** — khook never calls AWS itself (see roadmap non-goals);
whatever runs khook mints the token:

```bash
export KHOOK_SECRET_ECR_TOKEN="$(aws ecr get-login-password)"
```

The token is ordinary basic auth with username `AWS`, as in the `auth:`
example above.

## `apply:` — declaratively apply manifests

Server-side-agnostic `kubectl apply` via the dynamic client. Multi-document
YAML is supported in every source.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `manifests` | list | yes | | ordered list of sources, each exactly one of `inline:` (YAML string), `file:` (path), `url:` (HTTP(S)), `kustomize:` (local kustomization directory) |
| `namespace` | string | no | | default namespace for namespace-less namespaced resources |
| `createNamespace` | bool | no | `false` | create `namespace` if missing |
| `skipIfExists` | bool | no | `false` | skip (success) if all resources already exist |
| `serverSide` | bool | no | `false` | server-side apply |
| `waitFor` | string | no | | block until every applied object meets this — [`wait.for`'s grammar](#wait--block-until-a-condition-holds) minus `delete` |

```yaml
apply:
  namespace: argocd
  waitFor: condition=Established
  manifests:
    - inline: |
        apiVersion: argoproj.io/v1alpha1
        kind: Application
        ...
    - file: ./manifests/extra.yaml
    - url: https://example.com/manifest.yaml
    - kustomize: ./overlays/prod
```

`waitFor` — apply + wait in one step, the
`kubectl apply -f x && kubectl wait --for=... -f x` equivalent: after the
apply, the step polls **exactly the objects it applied** until each meets the
condition, bounded by the step `timeout`. It also runs when `skipIfExists`
short-circuits — the condition must hold whether this run created the objects
or found them, so re-runs behave like first runs. Waiting on *other*
resources (or a subset) is a separate `wait:` step.

`kustomize:` sources are rendered in-process (`sigs.k8s.io/kustomize`) and
must be **local** paths (`./`, `../`, or `/`). Kustomizations referencing
remote bases fail: kustomize shells out to `git` for those, which the
zero-runtime-deps rule excludes.

## `delete:` — remove resources

Three mutually exclusive forms. `helm:` owns presence, `delete:` owns
absence — uninstalling a Helm release is the third form here, not a mode of
`helm:`.

**By manifests** (delete what these define; same source list as
`apply.manifests`, `kustomize:` included):

```yaml
delete:
  manifests:
    - file: ./manifests/old.yaml
```

**By reference/selector** (the bootstrap-critical form — e.g. removing
`aws-node` before installing Cilium):

| Field | Type | Required | Notes |
|---|---|---|---|
| `resource` | string | yes | type (`pods`) or `kind/name` (`daemonset/aws-node`) |
| `namespace` | string | no | mutually exclusive with `allNamespaces` |
| `allNamespaces` | bool | no | |
| `selector` | string | no | label selector |
| `fieldSelector` | string | no | field selector |
| `ignoreNotFound` | bool | no (`true`) | absent resources are success, not failure |

```yaml
delete:
  resource: daemonset/aws-node
  namespace: kube-system
```

**By release** (helm uninstall):

| Field | Type | Required | Notes |
|---|---|---|---|
| `release` | string | yes | Helm release name |
| `namespace` | string | no (`default`) | the release's namespace |
| `ignoreNotFound` | bool | no (`true`) | an absent release is success, not failure |

The step waits until the release's resources are gone (like the other
forms), bounded by the step `timeout`. `selector`, `fieldSelector`, and
`allNamespaces` do not apply.

```yaml
delete:
  release: nginx-ingress
  namespace: ingress
```

## `patch:` — modify a resource in place

`kubectl patch`: change fields of a resource whose full manifest this spec
does not own (a chart-installed DaemonSet, a default StorageClass, a CR).
When the spec *does* own the manifest, prefer re-`apply:`ing it.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `target` | string | yes | | `kind/name` (`daemonset/aws-node`) |
| `namespace` | string | no | `default` | ignored for cluster-scoped kinds |
| `type` | string | no | `strategic` | `strategic` \| `merge` \| `json` |
| `patch` | map or list | yes | | the patch body: a mapping for `strategic`/`merge`, a list of operations for `json` |

```yaml
patch:
  target: daemonset/aws-node
  namespace: kube-system
  patch:
    spec:
      template:
        spec:
          nodeSelector:
            khook.io/non-existing: "true"
```

Semantics:

- The target must exist — a missing resource fails the step (order it after
  whatever creates it with `needs:`).
- `strategic` (the default, like kubectl) understands list merge keys of
  built-in types but is **rejected by custom resources** — use `merge`
  (RFC 7386) there; it works on every kind but replaces lists wholesale.
- `json` (RFC 6902) is a list of `op`/`path`/`value` operations, for list
  surgery and field removal. Mind idempotency: a `remove` of an
  already-removed path or a list-index `add` fails on re-runs —
  `strategic`/`merge` are naturally idempotent, prefer them.
- `plan` reports the patch; `diff` shows the exact change via a server
  dry-run of the patch.

```yaml
# merge for CRs / cluster-scoped targets; json for removals
patch:
  target: storageclass/gp3
  type: merge
  patch:
    metadata:
      annotations:
        storageclass.kubernetes.io/is-default-class: "true"
```

## `wait:` — block until a condition holds

| Field | Type | Required | Notes |
|---|---|---|---|
| `for` | string | yes | `condition=<Name>[=<value>]`, `jsonpath=<expr>[=<value>]`, or `delete` |
| `on` | string | yes | resource type (`pods`) or `kind/name` (`deployment/argocd-server`) |
| `namespace` | string | no | mutually exclusive with `allNamespaces` |
| `allNamespaces` | bool | no | |
| `selector` | string | no | label selector |
| `fieldSelector` | string | no | field selector |

The step-level `timeout` bounds the wait — there is no separate `wait.timeout`.

```yaml
wait:
  for: condition=Ready
  on: pods
  allNamespaces: true
```

`for:` forms, matching `kubectl wait`:

- `condition=<Name>` — `status.conditions[type==Name].status` is `"True"`;
  `condition=<Name>=<value>` compares against another value.
- `jsonpath=<expr>` — the expression yields at least one non-empty value;
  `jsonpath=<expr>=<value>` — some yielded value equals `<value>`. kubectl's
  relaxed syntax is accepted: `{.status.phase}`, `.status.phase`, and
  `status.phase` are equivalent. A missing path means "not yet", not an
  error. Expressions with filters (which contain `=`) need the braced form:
  `jsonpath={.status.conditions[?(@.type=="Ready")].status}=True`.
- `delete` — every matching resource is gone.

```yaml
wait:
  for: jsonpath={.status.readyReplicas}=2
  on: deployment/coredns
  namespace: kube-system
```

## `rollout:` — imperative rollout commands

Exactly one of `restart:` / `status:`, each taking `kind/name`.

| Field | Type | Required | Notes |
|---|---|---|---|
| `restart` | string | one of | `deployment/x`, `daemonset/x`, `statefulset/x` |
| `status` | string | one of | same format; blocks until rollout complete (bounded by step `timeout`) |
| `namespace` | string | yes | |

```yaml
rollout:
  restart: daemonset/eks-pod-identity-agent
  namespace: kube-system
```

## `job:` — run a container to completion

The escape hatch: anything the DSL does not model runs as a `batch/v1` Job —
the "shell script" slot of the cloud-init analogy. khook creates the Job,
waits for it to finish (bounded by the step `timeout`), and on failure
surfaces the pod's last log lines in the step error.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `image` | string | yes | | container image to run |
| `command` | list | no | image entrypoint | container command (entrypoint override) |
| `args` | list | no | | container args |
| `env` | map | no | | environment variables (`NAME: value`) |
| `namespace` | string | no | `default` | namespace the Job runs in |
| `createNamespace` | bool | no | `false` | create `namespace` if missing |
| `serviceAccount` | string | no | namespace default | ServiceAccount for the pod |
| `skipIfSucceeded` | bool | no | `false` | skip (success) if this step's Job already completed successfully |

```yaml
job:
  image: public.ecr.aws/aws-cli/aws-cli:2.17.0
  command: ["sh", "-c"]
  args: ["aws sts get-caller-identity"]
  env:
    AWS_REGION: us-east-1
  namespace: kube-system
  serviceAccount: bootstrap-admin
```

Semantics:

- The Job is named after the step and labeled
  `app.kubernetes.io/managed-by: khook`. A same-named Job **not** carrying
  that label is an error — khook never replaces a Job it does not own.
- Each run **replaces** the previous run's Job (delete, wait for it to be
  gone, recreate) unless `skipIfSucceeded` short-circuits.
- Retries follow the step's `retries`: the Job is created with
  `backoffLimit: 0` and `restartPolicy: Never`, so every khook attempt is a
  fresh Job rather than an in-cluster pod restart.
- The step `timeout` is also set as the Job's `activeDeadlineSeconds`, so a
  Job khook stops waiting on cannot keep running in-cluster.

Jobs do not publish values back to the spec (non-goal: khook is not a data
bus). To hand data to later steps, write a Secret/ConfigMap from the job and
have consumers reference it *by name* (`secretKeyRef`, `existingSecret`-style
chart values) — the name is static and plannable, the value flows through the
API server.

## Command coverage matrix

What a "cloud-init for k8s" needs, mapped to the DSL. Non-goals excluded
(see roadmap.md).

| CLI equivalent | khook | Status |
|---|---|---|
| `helm repo add` + `helm install`/`upgrade` | `helm:` | **v1 core** |
| `helm install --atomic/--wait/--create-namespace` | `helm.atomic/wait/createNamespace` | **v1 core** |
| `helm install -f values.yaml --set k=v` | `helm.valuesFrom` (file or url) / `helm.values` | **v1 core** |
| `helm install oci://...` / local chart | `helm.chart: oci://...` / path | **v1 core** |
| `helm uninstall` | `delete.release` | **v1 core** |
| `helm install --username/--password` / `helm registry login` | `helm.auth` / URL userinfo | **v1 core** |
| `kubectl apply -f file/url/-` | `apply:` | **v1 core** |
| `kubectl apply --server-side` | `apply.serverSide` | **v1 core** |
| `kubectl apply -k` (kustomize) | `apply.manifests: - kustomize:` (local) | **v1 core** |
| `kubectl apply -f x && kubectl wait -f x` | `apply.waitFor` | **v1 core** |
| `kubectl delete -f` / by selector | `delete:` | **v1 core** |
| `kubectl patch` (strategic/merge/json) | `patch:` | **v1 core** |
| `kubectl wait --for=condition=...` | `wait:` | **v1 core** |
| `kubectl wait --for=jsonpath=...` | `wait.for: jsonpath=...` / `apply.waitFor` | **v1 core** |
| `kubectl rollout restart/status` | `rollout:` | **v1 core** |
| `kubectl create namespace` | `createNamespace: true` / `apply:` | **v1 core** |
| arbitrary in-cluster commands | `job:` (container to completion) | **v1 core** |
| `helm rollback` | — `atomic:` covers failed upgrades; re-applying the spec is the recovery path | non-goal |
| `kubectl apply --prune` | — pruning is reconciliation; Argo/Flux own it, `delete:` owns explicit absence | non-goal |
| `kubectl label` / `annotate` | `patch:` (or `apply:` a minimal manifest — existing objects are merge-patched) | **v1 core** |
| `kubectl scale` | `patch:` `spec.replicas` (or `apply:` a minimal manifest) | **v1 core** |
| `kubectl exec` / `cp` / `port-forward` | — interactive, out of scope | non-goal |
| `kubectl get/describe` as output | — read paths belong to `plan`/`status` | non-goal |

## Appendix: design decisions vs the v0 prototype

The v1 DSL is a from-scratch redesign of a proven v0 prototype (its
`examples/` survive in-tree). Decisions made in the redesign, recorded so
they are not re-litigated:

- **`kind: Khook`** (was `ClusterBootstrap`) with `apiVersion: khook.io/v1`.
- **Action key implies the type** — no `type:` discriminator. A step has exactly
  one action key (`helm:`, `apply:`, `delete:`, ... — schema: oneOf).
- **`steps:` / `needs:`** replace v0's `operations:` / `dependsOn:`.
- **Top-level `defaults:`** replaces `config.defaults`.
- **v0's `exec` grab-bag is gone** — `wait:` and `rollout:` are first-class.
- **Helm flattened**: `repo:` is just the URL (no repository name — the SDK
  doesn't need a repo cache); `atomic:`/`wait:` sit directly on the op (no
  `flags:` block); `release:` defaults to the step name.
- **Chart form implies source** — one `chart:` field covers repo charts,
  `oci://` references, and local paths; no `type:`/`sourceRef:` discriminator.
- **Uninstall lives on `delete:`** (`delete.release`), not on `helm:` — the
  step types split by desired state (present vs absent), so a `helm:` step is
  always declarative install-or-upgrade and never flips meaning on a flag.
- **No `reuseValues`** — carrying a prior release's values forward makes a
  run's outcome depend on cluster state, breaking "the same spec twice gives
  the same result". The spec is the sole source of truth for values;
  `skipIfInstalled` covers "don't touch an existing release".
- **No `helm rollback`** — `atomic:` handles failed upgrades; recovery
  otherwise is re-applying a known-good spec, not imperative history surgery.
- **`values:` is a plain map** (the common case); `valuesFrom:` is a Flux-style
  list for external values. `--set`-style overrides live on the CLI, not in
  the spec.
- **`manifests:` is one list** of `- inline:` / `- file:` / `- url:` entries,
  replacing three parallel fields.
- **Prefixed environment variables**: only env vars starting with `KHOOK_VAR_`
  are consumed (`--var-prefix` to override), preventing unrelated environment
  (PATH, CI secrets) from leaking into specs.
- **No `apply.prune`** — pruning is drift reconciliation, which khook hands
  off to Argo/Flux; `delete:` owns explicit absence. kubectl's own `--prune`
  is quasi-deprecated (its ApplySet successor is still alpha).
- **`patch:` defaults to `strategic`** (kubectl parity, muscle memory) even
  though strategic fails on CRs — the error hints at `type: merge`. The body
  field is named `patch:` (kustomize's `target:`/`patch:` naming), accepting
  a structured mapping/list rather than an embedded string.
- **`apply.waitFor` is a plain string** waiting on exactly the applied
  objects — scoping or waiting on other resources is what a `wait:` step is
  for. It shares `wait.for`'s grammar rather than growing its own.
- **`kustomize:` is a manifest source, not a step type** — one list, four
  source shapes; local paths only (remote bases would need a `git` binary).

<!-- {% endraw %} -->
