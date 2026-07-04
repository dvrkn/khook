<div align="center">

# ⚓ khook

**Declarative bootstrap for Kubernetes.**

One static binary that takes a freshly created cluster from
*"API server answers"* to *"workloads can be deployed"* — a declarative,
idempotent DAG of `helm`, `apply`, `wait`, and friends. No kubectl, no helm
binary, no bash.

[![CI](https://github.com/dvrkn/khook/actions/workflows/ci.yml/badge.svg)](https://github.com/dvrkn/khook/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Kubernetes SDK](https://img.shields.io/badge/client--go-v0.36-326CE5?logo=kubernetes&logoColor=white)](go.mod)
[![Helm SDK](https://img.shields.io/badge/helm-v4-0F1689?logo=helm&logoColor=white)](go.mod)
[![Status](https://img.shields.io/badge/status-pre--release-orange)](ROADMAP.md)

**[Docs](https://khook.io/) · [Quickstart](https://khook.io/docs/) · [DSL](https://khook.io/dsl/) · [CLI](https://khook.io/cli/) · [vs Terraform](https://khook.io/vs-terraform/)**

</div>

---

## The gap khook fills

Terraform (or eksctl, or CAPI) hands you a cluster. ArgoCD takes over once it's
installed. In between lives everybody's least favorite artifact: the bootstrap
script — a few hundred lines of `kubectl apply`, `helm upgrade --install`,
`sleep 30`, and retry loops, duct-taped into a `null_resource` and feared by
everyone on call.

khook replaces that gap with a declarative spec:

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

- **One binary, zero dependencies.** The Kubernetes and Helm SDKs are embedded;
  khook never shells out. Nothing to install on the runner but khook itself.
- **A DAG, not a script.** Steps declare `needs:`; khook topologically sorts
  them and runs each level in parallel. Cycles are caught before anything
  touches the cluster.
- **Idempotent by design.** Re-running a spec is always safe — Helm release
  history decides install-vs-upgrade, applies converge existing resources,
  deletes treat "already gone" as success. Run it on every `terraform apply`.
- **Fails loud, precisely.** Per-step timeouts and retries, `onError: fail |
  continue`, and a summary table naming exactly what succeeded, failed, or was
  skipped — with distinct exit codes for validation vs execution failures.
- **Bootstrap, then hand off.** khook installs your CNI, secrets tooling, and
  GitOps controller — then gets out of the way. It is deliberately *not* a
  GitOps engine.

Seven verbs cover the bootstrap surface — `helm`, `apply`, `delete`, `patch`,
`wait`, `rollout`, `job` — with `${VAR}` substitution, [sprig](https://github.com/Masterminds/sprig)
pipelines, and `when:` ([CEL](https://cel.dev)) conditionals so one spec serves
many environments. Full field reference: **[the DSL spec](https://khook.io/dsl/)**.

## Quickstart

```bash
make build
k3d cluster create dev
bin/khook apply -f examples/simple.yaml \
  --set NAMESPACE_NAME_TO_CREATE=demo \
  --set NAMESPACE_NAME_FOR_INGRESS=ingress
```

On a terminal each step is a live status line; in CI you get plain logs and a
summary table, or `--output json`. Run it again — everything converges, nothing
breaks. That's the point.

Full walkthrough in **[Getting started](https://khook.io/docs/)**.

## Docs

- **[Getting started](https://khook.io/docs/)** — install, first spec, variables
- **[DSL specification](https://khook.io/dsl/)** — the seven step types, variables, pipelines, conditionals
- **[CLI reference](https://khook.io/cli/)** — commands, flags, variable precedence, exit codes, semantics
- **[vs Terraform](https://khook.io/vs-terraform/)** — why not the `kubernetes`/`helm` providers
- **[Examples](https://khook.io/examples/)** — [`real-case.yaml`](examples/real-case.yaml) (production-shaped EKS bootstrap), [`localenv.yaml`](examples/localenv.yaml) (k3d/kind)

## Status

The v1 core is implemented and tested (unit + k3d end-to-end): the seven step
types, DAG engine, variables, resumable runs (`state:`), teardown (`destroy`),
and the CLI. Pre-release — a Terraform/Lambda integration is on the
[roadmap](ROADMAP.md).

## Development

```bash
go test ./...   # unit tests (engine, spec, executors against fakes)
./tests/e2e.sh  # end-to-end against a throwaway k3d cluster
```

Contributions welcome — read [`AGENTS.md`](AGENTS.md) for repo conventions and
the reading order.

## License

[MIT](LICENSE)
