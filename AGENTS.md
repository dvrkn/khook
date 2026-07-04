# AGENTS.md

Guidance for AI agents working in this repository.

## What this is

**khook** — "cloud-init for Kubernetes." A single Go binary embedding the
Kubernetes and Helm SDKs that initializes a freshly created cluster from a
declarative YAML spec (`apiVersion: khook.dvrkn.com/v1`, `kind: Khook`).

## Current state: pre-code, planning done

This is a **from-scratch v1 rewrite**. There is no Go code in the tree yet.
Read in this order before doing anything:

1. **`roadmap.md`** — vision, phased plan, non-goals, open questions. Source of truth.
2. **`docs/dsl.md`** — normative DSL spec: field-level reference for the v1 step
   types and the kubectl/helm command coverage matrix.
3. **`examples/*.yaml`** — spec-by-example. Changes must keep these valid against
   `docs/dsl.md`; `real-case.yaml` is the benchmark a v1 must handle cleanly.

## Decisions already made (don't re-litigate)

- Name: `khook`; module path, binary, and docs all use it. Spec kind: `Khook`.
- DSL shape: action key implies type (`helm:`/`apply:`/`delete:`/`wait:`/`rollout:`),
  `steps:` + `needs:`, top-level `defaults:` — details in `docs/dsl.md`.
- Variables: `${VAR}` / `${VAR:-default}`; env vars consumed only with the
  `KHOOK_VAR_` prefix (`--var-prefix` to override).
- Dev/test platform: **k3d** (E2E tests spin up k3d clusters).
- One binary, zero runtime deps: SDKs only, never shell out to kubectl/helm.

## Conventions

- Commits and PRs must not carry AI attribution (no `Co-Authored-By: Claude`,
  no "Generated with" lines).
