---
title: khook vs Terraform
layout: docs
permalink: /vs-terraform/
description: Why bootstrap doesn't belong in Terraform's kubernetes/helm providers — and where it still does.
---

# Isn't this just Terraform's `kubernetes`/`helm` providers?

No — and it isn't trying to be. Terraform owns the layer below: VPCs, node
groups, IAM, the cluster itself. khook starts where that ends, and is built
to be *driven by* Terraform (run it on every `terraform apply`), not to
replace it. What khook replaces is the anti-pattern of pushing the
*bootstrap* through Terraform's in-cluster providers — which fights the tool
on day zero.

## Where the providers fight you

- **The chicken-and-egg.** Providers are configured at plan time, but the
  cluster's endpoint and credentials only exist after apply. HashiCorp's own
  docs warn against creating a cluster and its in-cluster resources in one
  apply. khook runs strictly after: a kubeconfig is an input, never a cycle
  in the graph.
- **Plans need a live API schema.** Ship a chart that installs custom
  resource definitions (CRDs) together with the custom resources that use
  them in the same apply and the plan fails — the type doesn't exist yet.
  khook executes in DAG order against the real server: CRDs land, then
  everything that needs them.
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

## Where Terraform is still the right tool

The flip side, honestly: when in-cluster objects must be wired into the cloud
graph — an IRSA role annotated onto a ServiceAccount, DNS records, a
stable, review-gated set of namespaces and quotas — Terraform's providers,
with plan/review and managed deletion, are the better tool. Keep those there.
khook is for the sequenced, wait-heavy, run-once-converge-always window
between "cluster exists" and "GitOps has the wheel."

## "But you'll have state too"

khook's run record does store state in an in-cluster Secret — same place as Terraform's
`kubernetes` backend, same trick as Helm's release records. The difference is
what's in it: a **journal** (per-step input hash and outcome) so a failed
bootstrap resumes at step 7 instead of redoing 12, and an edited spec
re-runs only the steps whose inputs changed.
It is not an ownership ledger of your cluster's objects. Delete it and
nothing breaks — the next run just re-converges.

## The division of labor

| Layer | Owner |
|---|---|
| VPC, node groups, IAM, the cluster itself | Terraform / eksctl / CAPI |
| Day-zero bootstrap: CNI, CRDs, secrets tooling, GitOps controller, readiness gates | **khook** |
| In-cluster objects wired into the cloud graph (IRSA, DNS, review-gated quotas) | Terraform providers |
| Long-term reconciliation, drift correction, app delivery | ArgoCD / Flux |
