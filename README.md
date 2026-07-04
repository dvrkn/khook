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
[vs Terraform](#isnt-this-just-terraforms-kuberneteshelm-providers) ·
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
apiVersion: khook.io/v1
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
✓ create-namespace (apply)  18ms
✓ ingress-nginx (helm)  21.457s
```

On a terminal each step is a live status line (pending → running →
ok/failed/skipped, with spinner and elapsed time). In CI you get plain logs
and a summary table instead, or `--output json` for machine-readable results.

Run it again — everything converges, nothing breaks. That's the point.

## Isn't this just Terraform's `kubernetes`/`helm` providers?

No — and it isn't trying to be. Terraform owns the layer below: VPCs, node
groups, IAM, the cluster itself. khook starts where that ends, and is built
to be *driven by* Terraform (run it on every `terraform apply`), not to
replace it. What khook replaces is the anti-pattern of pushing the
*bootstrap* through Terraform's in-cluster providers — which fights the tool
on day zero:

- **The chicken-and-egg.** Providers are configured at plan time, but the
  cluster's endpoint and credentials only exist after apply. HashiCorp's own
  docs warn against creating a cluster and its in-cluster resources in one
  apply. khook runs strictly after: a kubeconfig is an input, never a cycle
  in the graph.
- **Plans need a live API schema.** Ship a CRD chart and its custom resources
  in the same apply and the plan fails — the type doesn't exist yet. khook
  executes in DAG order against the real server: CRDs land, then everything
  that needs them.
- **No "wait" verb.** Bootstrap is mostly *waiting* — for a CNI to be ready,
  a webhook to answer, a rollout to finish. In Terraform that's `time_sleep`
  and `local-exec`; in khook, readiness gates (`wait:` on conditions or
  jsonpath, `rollout:`) are first-class steps.
- **State takes your objects hostage.** Every in-cluster object Terraform
  creates lives in its state forever: recreate the cluster and you're
  surgically pruning orphans; hand a release to ArgoCD and two owners fight
  over every field. khook keeps no object inventory — the cluster is the
  source of truth, idempotency comes from Helm release history and
  server-side apply, and handing off to GitOps is the design, not a turf war.

The flip side, honestly: when in-cluster objects must be wired into the cloud
graph — an IRSA role annotated onto a ServiceAccount, DNS records, a
stable, review-gated set of namespaces and quotas — Terraform's providers,
with plan/review and managed deletion, are the better tool. Keep those there.
khook is for the sequenced, wait-heavy, run-once-converge-always window
between "cluster exists" and "GitOps has the wheel."

*"But you'll have state too."* The planned run record
([roadmap](roadmap.md), phase 2) does store state in an in-cluster Secret —
same place as Terraform's `kubernetes` backend, same trick as Helm's release
records. The difference is what's in it: a **journal** (spec hash, per-step
outcome) so a failed bootstrap resumes at step 7 instead of redoing 12, and
unchanged steps skip. It is not an ownership ledger of your cluster's
objects. Delete it and nothing breaks — the next run just re-converges.

## How it works

A spec is a set of **steps**, each with exactly one action. Seven verbs cover
the bootstrap surface:

| Verb | What it does | Instead of |
|---|---|---|
| `helm:` | install-or-upgrade a chart (history decides) | `helm repo add` + `helm upgrade --install` |
| `apply:` | apply manifests — inline, file, URL, or kustomize — optionally waiting on them (`waitFor`) | `kubectl apply -f/-k` (`&& kubectl wait`) |
| `delete:` | remove resources by manifest or selector | `kubectl delete` |
| `patch:` | modify a resource in place (strategic/merge/json) | `kubectl patch` |
| `wait:` | block until a condition or jsonpath holds (or gone) | `kubectl wait` + `sleep`-and-pray |
| `rollout:` | restart / await workload rollouts | `kubectl rollout restart/status` |
| `job:` | run a container to completion in-cluster | one-off `kubectl run` / bash scripts |

`${VAR}` / `${VAR:-default}` substitution — with optional
[sprig](https://github.com/Masterminds/sprig) pipelines on values, helm-style
(`${APP | lower | trunc 63}`, hermetic function set) — and `when:` conditionals
([CEL](https://cel.dev) expressions over the variables, e.g.
`when: vars.get("ENABLE_ARGOCD", "false") == "true"`) keep one spec serving
many environments — values come from `--set`, `--var-file`, or `KHOOK_VAR_*`
environment variables; `KHOOK_SECRET_*` works the same but redacts the value
from all khook output. The full field reference lives in
[`docs/dsl.md`](docs/dsl.md); working specs in [`examples/`](examples/) —
[`real-case.yaml`](examples/real-case.yaml) is a production-shaped EKS
bootstrap (CNI swap, external-secrets, ArgoCD handoff), and
[`localenv.yaml`](examples/localenv.yaml) is a k3d/kind local environment
(optional Cilium, Argo CD at `argocd.localhost`, kube-prometheus-stack with
Grafana at `grafana.localhost`).

## CLI

| Command | What it does |
|---|---|
| `khook apply -f spec.yaml` | execute the spec against the cluster |
| `khook plan -f spec.yaml` | show what apply would do — install vs upgrade vs skip, checked against the cluster (`--diff` for rendered object diffs via server-side dry-run, `--offline` for the DAG-only plan) |
| `khook validate -f spec.yaml` | parse + validate (exit 2 on problems) |
| `khook schema` | print the spec's JSON Schema (committed at [`schema/v1/khook.json`](schema/v1/khook.json) — point `# yaml-language-server: $schema=...` at it for editor validation and autocomplete) |
| `khook version` | print version info |

Full reference — flags, variable precedence, exit codes, execution
semantics: [`docs/cli.md`](docs/cli.md).

## Status

The v1 core is implemented and tested (unit + k3d end-to-end): the five step
types, DAG engine, variables, and the CLI above. Pre-release — no published
binaries yet; OCI charts, resumable runs, and a Terraform/Lambda
integration are on the [roadmap](roadmap.md).

## Development

```bash
go test ./...    # unit tests (engine, spec, executors against fakes)
./hack/e2e.sh    # end-to-end against a throwaway k3d cluster
```

Contributions welcome — read [`AGENTS.md`](AGENTS.md) for repo conventions
and the reading order.
