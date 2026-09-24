---
title: Getting started
layout: docs
permalink: /docs/
description: Install khook, bootstrap a local k3d cluster, and write your first spec.
---

# Getting started

khook is a single static binary with the Kubernetes and Helm SDKs compiled
in. It does not need `kubectl`, `helm`, or any other runtime dependency.

## Install

With [Go](https://go.dev/):

```console
$ go install github.com/dvrkn/khook/cmd/khook@latest
```

## Try it on a throwaway cluster

Create a local cluster with [k3d](https://k3d.io/) and save this spec as
`quickstart.yaml`:

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

On a terminal, each step gets a live status line (pending, running, then
ok/failed/skipped, with elapsed time). In CI, khook prints plain logs and a
summary table, or JSON with `--output json`.

Run the command again: the namespace apply converges and the Helm release
upgrades or no-ops. Re-runs are always safe.

## Your first spec

A spec is a `khook.io/v1` document with a list of **steps**. Each step has
exactly one action key, and the key sets the step type:

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
    needs: [cilium]        # runs only after cilium succeeds
    wait:
      for: condition=Ready
      on: pods
      allNamespaces: true
```

Steps are sorted topologically and run in **parallel levels**: every step
whose `needs` are satisfied runs concurrently. A dependency cycle is a
validation error.

The first line points yaml-language-server at the
[JSON Schema](schema/v1/khook.json) for editor validation and autocomplete.

## Validate, plan, apply

```console
$ khook validate -f bootstrap.yaml       # parse and validate; no cluster access
$ khook plan -f bootstrap.yaml           # read-only: install/upgrade/no-op per step
$ khook plan -f bootstrap.yaml --diff    # plus object diffs via server-side dry-run
$ khook apply -f bootstrap.yaml
```

`plan` never mutates the cluster; `--offline` skips cluster access entirely.
`graph` prints the step DAG as Mermaid or Graphviz DOT. Exit code `2` means a
validation error, `1` an execution failure. See the [CLI reference](cli.md).

## Variables and secrets

`${NAME}` and `${NAME:-default}` are resolved before parsing. Sources, highest
precedence first: `--set`, `--var-file`, `KHOOK_SECRET_*` env vars,
`KHOOK_VAR_*` env vars, in-spec defaults.

```console
$ export KHOOK_VAR_ENV=prod
$ export KHOOK_SECRET_ECR_TOKEN="$(aws ecr get-login-password)"
$ khook apply -f bootstrap.yaml --set APP_NAME=payments
```

Only prefixed env vars are read, so `PATH` and unrelated CI secrets never reach
a spec. `KHOOK_SECRET_*` values are also redacted from khook's output. Values
can go through [sprig](https://github.com/Masterminds/sprig) functions
(`${APP_NAME | lower | trunc 63}`), and `when:` takes a [CEL](https://cel.dev)
expression to make a step conditional. Details: [DSL specification](dsl.md).

## Next

- **[DSL specification](dsl.md)**: field reference for the seven step types,
  variables, pipelines, and `when:`.
- **[CLI reference](cli.md)**: commands, flags, exit codes, execution
  semantics.
- **[Examples](examples.md)**: from a two-step demo to an EKS bootstrap.
- **[khook vs Terraform](vs-terraform.md)**: where each tool fits.
