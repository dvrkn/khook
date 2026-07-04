---
title: Getting started
layout: docs
permalink: /docs/
description: Install khook, bootstrap a local k3d cluster, and write your first spec.
---

# Getting started

khook is a single static binary — the Kubernetes and Helm SDKs are compiled
in, so there is nothing to install on the machine that runs it but khook
itself. No `kubectl`, no `helm`, no runtime dependencies.

## Install

With [Go](https://go.dev/):

```console
$ go install github.com/dvrkn/khook/cmd/khook@latest
```

## Try it on a throwaway cluster

[k3d](https://k3d.io/) gives you a disposable local cluster in seconds. Save
this two-step spec as `quickstart.yaml`:

```yaml
# yaml-language-server: $schema=https://khook.io/schema/v1/khook.json
apiVersion: khook.io/v1
kind: Khook
metadata:
  name: quickstart
steps:
  - name: create-namespace
    apply:
      manifests:
        - inline: |
            apiVersion: v1
            kind: Namespace
            metadata:
              name: ingress
  - name: ingress-nginx
    needs: [create-namespace]
    helm:
      chart: ingress-nginx
      repo: https://kubernetes.github.io/ingress-nginx
      version: 4.8.3
      namespace: ingress
```

```console
$ k3d cluster create dev
$ khook apply -f quickstart.yaml
✓ create-namespace (apply)  18ms
✓ ingress-nginx (helm)  21.457s
```

On a terminal each step is a live status line (pending → running →
ok/failed/skipped, with spinner and elapsed time). In CI you get plain logs
and a summary table instead, or `--output json` for machine-readable results.

Run the same command again: the namespace apply converges, the Helm release
upgrades (or no-ops), and nothing breaks. Idempotent re-runs are the core
promise — you can wire khook into every `terraform apply`.

## Your first spec

A spec is a `khook.io/v1` document with a list of **steps**. Each step has
exactly one action key — the key implies the type, there is no `type:` field:

```yaml
# yaml-language-server: $schema=https://khook.io/schema/v1/khook.json
apiVersion: khook.io/v1
kind: Khook
metadata:
  name: bootstrap

defaults:          # fallbacks for every step
  timeout: 5m
  retries: 0
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
    needs: [cilium]        # DAG edge — runs only after cilium succeeds
    wait:
      for: condition=Ready
      on: pods
      allNamespaces: true
```

Steps are topologically sorted and run in **parallel levels**: everything
with satisfied `needs` runs concurrently. Dependency cycles are a validation
error — caught before anything touches the cluster.

The first line wires your editor to the [JSON Schema](../schema/v1/khook.json)
for validation and autocomplete via yaml-language-server.

## Validate, plan, then apply

khook is designed to be safe to point at a real cluster:

```console
$ khook validate -f bootstrap.yaml   # parse + validate only, no cluster access
$ khook plan -f bootstrap.yaml       # read-only: predicts install/upgrade/no-op per step
$ khook plan -f bootstrap.yaml --diff  # + rendered object diffs via server-side dry-run
$ khook apply -f bootstrap.yaml
```

`plan` checks every step against the live cluster without mutating anything;
`--offline` skips cluster access entirely for CI. `graph` emits the step DAG
as Mermaid or Graphviz DOT for docs and review. Exit codes are distinct:
`2` for validation problems, `1` for execution failures — see the
[CLI reference](cli.md).

## Variables and secrets

Specs are parameterized with `${NAME}` / `${NAME:-default}` references,
resolved before parsing. Values come from (in precedence order) `--set`,
`--var-file`, `KHOOK_SECRET_*` env vars, `KHOOK_VAR_*` env vars, then in-spec
defaults:

```console
$ export KHOOK_VAR_ENV=prod
$ export KHOOK_SECRET_ECR_TOKEN="$(aws ecr get-login-password)"
$ khook apply -f bootstrap.yaml --set APP_NAME=payments
```

Only prefixed environment variables are consumed, so unrelated environment
(PATH, CI secrets) can't leak into specs. `KHOOK_SECRET_*` values are
additionally redacted from all khook output. Values can be piped through
[sprig](https://github.com/Masterminds/sprig) functions helm-style
(`${APP_NAME | lower | trunc 63}`), and steps can be conditional with
`when:` [CEL](https://cel.dev) expressions. The full rules are in the
[DSL specification](dsl.md).

## Where to go next

- **[DSL specification](dsl.md)** — the normative field-level reference for
  all seven step types, variables, pipelines, and `when:` conditions.
- **[CLI reference](cli.md)** — commands, flags, variable precedence, exit
  codes, execution semantics.
- **[Examples](examples.md)** — from a two-step demo to a production-shaped
  EKS bootstrap.
- **[khook vs Terraform](vs-terraform.md)** — where each tool belongs.
