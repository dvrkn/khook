# khook

**cloud-init for Kubernetes.** One static binary that takes a freshly created
cluster from "API server answers" to "workloads can be deployed" — CNI,
secrets management, GitOps controller — driven by a declarative YAML spec.
No `kubectl`, no `helm`, no shell scripts: the Kubernetes and Helm SDKs are
embedded, and khook never shells out.

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

Steps form a DAG via `needs:` and run in parallel levels. Five step types
cover the bootstrap surface: `helm:` (install-or-upgrade decided by release
history), `apply:`, `delete:`, `wait:`, `rollout:`. Running the same spec
twice is safe by design. See [`docs/dsl.md`](docs/dsl.md) for the full field
reference and [`examples/`](examples/) for working specs.

## Try it in 60 seconds

```bash
go build -o bin/khook ./cmd/khook
k3d cluster create dev
bin/khook apply -f examples/simple.yaml \
  --set NAMESPACE_NAME_TO_CREATE=demo \
  --set NAMESPACE_NAME_FOR_INGRESS=ingress
```

## CLI

| Command | What it does |
|---|---|
| `khook apply -f spec.yaml` | execute the spec against the cluster |
| `khook plan -f spec.yaml` | print the DAG execution plan, no cluster access |
| `khook validate -f spec.yaml` | parse + validate (exit 2 on problems) |
| `khook schema` | print the spec's JSON Schema |
| `khook version` | print version info |

Variables: `${NAME}` / `${NAME:-default}` in the spec, supplied via `--set
NAME=value`, `--var-file vars.yaml`, or environment variables prefixed
`KHOOK_VAR_` (precedence in that order). Cluster access uses standard
kubeconfig rules (`--kubeconfig`, `--context` to override). Full reference:
[`docs/cli.md`](docs/cli.md).

## Development

```bash
go test ./...    # unit tests (engine, spec, executors against fakes)
./hack/e2e.sh    # end-to-end against a throwaway k3d cluster
```

The roadmap lives in [`roadmap.md`](roadmap.md).
