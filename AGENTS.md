# AGENTS.md

Guidance for AI agents working in this repository.

## What this is

**khook** — declarative bootstrap for Kubernetes. A single Go binary embedding the
Kubernetes and Helm SDKs that initializes a freshly created cluster from a
declarative YAML spec (`apiVersion: khook.io/v1`, `kind: Khook`).

## Current state: v1 core implemented

The v1 core (spec parser, DAG engine, the five executors, CLI, unit tests,
k3d E2E) is implemented. Read in this order before doing anything:

1. **`docs/dsl.md`** — normative DSL spec: field-level reference for the v1 step
   types and the kubectl/helm command coverage matrix.
2. **`docs/cli.md`** — CLI reference: commands, flags, variables, exit codes.
3. **`ROADMAP.md`** — future work only (vision, phased plan, non-goals, open
   questions).
4. **`examples/*.yaml`** — spec-by-example. Changes must keep these valid against
   `docs/dsl.md`; `real-case.yaml` is the benchmark a v1 must handle cleanly.

Layout: `cmd/khook` (main), `internal/spec` (types/parse/validate/variables),
`internal/engine` (DAG levels + runner), `internal/ops` (executors),
`internal/kube` (clients), `internal/cli` (cobra commands), `tests/e2e.sh`
(k3d end-to-end; run it after touching engine or executors).

## Decisions already made (don't re-litigate)

- Name: `khook`; module path, binary, and docs all use it. Spec kind: `Khook`.
- DSL shape: action key implies type (`helm:`/`apply:`/`delete:`/`patch:`/`wait:`/`rollout:`/`job:`),
  `steps:` + `needs:`, top-level `defaults:` — details in `docs/dsl.md`.
- Variables: `${VAR}` / `${VAR:-default}` / `${VAR|sprig pipeline}` (hermetic
  sprig set, values never template-parsed — see `docs/dsl.md`); env vars
  consumed only with the `KHOOK_VAR_` prefix (`--var-prefix` to override).
  `KHOOK_SECRET_` prefix (`--secret-prefix`) is the same plus output
  redaction, including of pipeline-derived values.
- Variables are **env-first**: values pre-exist in the environment (wrapper
  script / CI / toolbox image fetches them); no in-spec cloud resolvers, no
  host `exec:` step — `job:` is the escape hatch.
- Dev/test platform: **k3d** (E2E tests spin up k3d clusters).
- One binary, zero runtime deps: SDKs only, never shell out to kubectl/helm.
- Binary size: release builds go through `make build` (`CGO_ENABLED=0`,
  `-trimpath`, `-ldflags "-s -w"` + version stamping) → ~68 MB. The floor is
  structural: helm v4's `pkg/action`/`pkg/kube` link the full typed
  `k8s.io/client-go` clientset and all of `k8s.io/api`, and `text/template`
  (helm's render engine) calls `reflect.MethodByName`, which disables linker
  method pruning globally. Evaluated and rejected (2026-07-03): stubbing
  helm's WASM-plugin runtime + SQL storage driver via `go build -overlay`
  (only ~3 MB saved) and UPX compression.

## Conventions

- Commits and PRs must not carry AI attribution (no `Co-Authored-By: Claude`,
  no "Generated with" lines).
- **`roadmap.md` holds only future work.** When a feature ships, document it in
  `docs/` (and README where relevant) and remove it from the roadmap in the
  same change — the roadmap is never a record of what exists.
