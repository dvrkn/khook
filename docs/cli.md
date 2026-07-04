# khook CLI reference

One binary, five subcommands. Cluster access follows standard kubeconfig
loading rules (`KUBECONFIG`, `~/.kube/config`).

## Global flags

| Flag | Default | Notes |
|---|---|---|
| `--kubeconfig` | standard loading rules | path to a kubeconfig file |
| `--context` | current context | kubeconfig context to use |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` (`debug` includes Helm SDK output) |
| `--log-format` | `text` | `text` or `json`; logs go to stderr |

## Spec flags (`apply`, `plan`, `validate`)

| Flag | Notes |
|---|---|
| `-f, --file` | path to the Khook spec (required) |
| `--set NAME=value` | set a variable (repeatable) |
| `--var-file vars.yaml` | flat `NAME: value` YAML map |
| `--var-prefix` | env-var prefix consumed as variables (default `KHOOK_VAR_`) |

Variable precedence: `--set` > `--var-file` > prefixed env >
`${NAME:-default}` written in the spec. A `${NAME}` with no source and no
default fails validation, reporting **all** missing variables at once.

## Commands

### `khook apply -f spec.yaml`

Parses, validates, resolves the DAG, then executes steps in parallel levels
against the cluster. Output depends on where it runs:

- **Interactive terminal** (stdout is a TTY): one live status line per step
  in DAG order — `pending → running → ok/failed/skipped` — with spinner,
  elapsed time, and retry attempt, redrawn in place. Full error details for
  failed steps print after the run. Step-level logging is silenced (raised to
  `warn`) so it doesn't garble the display; pass `--log-level` explicitly to
  override. `NO_COLOR` disables colors; `TERM=dumb` disables live rendering.
- **Non-interactive** (piped, CI): one log line per state change and a final
  summary table (step, type, status `ok`/`failed`/`skipped`, attempts,
  duration, detail — the error for failures, the reason for skips).
- **`-o, --output json`**: instead of the table, the final results print as
  one JSON document on stdout — run `name`, run `status` (`ok`/`failed`),
  and per-step `name`, `type`, `status`, `attempts`, `durationMs`, `error`,
  `skipReason`. Logs still stream to stderr, so `khook apply -o json 2>/dev/null`
  is clean JSON for CI. Exit codes are unchanged.

### `khook plan -f spec.yaml`

Shows what `apply` would do. Prints the execution plan — levels, step types,
one-line action summaries, `needs` edges — and checks every step against the
live cluster (reads only; plan never mutates anything):

| Step type | Predicted action |
|---|---|
| `helm:` | `install` (no release history), `upgrade` (shows current revision, chart version, status → target chart), or `skip` (`skipIfInstalled`) |
| `apply:` | `create` / `configure`, listing which objects are new vs existing, or `skip` (`skipIfExists`) |
| `delete:` | `delete` (named object or selector match count) or `no-op` (already absent) |
| `wait:` | `no-op` when the condition already holds, `wait` otherwise (shows how many objects currently match) |
| `rollout:` | `restart`, `no-op` (rollout already complete), or `wait` |

A step whose `when:` condition is false is reported `skip` (with the
condition) without touching the cluster — `--offline` shows it too. Steps
that can't be assessed yet — e.g. a CRD or namespace an earlier step creates,
a missing values file — are reported as `unknown` with the reason, not
treated as errors. A closing `Plan:` line totals the actions. Variables
are resolved, so the plan shows final values.

`--diff` adds kubectl-diff-style unified object diffs under each step that
would change something (`install`/`upgrade`/`create`/`configure`):

- `apply:` steps send each manifest as a **server-side dry-run** of the same
  request `apply` would make (create, merge patch, or server-side apply), so
  the diff includes server defaulting and admission effects. Managed fields
  are hidden, like `kubectl diff`.
- `helm:` steps dry-run render the chart (server dry-run: real cluster
  capabilities, nothing stored) and diff it against the manifest of the
  release's last revision. An install diffs against empty — all additions.
- `delete:` / `wait:` / `rollout:` steps have no rendered objects; the plan
  line already says what happens.

An unchanged step prints `diff: no changes` — a re-run of an already-applied
spec shows no diffs at all. A step whose diff can't be computed (chart
download failure, kind not on the cluster yet) prints `diff: unavailable`
with the reason, and the plan still succeeds. Like plan itself, `--diff`
never mutates the cluster; it cannot be combined with `--offline`.

`--offline` skips all cluster access and prints the DAG-only plan (no
kubeconfig needed — useful in CI). An unreachable cluster without `--offline`
exits 1.

### `khook validate -f spec.yaml`

Parse + validation only (variables, schema, action keys, DAG cycles). No
cluster access. Prints all problems at once, not just the first.

### `khook schema`

Prints the JSON Schema for the spec (draft 2020-12), including the
exactly-one-action-key constraint on steps.

### `khook version`

Version, commit, build date, platform (set via `-ldflags -X
github.com/dvrkn/khook/internal/cli.Version=...` at release time).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | execution failure (a step failed, cluster unreachable) |
| 2 | validation failure (bad spec, unresolved variables, DAG cycle, bad flags) |

## Execution semantics

Steps run in topologically sorted parallel levels. Per-step `timeout` bounds
each attempt; `retries`/`retryDelay` control re-attempts; `onError: fail`
(default) lets running steps finish but starts nothing new, `onError:
continue` keeps scheduling other branches (dependents of the failed step are
still skipped). A step whose `when:` condition is false is skipped but still
satisfies its dependents' `needs` (see [`docs/dsl.md`](dsl.md)). Skipped
steps and the reason always appear in the summary. Ctrl-C cancels the run;
steps not yet started are reported skipped.

Re-running the same spec is safe: `helm:` upgrades instead of installing,
`apply:` patches existing resources, `delete:` treats absent resources as
success (`ignoreNotFound` defaults true), and the `skipIfInstalled` /
`skipIfExists` fields short-circuit steps whose outcome already holds.

## Logging

Structured `log/slog` output. `--log-format json` emits one JSON object per
line for machine consumption; the summary (table, JSON results, or live
progress lines) goes to stdout, logs to stderr. When `apply` renders live
progress on a TTY, the default log level is raised to `warn` unless
`--log-level` was given explicitly.
