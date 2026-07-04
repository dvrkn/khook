<div align="center">

# ⚓ khook

**cloud-init for Kubernetes.**

One static binary that takes a freshly created cluster from
*"API server answers"* to *"workloads can be deployed."*

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](go.mod)
[![Kubernetes SDK](https://img.shields.io/badge/client--go-v0.36-326CE5?logo=kubernetes&logoColor=white)](go.mod)
[![Helm SDK](https://img.shields.io/badge/helm-v4-0F1689?logo=helm&logoColor=white)](go.mod)
[![Status](https://img.shields.io/badge/status-pre--release-orange)](roadmap.md)

[The pitch](#the-pitch) · [Quickstart](#try-it-in-60-seconds) ·
[How it works](#how-it-works) · [CLI](#cli) · [Docs](docs/dsl.md)

</div>

---

## The pitch

Terraform (or eksctl, or CAPI) hands you a cluster. ArgoCD takes over once
it's installed. In between lives everybody's least favorite artifact: the
bootstrap script — a few hundred lines of `kubectl apply`, `helm upgrade
--install`, `sleep 30`, and retry loops, duct-taped into a `null_resource`
and feared by everyone on call.

khook replaces that gap with a declarative spec:

```yaml
apiVersion: khook.dvrkn.com/v1
kind: Khook
metadata:
  name: bootstrap
defaults:
  timeout: 5m
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
```

- **One binary, zero dependencies.** The Kubernetes and Helm SDKs are
  embedded. khook never shells out — nothing to install on the runner but
  khook itself.
- **A DAG, not a script.** Steps declare `needs:`; khook topologically sorts
  them and runs each level in parallel. Cycles are caught before anything
  touches the cluster.
- **Idempotent by design.** Re-running a spec is always safe: Helm release
  history decides install-vs-upgrade, applies converge existing resources,
  deletes treat "already gone" as success. Run it on every `terraform apply`.
- **Fails loud, precisely.** Per-step timeouts and retries, `onError:
  fail | continue`, a summary table naming exactly what succeeded, what
  failed, and what was skipped because of it — with distinct exit codes for
  validation vs execution failures.
- **Bootstrap, then hand off.** khook installs your CNI, secrets tooling, and
  GitOps controller — then gets out of the way. It is deliberately *not* a
  GitOps engine.

## Try it in 60 seconds

```bash
go build -o bin/khook ./cmd/khook
k3d cluster create dev
bin/khook apply -f examples/simple.yaml \
  --set NAMESPACE_NAME_TO_CREATE=demo \
  --set NAMESPACE_NAME_FOR_INGRESS=ingress
```

```text
STEP              TYPE   STATUS  ATTEMPTS  DURATION  DETAIL
create-namespace  apply  ok      1         18ms
ingress-nginx     helm   ok      1         21.457s
```

Run it again — everything converges, nothing breaks. That's the point.

## How it works

A spec is a set of **steps**, each with exactly one action. Five verbs cover
the bootstrap surface:

| Verb | What it does | Instead of |
|---|---|---|
| `helm:` | install-or-upgrade a chart (history decides) | `helm repo add` + `helm upgrade --install` |
| `apply:` | apply manifests — inline, file, or URL | `kubectl apply -f` |
| `delete:` | remove resources by manifest or selector | `kubectl delete` |
| `wait:` | block until a condition holds (or gone) | `kubectl wait` + `sleep`-and-pray |
| `rollout:` | restart / await workload rollouts | `kubectl rollout restart/status` |

`${VAR}` / `${VAR:-default}` substitution keeps one spec serving many
environments — values come from `--set`, `--var-file`, or `KHOOK_VAR_*`
environment variables. The full field reference lives in
[`docs/dsl.md`](docs/dsl.md); working specs in [`examples/`](examples/) —
[`real-case.yaml`](examples/real-case.yaml) is a production-shaped EKS
bootstrap (CNI swap, external-secrets, ArgoCD handoff).

## CLI

| Command | What it does |
|---|---|
| `khook apply -f spec.yaml` | execute the spec against the cluster |
| `khook plan -f spec.yaml` | print the DAG execution plan, no cluster access |
| `khook validate -f spec.yaml` | parse + validate (exit 2 on problems) |
| `khook schema` | print the spec's JSON Schema |
| `khook version` | print version info |

Full reference — flags, variable precedence, exit codes, execution
semantics: [`docs/cli.md`](docs/cli.md).

## Status

The v1 core is implemented and tested (unit + k3d end-to-end): the five step
types, DAG engine, variables, and the CLI above. Pre-release — no published
binaries yet; conditionals (`when:`), OCI charts, resumable runs, and a
Terraform/Lambda integration are on the [roadmap](roadmap.md).

## Development

```bash
go test ./...    # unit tests (engine, spec, executors against fakes)
./hack/e2e.sh    # end-to-end against a throwaway k3d cluster
```

Contributions welcome — read [`AGENTS.md`](AGENTS.md) for repo conventions
and the reading order.
