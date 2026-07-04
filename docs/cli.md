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
against the cluster. Prints one log line per state change and a final summary
table (step, type, status `ok`/`failed`/`skipped`, attempts, duration,
detail — the error for failures, the reason for skips).

### `khook plan -f spec.yaml`

Prints the execution plan — levels, step types, one-line action summaries,
`needs` edges — without touching the cluster. Variables are resolved, so the
plan shows final values.

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
still skipped). Skipped steps and the reason always appear in the summary.
Ctrl-C cancels the run; steps not yet started are reported skipped.

Re-running the same spec is safe: `helm:` upgrades instead of installing,
`apply:` patches existing resources, `delete:` treats absent resources as
success (`ignoreNotFound` defaults true), and the `skipIfInstalled` /
`skipIfExists` fields short-circuit steps whose outcome already holds.

## Logging

Structured `log/slog` output. `--log-format json` emits one JSON object per
line for machine consumption; the summary table always goes to stdout,
logs to stderr.
