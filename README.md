<div align="center">

# khook

**Declarative bootstrap for Kubernetes.**

A single static binary that runs a DAG of `helm`, `apply`, `wait`, and related
steps against a freshly created cluster. The Kubernetes and Helm SDKs are
compiled in; it never calls `kubectl`, `helm`, or a shell.

[![CI](https://github.com/dvrkn/khook/actions/workflows/ci.yml/badge.svg)](https://github.com/dvrkn/khook/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Kubernetes SDK](https://img.shields.io/badge/client--go-v0.37-326CE5?logo=kubernetes&logoColor=white)](go.mod)
[![Helm SDK](https://img.shields.io/badge/helm-v4-0F1689?logo=helm&logoColor=white)](go.mod)
[![Status](https://img.shields.io/badge/status-pre--release-orange)](ROADMAP.md)

**[Docs](https://khook.io/) · [Quickstart](https://khook.io/docs/) · [DSL](https://khook.io/dsl/) · [CLI](https://khook.io/cli/) · [vs Terraform](https://khook.io/vs-terraform/)**

</div>

---

## Why

Terraform, eksctl, or CAPI creates the cluster; Argo CD or Flux manages it
once installed. The steps in between (CNI, CRDs, secrets tooling, the GitOps
controller, readiness waits) usually live in a shell script. khook replaces
that script with a spec:

```yaml
apiVersion: khook.io/v1
kind: Khook
metadata:
  name: bootstrap
steps:
  - name: cilium
    helm:
      chart: cilium
      repo: https://helm.cilium.io/
      version: 1.18.4
      namespace: kube-system
      atomic: true
  - name: all-ready
    needs: [cilium]
    wait:
      for: condition=Ready
      on: pods
      allNamespaces: true
```

```console
$ khook apply -f bootstrap.yaml
✓ cilium (helm)  21.457s
✓ all-ready (wait)  4.203s
```

- **No runtime dependencies.** Nothing to install on the runner besides khook.
- **DAG execution.** Steps declare `needs:` and run in parallel levels.
  Cycles fail validation before the cluster is touched.
- **Idempotent.** Helm release history decides install vs upgrade, applies
  converge existing objects, deletes treat "not found" as success. Safe to run
  on every `terraform apply`.
- **Explicit failures.** Per-step timeouts and retries, `onError: fail |
  continue`, a summary of what succeeded, failed, or was skipped, and separate
  exit codes for validation and execution errors.
- **Not a GitOps engine.** khook installs the GitOps controller and stops.

Step types: `helm`, `apply`, `delete`, `patch`, `wait`, `rollout`, `job`.
Specs take `${VAR}` substitution, [sprig](https://github.com/Masterminds/sprig)
pipelines, and `when:` ([CEL](https://cel.dev)) conditions. Field reference:
**[DSL spec](https://khook.io/dsl/)**.

## Quickstart

```bash
make build
k3d cluster create dev
bin/khook apply -f examples/simple.yaml \
  --set NAMESPACE_NAME_TO_CREATE=demo \
  --set NAMESPACE_NAME_FOR_INGRESS=ingress
```

A TTY gets live per-step status lines; CI gets plain logs and a summary table,
or JSON with `--output json`. Re-running converges without changes.

Walkthrough: **[Getting started](https://khook.io/docs/)**.

## Docs

- **[Getting started](https://khook.io/docs/)**: install, first spec, variables
- **[DSL specification](https://khook.io/dsl/)**: step types, variables, pipelines, conditions
- **[CLI reference](https://khook.io/cli/)**: commands, flags, variable precedence, exit codes
- **[vs Terraform](https://khook.io/vs-terraform/)**: why not the `kubernetes`/`helm` providers
- **[Examples](https://khook.io/examples/)**: [`real-case.yaml`](examples/real-case.yaml) (EKS bootstrap), [`localenv.yaml`](examples/localenv.yaml) (k3d/kind)

## Status

Pre-release. The v1 core is implemented and covered by unit and k3d
end-to-end tests: the seven step types, DAG engine, variables, resumable runs
(`state:`), teardown (`destroy`), and the CLI. Planned work, including a
Terraform/Lambda integration, is in the [roadmap](ROADMAP.md).

## Development

```bash
go test ./...   # unit tests (engine, spec, executors against fakes)
./tests/e2e.sh  # end-to-end against a throwaway k3d cluster
```

Repo conventions and reading order: [`AGENTS.md`](AGENTS.md).

## License

[MIT](LICENSE)
