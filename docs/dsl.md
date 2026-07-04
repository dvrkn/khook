# khook DSL v1 — specification

Normative spec for `apiVersion: khook.dvrkn.com/v1`, `kind: Khook`.
`examples/*.yaml` must always validate against this document; where they
disagree, this document wins. Anything marked **(roadmap)** is not part of v1
core — see the command coverage matrix at the bottom and `roadmap.md`.

## Document envelope

```yaml
apiVersion: khook.dvrkn.com/v1   # required, fixed
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
3. Environment variables prefixed `KHOOK_VAR_` (`KHOOK_VAR_FOO` → `${FOO}`);
   prefix configurable with `--var-prefix`
4. `${NAME:-default}` fallback written in the spec

A `${NAME}` with no source and no default is a validation error; all missing
variables are reported at once.

## `defaults:`

Fallbacks for the per-step fields of the same name.

| Field | Type | Default | Notes |
|---|---|---|---|
| `timeout` | duration | `5m` | per-step execution timeout |
| `retries` | int | `0` | retry attempts after a failed try |
| `retryDelay` | duration | `10s` | pause between tries |
| `onError` | `fail` \| `continue` | `fail` | `fail` stops scheduling new steps; `continue` marks the step failed and keeps going |

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
                          #   helm | apply | delete | wait | rollout | job
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
| `chart` | string | yes | | chart name in `repo` |
| `repo` | URL | yes | | HTTP(S) chart repository URL; no repo "name" needed |
| `version` | string | no | latest | exact chart version (recommended: always pin) |
| `release` | string | no | step `name` | Helm release name |
| `namespace` | string | no | `default` | target namespace |
| `createNamespace` | bool | no | `false` | create namespace if missing |
| `skipIfInstalled` | bool | no | `false` | skip (success) if the release already exists, regardless of version/values |
| `atomic` | bool | no | `false` | roll back on failure |
| `wait` | bool | no | `false` | wait for resources ready before step succeeds |
| `values` | map | no | | inline values (merged over chart defaults) |
| `valuesFrom` | list | no | | ordered list of `- file: path` entries; later entries and `values` override earlier ones |

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

**(roadmap)** `oci://` charts, local chart paths, `- url:` in `valuesFrom`,
`reuseValues`, uninstall, rollback, private repo auth.

## `apply:` — declaratively apply manifests

Server-side-agnostic `kubectl apply` via the dynamic client. Multi-document
YAML is supported in every source.

| Field | Type | Required | Default | Notes |
|---|---|---|---|---|
| `manifests` | list | yes | | ordered list of sources, each exactly one of `inline:` (YAML string), `file:` (path), `url:` (HTTP(S)) |
| `namespace` | string | no | | default namespace for namespace-less namespaced resources |
| `createNamespace` | bool | no | `false` | create `namespace` if missing |
| `skipIfExists` | bool | no | `false` | skip (success) if all resources already exist |
| `serverSide` | bool | no | `false` | server-side apply |

```yaml
apply:
  namespace: argocd
  manifests:
    - inline: |
        apiVersion: argoproj.io/v1alpha1
        kind: Application
        ...
    - file: ./manifests/extra.yaml
    - url: https://example.com/manifest.yaml
```

**(roadmap)** `prune`, `patch`, kustomize source, `waitFor:` shorthand
(apply + wait in one step).

## `delete:` — remove resources

Two mutually exclusive forms.

**By manifests** (delete what these define):

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

## `wait:` — block until a condition holds

| Field | Type | Required | Notes |
|---|---|---|---|
| `for` | string | yes | `condition=<Name>`, `condition=<Name>=<value>`, or `delete` |
| `on` | string | yes | resource type (`pods`) or `kind/name` (`deployment/argocd-server`) |
| `namespace` | string | no | mutually exclusive with `allNamespaces` |
| `allNamespaces` | bool | no | |
| `selector` | string | no | label selector |

The step-level `timeout` bounds the wait — there is no separate `wait.timeout`.

```yaml
wait:
  for: condition=Ready
  on: pods
  allNamespaces: true
```

**(roadmap)** `for: jsonpath=...`.

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

**(roadmap)** output capture — a `job` publishing small values that later
steps consume.

## Command coverage matrix

What a "cloud-init for k8s" needs, mapped to the DSL. Non-goals excluded
(see roadmap.md).

| CLI equivalent | khook | Status |
|---|---|---|
| `helm repo add` + `helm install`/`upgrade` | `helm:` | **v1 core** |
| `helm install --atomic/--wait/--create-namespace` | `helm.atomic/wait/createNamespace` | **v1 core** |
| `helm install -f values.yaml --set k=v` | `helm.valuesFrom` / `helm.values` | **v1 core** |
| `kubectl apply -f file/url/-` | `apply:` | **v1 core** |
| `kubectl apply --server-side` | `apply.serverSide` | **v1 core** |
| `kubectl delete -f` / by selector | `delete:` | **v1 core** |
| `kubectl wait --for=condition=...` | `wait:` | **v1 core** |
| `kubectl rollout restart/status` | `rollout:` | **v1 core** |
| `kubectl create namespace` | `createNamespace: true` / `apply:` | **v1 core** |
| arbitrary in-cluster commands | `job:` (container to completion) | **v1 core** |
| `helm install oci://...` / local chart | `helm.chart: oci://...` / path | roadmap P2 |
| `helm uninstall` / `rollback` | `helm.uninstall` (shape TBD) | roadmap P2 |
| `kubectl apply -k` (kustomize) | `apply.kustomize` (shape TBD) | roadmap P2 |
| `kubectl apply --prune` / `patch` | `apply.prune` / `patch:` | roadmap P2 |
| `kubectl label` / `annotate` | `label:` / `annotate:` (shape TBD) | roadmap P2 |
| `kubectl scale` | `scale:` (shape TBD) | roadmap P2 |
| `kubectl wait --for=jsonpath=` | `wait.for: jsonpath=...` | roadmap P2 |
| `kubectl exec` / `cp` / `port-forward` | — interactive, out of scope | non-goal |
| `kubectl get/describe` as output | — read paths belong to `plan`/`status` | non-goal |

## Appendix: design decisions vs the v0 prototype

The v1 DSL is a from-scratch redesign of a proven v0 prototype (its
`examples/` survive in-tree). Decisions made in the redesign, recorded so
they are not re-litigated:

- **`kind: Khook`** (was `ClusterBootstrap`) with `apiVersion: khook.dvrkn.com/v1`.
- **Action key implies the type** — no `type:` discriminator. A step has exactly
  one action key (`helm:`, `apply:`, `delete:`, ... — schema: oneOf).
- **`steps:` / `needs:`** replace v0's `operations:` / `dependsOn:`.
- **Top-level `defaults:`** replaces `config.defaults`.
- **v0's `exec` grab-bag is gone** — `wait:` and `rollout:` are first-class.
- **Helm flattened**: `repo:` is just the URL (no repository name — the SDK
  doesn't need a repo cache); `atomic:`/`wait:` sit directly on the op (no
  `flags:` block); `release:` defaults to the step name.
- **`values:` is a plain map** (the common case); `valuesFrom:` is a Flux-style
  list for external values. `--set`-style overrides live on the CLI, not in
  the spec.
- **`manifests:` is one list** of `- inline:` / `- file:` / `- url:` entries,
  replacing three parallel fields.
- **Prefixed environment variables**: only env vars starting with `KHOOK_VAR_`
  are consumed (`--var-prefix` to override), preventing unrelated environment
  (PATH, CI secrets) from leaking into specs.
