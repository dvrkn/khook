---
title: DSL specification
layout: docs
permalink: /dsl/
description: Normative spec for apiVersion khook.io/v1, kind Khook — step types, variables, pipelines, and conditions.
---
<!-- {% raw %} — body is Liquid-free on the website build; invisible on GitHub -->
# khook DSL v1 — specification

Normative spec for `apiVersion: khook.io/v1`, `kind: Khook`.
`examples/*.yaml` must validate against this document; where they disagree,
this document wins.

The JSON Schema is committed at [`docs/schema/v1/khook.json`](schema/v1/khook.json)
and printed by `khook schema`. For editor validation and autocomplete, add
this as the first line of a spec:

```yaml
# yaml-language-server: $schema=<path or URL to khook.json>
```

The examples use the schema's `$id`, `https://khook.io/schema/v1/khook.json`,
served by the website. A repo-relative path to the committed file also works,
including offline.

## Document envelope

```yaml
apiVersion: khook.io/v1   # required, fixed
kind: Khook               # required, fixed
metadata:
  name: my-bootstrap      # required; used in logs and the state record
defaults: { ... }         # optional
steps: [ ... ]            # required, at least one
```

## Variables

`${NAME}` and `${NAME:-default}` are substituted as text across the whole
document **before** YAML parsing, so a variable can hold any scalar. Sources,
highest precedence first:

1. CLI `--set NAME=value`
2. CLI `--var-file vars.yaml` (flat `NAME: value` map)
3. Env vars prefixed `KHOOK_SECRET_` (`--secret-prefix` to override): same as
   below, but the value is redacted from khook's output (logs, plan, diff,
   errors); see the [CLI reference](cli.md)
4. Env vars prefixed `KHOOK_VAR_` (`KHOOK_VAR_FOO` → `${FOO}`;
   `--var-prefix` to override)
5. The `${NAME:-default}` fallback in the spec

A `${NAME}` with no source and no default is a validation error; all missing
variables are reported at once.

### Pipelines — sprig functions on values

A reference can pipe its value through
[sprig](https://github.com/Masterminds/sprig) functions, as in Helm templates:

```yaml
metadata:
  name: ${APP | lower | trunc 63}
stringData:
  tokenB64: ${TOKEN | b64enc}
timeout: ${WINDOW:-5|printf "%sm"}     # default applies first, then the pipes
```

Grammar: `${NAME}`, `${NAME:-default}`, `${NAME|pipeline}`,
`${NAME:-default|pipeline}`. The pipeline is plain sprig (`fn`, `fn arg`,
chained with `|`) applied to the value, like Helm's `. | pipeline`. Rules:

- **Only the pipeline is templated.** Values are data and are never parsed as
  templates. The document never goes through a template engine, so `{{ }}` in
  embedded Argo/Helm manifests is left alone.
- **Hermetic functions only**: sprig's hermetic map, minus anything that
  reads the environment (`env`, `expandenv`, which would bypass the
  `KHOOK_VAR_` prefix), touches the network, or is nondeterministic (`now`,
  `rand*`, `uuidv4`, certificate/password generators). `plan` and `apply` must
  see the same spec.
- **Missing is still an error**: `${NAME|b64enc}` with `NAME` unset fails like
  `${NAME}`. Use `:-` for fallbacks (`${NAME:-dev|upper}`); sprig's `default`
  only sees empty strings, not unset names.
- A pipeline cannot contain `}`, and a `:-default` followed by a pipeline
  cannot contain `|` (use sprig `default`/`replace` inside the pipeline
  instead).
- Pipeline outputs of secret variables (`KHOOK_SECRET_*`) are redacted like
  the raw values: `${TOKEN|b64enc}` is masked in logs, plan, and diff.

## `defaults:`

Fallbacks for the per-step fields of the same name.

| Field | Type | Default | Notes |
|---|---|---|---|
| `timeout` | duration | `5m` | per-step execution timeout |
| `retries` | int | `0` | retry attempts after a failed try |
| `retryDelay` | duration | `10s` | pause between tries |
| `onError` | `fail` \| `continue` | `fail` | `fail` stops scheduling new steps; `continue` marks the step failed and keeps going |

## `state:` — the run-state record

Optional, off by default. When enabled, khook journals each run in an
in-cluster Secret (a per-step input hash plus each step's outcome). On a
re-run, steps whose inputs are unchanged since they last succeeded are skipped
with reason `unchanged since it succeeded in a previous run (state record)`
and still satisfy `needs`. Change detection is per step: editing one step
re-runs only that step. The record is a journal, not an inventory of objects;
deleting the Secret is safe and makes the next run re-converge everything.

```yaml
state:
  enabled: true          # optional; `state: {}` already opts in.
                         # `enabled: ${USE_STATE:-false}` toggles per env.
  namespace: kube-system # optional; default "default"
  name: my-bootstrap     # optional; default "khook-state-<metadata.name>"
```

| Field | Type | Default | Notes |
|---|---|---|---|
| `enabled` | bool | `true` *when the block is present* | no `state:` block = disabled |
| `namespace` | string | `default` | namespace of the record Secret (RFC 1123 label) |
| `name` | string | `khook-state-<metadata.name>` | record Secret name (RFC 1123 subdomain) |

Semantics:

- Each step has an **input hash**: the canonical form of its action block
  after variable substitution (chart, version, values, manifests, patch body)
  plus the content of every **local file** it references (`manifests[].file`,
  `kustomize:` directories, `valuesFrom[].file`, local chart paths). Cosmetic
  YAML edits (comments, key order, quoting) don't change it. Any effective
  change does, including an edited local file or a rotated secret value
  substituted into the step.
- A step resumes when the record shows it **succeeded with the same input
  hash**. Changes to other steps don't affect it. Failed and skipped steps
  re-run. `when:` is evaluated every run and overrides the record. Per-type
  `skipIf` checks still apply to steps that execute.
- Scheduling fields are not inputs: changing `needs`, `when`, `timeout`,
  `retries`, `retryDelay`, or `onError` does not re-run a completed step.
  Renaming a step does, because the name keys the record.
- Remote sources (`url:`, OCI tags, unpinned charts) are hashed by
  reference, not content; see the tradeoffs below.
- A changed step does not force its dependents to re-run. `needs` is
  ordering only, and steps don't pass data through khook, so a dependent's
  inputs can't change through its dependency.
- The journal is written after every step, so an interrupted run (crash,
  Ctrl-C) resumes from the last completed step.
- khook refuses to touch a Secret with the record's name that lacks the
  `app.kubernetes.io/managed-by: khook` label.
- A fully successful `khook destroy` deletes the record Secret, so the next
  apply re-converges from scratch.
- Stored: per-step input **hashes** and outcomes (error text redacted and
  truncated), the whole-spec hash, khook's version, and timestamps. The
  rendered spec, which can contain secrets, is never written to the cluster.

### When to enable it

**Enable it** when re-running costs real time or disruption:

- **Long bootstraps.** CNI, ingress, cert-manager, secrets tooling, and a
  GitOps controller, each with waits, can take 10+ minutes. After a failure at
  step 9 of 12, state skips the first eight in about a second each instead of
  re-running them.
- **Specs applied on every `terraform apply` or CI run.** With an unchanged
  spec, every step resume-skips and the run exits 0 quickly. Without state,
  each step re-runs its idempotent path (Helm history lookups, server-side
  applies, waits).
- **Steps that are costly to repeat even when idempotent**: a `job:` that
  runs a data migration, a Helm upgrade that restarts workloads, charts from
  rate-limited registries.

**Optional** for mid-size specs where `skipIf` already makes re-runs cheap.
State still adds resume decisions without probing the cluster, including for
types with no existence check (`patch:`, `wait:`, `rollout:`), and
`khook status` output for the last run.

**Skip it** when:

- the spec is small and re-running it takes seconds;
- the cluster is throwaway (k3d/kind recreated more often than re-applied);
- khook's credentials can't write Secrets in the state namespace;
- every step's inputs change on every run (e.g. a timestamp variable in each
  step), so no hash ever matches and the record only adds overhead.

State is **never required**. khook is idempotent per step without it; state
only speeds up resumes and records what ran.

### Tradeoffs of enabling it

- **RBAC**: the runner needs `get`/`create`/`update` on Secrets in the state
  namespace.
- **Resume trusts the journal, not the cluster.** A step recorded `ok` is
  skipped even if its resources were deleted out-of-band. khook does no drift
  detection; that is the GitOps controller's job. To force a re-run, delete
  the record Secret (everything re-converges) or edit the step (its hash
  changes).
- **Remote content changes are invisible.** A `url:` manifest or values
  source, a mutable OCI tag, or an unpinned chart can change upstream without
  changing the input hash, and the step still resume-skips. Pin what you can;
  delete the record when you can't.
- **Single runner assumed.** No locking; concurrent applies against the same
  record are last-write-wins.
- **A run that succeeds but can't write its record exits 1.** Enabling state
  makes the journal part of the contract.
- **A crash can leave `runStatus: running` behind.** It is a marker for
  `status`, not a lock; the next apply overwrites it.
- **Size**: the record is one small JSON blob (error text is truncated), far
  below the ~1 MiB object limit.

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

The action key sets the step type; there is no `type:` field. Zero or more
than one action key is a validation error.

Steps are sorted topologically (a cycle is a validation error) and run in
parallel levels. A step runs only when all its `needs` succeeded. If a step
fails with `onError: fail`, running steps finish, nothing new starts, and
every step not yet run is reported `skipped`.

## `when:` — conditional steps

`when:` holds a [CEL](https://cel.dev) expression; the step runs only if it
evaluates to `true`. Conditions see only the merged variable map and are
evaluated once at load time, before any cluster access (`plan` shows the
result, including `--offline`).

Available on top of the CEL standard library (`&&`, `||`, `!`, `==`, `in`,
`startsWith`, `endsWith`, `contains`, `matches`, ternaries):

| Expression | Meaning |
|---|---|
| `vars` | the merged variable map; every value is a string |
| `vars.NAME` | the variable's value; an error if unset (like a bare `${NAME}`) |
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

- **All values are strings**: compare against `"true"`, not `true`.
- **Use `vars.NAME`, not `${NAME}`.** `${NAME}` is substituted as text before
  parsing and would splice the raw value into the expression as bare tokens.
- A step excluded by `when:` is reported `skipped` but **satisfies `needs`**:
  `needs` is ordering, and the exclusion is deliberate. (A *failed* step does
  block its dependents.) Dependents that should be excluded too need their own
  `when:`.

## `skipIf` — skip a step that's already done

`skipIf` names a predicate checked against the cluster before the step runs.
If it holds, the step is skipped **as success** and satisfies `needs`. Each
type that has a natural "already done" check accepts exactly one predicate;
any other value fails validation, and the JSON Schema autocompletes the right
one per type:

| Step type | Predicate | Skips when |
|---|---|---|
| `helm:` | `skipIf: installed` | the release already exists (any version, any values) |
| `apply:` | `skipIf: exists` | every manifest resource already exists |
| `job:` | `skipIf: succeeded` | this step's Job already completed successfully |

Other types don't need it: `delete:` treats "already absent" as a no-op
(`ignoreNotFound` defaults to true), and `wait:` / `rollout: status` no-op when
the condition already holds. `skipIf` trades convergence for stability: once
the target exists, the step stops enforcing the spec's version, values, or
content. Use it for "leave it alone if it's there" steps, not for steps that
must converge on every run.

## `helm:` — install or upgrade a chart release

Install or upgrade, decided by the release history.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `chart` | string | yes | | bare name, `oci://` reference, or local path; see [Chart sources](#chart-sources) |
| `repo` | URL | bare-name charts | | HTTP(S) chart repository URL; no repo "name" needed |
| `version` | string | no | latest | exact chart version (always pin); may be written inline instead: `chart: name:1.2.3` |
| `auth` | map | no | | `username:` + `password:` for a private repo/registry; see [Private sources](#private-sources--auth) |
| `release` | string | no | step `name` | Helm release name |
| `namespace` | string | no | `default` | target namespace |
| `createNamespace` | bool | no | `false` | create namespace if missing |
| `skipIf` | `installed` | no | | skip (success) if the release exists, regardless of version/values; see [skipIf](#skipif--skip-a-step-thats-already-done) |
| `atomic` | bool | no | `false` | roll back on failure |
| `wait` | bool | no | `false` | wait for resources to be ready before the step succeeds |
| `values` | map | no | | inline values, merged over chart defaults |
| `valuesFrom` | list | no | | ordered sources, each exactly one of `- file: path` / `- url: https://...`; later entries and `values` override earlier ones |

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

The form of `chart:` determines the source:

```yaml
chart: cilium                                  # bare name, resolved in repo: (required)
chart: cilium:1.18.4                           # same, version inline (names cannot contain ":")
chart: oci://ghcr.io/org/charts/my-app:1.2.3   # OCI registry reference; repo: forbidden
chart: ./charts/my-app                         # local directory
chart: ./charts/my-app-1.2.3.tgz               # packaged chart
```

- A **bare name** requires `repo:`; the other forms forbid it.
- An **`oci://` reference** is versioned by an inline `:tag` (or `@digest`)
  or by `version:`, not both.
- A **local path** starts with `./`, `../`, or `/` and forbids `version:`
  (the path pins the chart) and any auth.
- Inline `name:version` and the `version:` field are mutually exclusive in
  every form.

### Private sources — auth

Credentials go either inline as URL userinfo or in an `auth:` block, not
both. Pass secrets as variables (`KHOOK_SECRET_*` values are redacted from all
output). khook strips inline credentials from every log, plan, diff, and error
line regardless.

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
where values contain URL-special characters (`|urlquery` does this). ECR
tokens are base64 and always need encoding inline, so prefer the `auth:`
block for them.

**ECR**: khook never calls AWS (see the roadmap non-goals). Whatever runs
khook mints the token:

```bash
export KHOOK_SECRET_ECR_TOKEN="$(aws ecr get-login-password)"
```

It is basic auth with username `AWS`, as in the `auth:` example above.

## `apply:` — declaratively apply manifests

`kubectl apply` through the dynamic client: missing objects are created,
existing ones merge-patched, or server-side applied with `serverSide: true`.
Every source may contain multiple YAML documents.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `manifests` | list | yes | | ordered sources, each exactly one of `inline:` (YAML string), `file:` (path), `url:` (HTTP(S)), `kustomize:` (local kustomization directory) |
| `namespace` | string | no | | default namespace for namespaced resources without one |
| `createNamespace` | bool | no | `false` | create `namespace` if missing |
| `skipIf` | `exists` | no | | skip (success) if all resources already exist; see [skipIf](#skipif--skip-a-step-thats-already-done) |
| `serverSide` | bool | no | `false` | server-side apply |
| `waitFor` | string | no | | wait until every applied object meets this; [`wait.for` grammar](#wait--block-until-a-condition-holds) without `delete` |

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

`waitFor` is apply and wait in one step
(`kubectl apply -f x && kubectl wait --for=... -f x`). After the apply, the
step polls **exactly the objects it applied** until each meets the condition,
bounded by the step `timeout`. It also runs when `skipIf: exists` skips the
apply, so re-runs behave like first runs. To wait on other resources or a
subset, use a separate `wait:` step.

`kustomize:` sources are rendered in-process (`sigs.k8s.io/kustomize`) and
must be **local** paths (`./`, `../`, or `/`). Remote bases fail: kustomize
needs a `git` binary for them, and khook has no runtime dependencies.

## `delete:` — remove resources

Three mutually exclusive forms. `helm:` owns presence and `delete:` owns
absence, so uninstalling a release is a `delete:` form, not a `helm:` mode.

**By manifests** (delete what they define; same sources as
`apply.manifests`, including `kustomize:`):

```yaml
delete:
  manifests:
    - file: ./manifests/old.yaml
```

**By reference or selector** (e.g. removing `aws-node` before installing
Cilium):

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

**By release** (Helm uninstall):

| Field | Type | Required | Notes |
|---|---|---|---|
| `release` | string | yes | Helm release name |
| `namespace` | string | no (`default`) | the release's namespace |
| `ignoreNotFound` | bool | no (`true`) | an absent release is success, not failure |

The step waits until the release's resources are gone, bounded by the step
`timeout`. `selector`, `fieldSelector`, and `allNamespaces` do not apply.

```yaml
delete:
  release: nginx-ingress
  namespace: ingress
```

## `patch:` — modify a resource in place

`kubectl patch`: change fields of a resource whose manifest this spec doesn't
own (a chart-installed DaemonSet, a default StorageClass, a CR). If the spec
owns the manifest, re-`apply:` it instead.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `target` | string | yes | | `kind/name` (`daemonset/aws-node`) |
| `namespace` | string | no | `default` | ignored for cluster-scoped kinds |
| `type` | string | no | `strategic` | `strategic` \| `merge` \| `json` |
| `patch` | map or list | yes | | patch body: a mapping for `strategic`/`merge`, a list of operations for `json` |

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

- The target must exist; a missing resource fails the step (order it after
  its creator with `needs:`).
- `strategic` (default, as in kubectl) knows the list merge keys of built-in
  types but is **rejected by custom resources**. Use `merge` (RFC 7386) there;
  it works on every kind but replaces lists wholesale.
- `json` (RFC 6902) is a list of `op`/`path`/`value` operations, for list
  edits and field removal. It is not always idempotent: removing an
  already-removed path or `add` at a list index fails on re-runs. Prefer
  `strategic`/`merge`.
- `plan` reports the patch; `--diff` shows the exact change via a server
  dry-run.

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

The step `timeout` bounds the wait; there is no `wait.timeout`.

```yaml
wait:
  for: condition=Ready
  on: pods
  allNamespaces: true
```

`for:` forms, as in `kubectl wait`:

- `condition=<Name>`: `status.conditions[type==Name].status` is `"True"`.
  `condition=<Name>=<value>` compares against another value.
- `jsonpath=<expr>`: the expression yields at least one non-empty value.
  `jsonpath=<expr>=<value>`: some yielded value equals `<value>`. kubectl's
  relaxed syntax works: `{.status.phase}`, `.status.phase`, and `status.phase`
  are equivalent. A missing path means "not yet", not an error. Filter
  expressions (which contain `=`) need the braced form:
  `jsonpath={.status.conditions[?(@.type=="Ready")].status}=True`.
- `delete`: every matching resource is gone.

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
| `status` | string | one of | same format; waits until the rollout completes (bounded by step `timeout`) |
| `namespace` | string | yes | |

```yaml
rollout:
  restart: daemonset/eks-pod-identity-agent
  namespace: kube-system
```

## `job:` — run a container to completion

For anything the DSL doesn't model: the step runs a `batch/v1` Job, waits
for it to finish (bounded by the step `timeout`), and on failure includes the
pod's last log lines in the step error.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `image` | string | yes | | container image |
| `command` | list | no | image entrypoint | container command (overrides entrypoint) |
| `args` | list | no | | container args |
| `env` | map | no | | environment variables (`NAME: value`) |
| `namespace` | string | no | `default` | namespace the Job runs in |
| `createNamespace` | bool | no | `false` | create `namespace` if missing |
| `serviceAccount` | string | no | namespace default | ServiceAccount for the pod |
| `skipIf` | `succeeded` | no | | skip (success) if this step's Job already completed successfully; see [skipIf](#skipif--skip-a-step-thats-already-done) |

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

- The Job is named after the step and labeled
  `app.kubernetes.io/managed-by: khook`. A same-named Job **without** that
  label is an error; khook never replaces a Job it doesn't own.
- Each run **replaces** the previous Job (delete, wait until gone, recreate)
  unless `skipIf: succeeded` applies.
- Retries follow the step's `retries`. The Job has `backoffLimit: 0` and
  `restartPolicy: Never`, so each attempt is a new Job, not a pod restart.
- The step `timeout` is also the Job's `activeDeadlineSeconds`, so a Job khook
  stops waiting on doesn't keep running.

Jobs don't pass values back to the spec (khook is not a data bus). To hand
data to later steps, have the job write a Secret or ConfigMap and reference it
*by name* (`secretKeyRef`, `existingSecret`-style chart values): the name is
static and plannable, and the value goes through the API server.

## Command coverage matrix

Bootstrap operations mapped to the DSL. Non-goals are listed in `ROADMAP.md`.

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
| `helm rollback` | none; `atomic:` covers failed upgrades, re-applying the spec is the recovery path | non-goal |
| `kubectl apply --prune` | none; pruning is reconciliation (Argo/Flux), `delete:` covers explicit removal | non-goal |
| `kubectl label` / `annotate` | `patch:` (or `apply:` a minimal manifest; existing objects are merge-patched) | **v1 core** |
| `kubectl scale` | `patch:` `spec.replicas` (or `apply:` a minimal manifest) | **v1 core** |
| `kubectl exec` / `cp` / `port-forward` | none; interactive, out of scope | non-goal |
| `kubectl get/describe` as output | none; `plan`/`status` cover reads | non-goal |

## Appendix: design decisions vs the v0 prototype

v1 is a redesign of a v0 prototype. Decisions recorded so they aren't
reopened:

- **`kind: Khook`** (was `ClusterBootstrap`) with `apiVersion: khook.io/v1`.
- **The action key is the type**; no `type:` field. A step has exactly one
  action key (`helm:`, `apply:`, `delete:`, ...; schema: oneOf).
- **`steps:` / `needs:`** replace v0's `operations:` / `dependsOn:`.
- **Top-level `defaults:`** replaces `config.defaults`.
- **No `exec` catch-all**; `wait:` and `rollout:` are step types.
- **Flat `helm:`**: `repo:` is just the URL (the SDK needs no repo cache or
  name); `atomic:`/`wait:` sit on the step (no `flags:` block); `release:`
  defaults to the step name.
- **`chart:` form sets the source**: one field covers repo charts, `oci://`
  references, and local paths; no `type:`/`sourceRef:` field.
- **Uninstall is `delete.release`**, not a `helm:` flag. Step types split by
  desired state (present vs absent), so `helm:` always means install or
  upgrade.
- **No `reuseValues`**: carrying a previous release's values forward makes the
  result depend on cluster state, so the same spec could give different
  results. The spec is the only source of values; `skipIf: installed` covers
  "don't touch an existing release".
- **No `helm rollback`**: `atomic:` handles failed upgrades; otherwise recover
  by re-applying a known-good spec.
- **`values:` is a plain map**; `valuesFrom:` is a Flux-style list for
  external values. `--set`-style overrides belong on the CLI, not in the spec.
- **`manifests:` is one list** of `- inline:` / `- file:` / `- url:` entries
  instead of three parallel fields.
- **Prefixed env vars**: only `KHOOK_VAR_*` is read (`--var-prefix` to
  override), so `PATH` and unrelated CI secrets can't leak into specs.
- **No `apply.prune`**: pruning is drift reconciliation, left to Argo/Flux;
  `delete:` covers explicit removal. kubectl's `--prune` is effectively
  deprecated and its ApplySet successor is still alpha.
- **`patch:` defaults to `strategic`** to match kubectl, though strategic
  fails on CRs (the error suggests `type: merge`). The body field is `patch:`
  (as in kustomize's `target:`/`patch:`) and takes a structured mapping or
  list, not an embedded string.
- **`apply.waitFor` is a plain string** that waits on exactly the applied
  objects, using `wait.for`'s grammar. Other resources or subsets need a
  `wait:` step.
- **`kustomize:` is a manifest source, not a step type**: one list, four
  source forms, local paths only (remote bases would need `git`).

<!-- {% endraw %} -->
