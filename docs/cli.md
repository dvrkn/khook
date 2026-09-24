---
title: CLI reference
layout: docs
permalink: /cli/
description: khook commands, flags, variable precedence, exit codes, and execution semantics.
---
<!-- {% raw %} — body is Liquid-free on the website build; invisible on GitHub -->
# khook CLI reference

Cluster access follows standard kubeconfig loading rules (`KUBECONFIG`,
`~/.kube/config`).

## Global flags

| Flag | Default | Notes |
|---|---|---|
| `--kubeconfig` | standard loading rules | path to a kubeconfig file |
| `--context` | current context | kubeconfig context to use |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` (`debug` includes Helm SDK output) |
| `--log-format` | `text` | `text` or `json`; logs go to stderr |

## Spec flags (`apply`, `destroy`, `plan`, `validate`, `graph`, `status`)

| Flag | Notes |
|---|---|
| `-f, --file` | path to the Khook spec (required) |
| `--set NAME=value` | set a variable (repeatable) |
| `--var-file vars.yaml` | flat `NAME: value` YAML map |
| `--var-prefix` | env-var prefix read as variables (default `KHOOK_VAR_`) |
| `--secret-prefix` | env-var prefix read as **secret** variables (default `KHOOK_SECRET_`) |

Precedence: `--set` > `--var-file` > secret env > prefixed env >
`${NAME:-default}` in the spec. A `${NAME}` with no source and no default
fails validation; all missing variables are reported at once.

**Secret variables.** `KHOOK_SECRET_TOKEN=x` resolves `${TOKEN}` exactly like
`KHOOK_VAR_TOKEN=x`, and the value is also printed as `***` everywhere khook
writes: logs, `plan` output, `plan --diff` manifests, summaries, errors.
Sprig pipeline outputs of secret variables (`${TOKEN|b64enc}`, see
[DSL](dsl.md#pipelines--sprig-functions-on-values)) are masked too. Masking
covers khook's own output only: the value still reaches the cluster and is
visible to anything that can read the created resources. It is textual and
best-effort (a value split across lines in rendered YAML may not match). Keep
long-lived secrets out of specs (use external-secrets) and use this for
bootstrap-time secrets.

## Commands

### `khook apply -f spec.yaml`

Parses, validates, resolves the DAG, and runs steps in parallel levels. Output
depends on the environment:

- **Terminal** (stdout is a TTY): one live status line per step in DAG order
  (`pending → running → ok/failed/skipped`) with spinner, elapsed time, and
  retry attempt. Error details for failed steps print after the run. Step
  logging is raised to `warn` so it doesn't break the display; an explicit
  `--log-level` overrides that. `NO_COLOR` disables colors; `TERM=dumb`
  disables live rendering.
- **Non-interactive** (piped, CI): one log line per state change, then a
  summary table: step, type, status (`ok`/`failed`/`skipped`), attempts,
  duration, and detail (error for failures, reason for skips).
- **`-o, --output json`**: the results print as one JSON document on stdout
  instead of the table: run `name` and `status` (`ok`/`failed`), and per step
  `name`, `type`, `status`, `attempts`, `durationMs`, `error`, `skipReason`.
  Logs still go to stderr, so `khook apply -o json 2>/dev/null` yields clean
  JSON. Exit codes are unchanged.

With [`state:`](dsl.md#state--the-run-state-record) enabled, apply also
maintains the run-state record:

- It loads the record Secret before the first step; an unwritable record fails
  the run up front.
- It skips steps a previous run completed **whose inputs are unchanged**. The
  check is per step, so editing one step re-runs only that step. Resumed steps
  are reported `skipped` with reason
  `unchanged since it succeeded in a previous run (state record)`.
- It journals each step outcome as it happens and stamps the final run status.
- A run whose steps all succeed but whose record cannot be written **exits 1**.

### `khook destroy -f spec.yaml`

Tears down what the spec created in **reverse dependency order**: a step's
resources are removed before those of the steps it `needs`, and independent
branches run in parallel. Intended for dev clusters and CI. Output modes,
retries, timeouts, and `onError` work as in `apply`.

| Step type | Teardown |
|---|---|
| `helm:` | uninstalls the release and waits until its resources are gone |
| `apply:` | deletes the objects its manifests describe, last manifest first, waiting until each is gone |
| `job:` | deletes the step's Job and its pods (refuses a Job not managed by khook) |
| `delete:` / `patch:` | skipped; khook does not restore deletions or revert patches |
| `wait:` / `rollout:` | skipped; nothing was created |

- **Idempotent**: resources already gone count as success, so a partial
  destroy can be re-run.
- A step whose `when:` is false is skipped, as in `apply`; pass the same
  `--set`/env to get the same steps. Skipped steps still satisfy the teardown
  order.
- **Namespaces created via `createNamespace: true` are kept**, since they may
  hold resources khook did not create. Delete them (or the cluster) yourself.
- With [`state:`](dsl.md#state--the-run-state-record) enabled, a fully
  successful destroy also deletes the record Secret, so the next `apply`
  starts from scratch. A successful teardown that cannot remove the record
  exits 1.
- A failed step stops new teardown work (`onError: fail` default); steps
  whose teardown depended on it are reported skipped, and the exit code is 1.

### `khook plan -f spec.yaml`

Shows what `apply` would do: levels, step types, one-line action summaries,
`needs` edges, and a per-step check against the live cluster. Plan only reads.

| Step type | Predicted action |
|---|---|
| `helm:` | `install` (no release history), `upgrade` (current revision, chart version, status → target chart), or `skip` (`skipIf: installed`) |
| `apply:` | `create` / `configure`, listing new vs existing objects, or `skip` (`skipIf: exists`) |
| `delete:` | `delete` (named object or selector match count) or `no-op` (already absent) |
| `patch:` | `configure` (target exists) or `unknown` (target must exist by the time the step runs) |
| `wait:` | `no-op` if the condition already holds, otherwise `wait` (with the current match count) |
| `rollout:` | `restart`, `no-op` (rollout complete), or `wait` |
| `job:` | `run` (first run or replacing a previous Job) or `skip` (`skipIf: succeeded`) |

A step whose `when:` is false is reported `skip` with the condition, without
cluster access (also under `--offline`). Steps that can't be assessed yet (a
CRD or namespace an earlier step creates, a missing values file) are reported
`unknown` with the reason, not as errors. A final `Plan:` line totals the
actions. Variables are resolved, so the plan shows final values.

`--diff` adds unified object diffs, `kubectl diff`-style, under each step
that would change something (`install`/`upgrade`/`create`/`configure`):

- `apply:` sends each manifest as a **server-side dry-run** of the request
  `apply` would make (create, merge patch, or server-side apply), so the diff
  includes defaulting and admission. Managed fields are hidden.
- `helm:` renders the chart with a server dry-run (real cluster
  capabilities, nothing stored) and diffs it against the release's last
  revision. An install diffs against empty.
- `patch:` sends the patch as a **server dry-run** and diffs the result
  against the live object.
- `delete:` / `wait:` / `rollout:` / `job:` have no rendered objects; the plan
  line says what happens.

An unchanged step prints `diff: no changes`, so re-planning an applied spec
shows no diffs. If a diff can't be computed (chart download failure, kind not
on the cluster yet), the step prints `diff: unavailable` with the reason and
the plan still succeeds. `--diff` never mutates the cluster and cannot be
combined with `--offline`.

`--offline` skips cluster access and prints the DAG-only plan; no kubeconfig
needed. Without `--offline`, an unreachable cluster exits 1.

### `khook validate -f spec.yaml`

Parses and validates (variables, schema, action keys, DAG cycles) without
cluster access. Reports all problems at once.

### `khook status -f spec.yaml`

Reads the run-state record ([`state:`](dsl.md#state--the-run-state-record))
and shows the last run. Read-only. The record location is derived from the
spec as in `apply`, so pass the same spec and the same `--set`/env.

```text
spec:    prod-bootstrap
record:  secret kube-system/khook-state-prod-bootstrap (khook v0.3.0)
run:     failed, started 2026-07-04T10:00:00+03:00, updated 2026-07-04T10:04:12+03:00
spec has changed since this run — 1 unchanged completed step(s) still resume on the next apply

STEP     TYPE   STATUS   ATTEMPTS  DURATION  DETAIL
cni      helm   ok       1         1m12s
ingress  helm   failed   3         2m40s     context deadline exceeded
smoke    job    skipped                      needs "ingress" which did not succeed
```

The summary line predicts the next apply: which completed steps will still
resume. A completed step whose inputs changed shows
`input changed — will re-run` in the detail column; one removed from the spec
shows `no longer in the spec`.

- A spec without `state:` is a validation error (exit 2).
- **No record found exits 0** with a message. With `-o, --output json` it
  prints `{"found": false}`, otherwise
  `{"found": true, "specChanged": ..., "resumableSteps": [...], "record": {...}}`
  (`resumableSteps` lists the steps the next apply will skip).
- A step with status `-` was started but never finished (crash or
  interruption). `runStatus` may also still read `running`; it is a marker,
  not a lock.

### `khook graph -f spec.yaml`

Prints the step DAG without cluster access. The default is a
[Mermaid](https://mermaid.js.org/) flowchart (GitHub renders it in a
` ```mermaid ` fence); `--format dot` prints Graphviz DOT
(`khook graph -f spec.yaml --format dot | dot -Tsvg > dag.svg`).

Nodes show step name and type; edges are `needs`. Variables are resolved
(same spec flags as `apply`), so a step whose `when:` is false is drawn
dashed/gray and labeled `skipped`. The DAG is validated first; a cycle exits
2, as in `validate`.

```
$ khook graph -f examples/multi-app.yaml
flowchart TD
    n0["create-monitoring-namespace (apply)"]
    n1["prometheus (helm)"]
    n2["create-app-namespace (apply)"]
    n3["deploy-sample-app (apply)"]
    n0 --> n1
    n2 --> n3
```

### `khook schema`

Prints the spec's JSON Schema (draft 2020-12), including constraints type
reflection can't express: fixed `apiVersion`/`kind`, exactly one action key
per step, exactly one source key per manifest/values entry, exactly one of
`restart`/`status` (rollout) and `manifests`/`resource`/`release` (delete),
and the `onError`/patch-`type` value sets.

The same schema is committed at [`docs/schema/v1/khook.json`](schema/v1/khook.json)
for editors (`# yaml-language-server: $schema=...`). `make schema`
regenerates it, and a unit test fails if it drifts from the spec types. The
schema validates specs as written: `${VAR}` references sit inside string
values and pass through.

### `khook version`

Prints version, commit, build date, and platform (set at release time via
`-ldflags -X github.com/dvrkn/khook/internal/cli.Version=...`).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | execution failure (a step failed, cluster unreachable) |
| 2 | validation failure (bad spec, unresolved variables, DAG cycle, bad flags) |

## Execution semantics

Steps run in topologically sorted parallel levels. `timeout` bounds each
attempt; `retries`/`retryDelay` control re-attempts. `onError: fail`
(default) lets running steps finish and starts nothing new; `onError:
continue` keeps scheduling other branches (dependents of the failed step are
still skipped). A step whose `when:` is false is skipped but still satisfies
its dependents' `needs` (see [DSL](dsl.md#when--conditional-steps)). Skipped
steps and their reasons always appear in the summary. Ctrl-C cancels the run;
steps not yet started are reported skipped.

Re-running a spec is safe: `helm:` upgrades instead of installing, `apply:`
patches existing resources, `delete:` treats absent resources as success
(`ignoreNotFound` defaults to true), `job:` replaces the previous run's Job,
and each type's `skipIf` (`installed` / `exists` / `succeeded`) skips steps
whose outcome already holds.

## Logging

Structured `log/slog` output; `--log-format json` emits one JSON object per
line. The summary (table, JSON results, or live progress) goes to stdout,
logs go to stderr. When `apply` shows live progress on a TTY, the default log
level is `warn` unless `--log-level` is set.

<!-- {% endraw %} -->
