---
title: khook vs Terraform
layout: docs
permalink: /vs-terraform/
description: Why bootstrap doesn't belong in Terraform's kubernetes/helm providers, and where it still does.
---

# Isn't this just Terraform's `kubernetes`/`helm` providers?

No. Terraform owns the layer below: VPCs, node groups, IAM, the cluster.
khook starts after that and is meant to be run by Terraform (for example on
every `terraform apply`), not to replace it. What it replaces is running the
bootstrap itself through Terraform's in-cluster providers.

## Where the providers fall short

- **Provider configuration.** Providers are configured at plan time, but the
  cluster endpoint and credentials exist only after apply. HashiCorp's docs
  advise against creating a cluster and its in-cluster resources in one
  apply. khook runs strictly afterwards; the kubeconfig is an input.
- **Plans need the API schema.** A plan that includes CRDs and custom
  resources using them fails, because the types do not exist yet. khook
  executes in DAG order against the live server: CRDs first, then the
  resources that need them.
- **No wait primitive.** Bootstrap is mostly waiting: for the CNI, a webhook,
  a rollout. Terraform uses `time_sleep` and `local-exec`; khook has `wait:`
  (conditions, jsonpath) and `rollout:` steps.
- **State owns the objects.** Every in-cluster object Terraform creates stays
  in its state. Recreate the cluster and the state has orphans; hand a release
  to Argo CD and two tools manage the same fields. khook keeps no object
  inventory: the cluster is the source of truth, idempotency comes from Helm
  release history and server-side apply, and handoff to GitOps is expected.

## Where Terraform fits better

When in-cluster objects are wired into cloud resources (an IRSA role
annotated onto a ServiceAccount, DNS records, a reviewed set of namespaces
and quotas), Terraform's providers are the better tool: plan review and
managed deletion matter there. khook covers the sequenced, wait-heavy window
between "cluster exists" and "GitOps controller is running."

## khook's state

The optional run record ([`state:`](dsl.md#state--the-run-state-record)) is
an in-cluster Secret, like Terraform's `kubernetes` backend or Helm's release
records. It holds a **journal** (per-step input hash and outcome), so a failed
run resumes where it stopped and an edited spec re-runs only the changed
steps. It is not an inventory of cluster objects: delete it and the next run
re-converges everything.

## Division of labor

| Layer | Owner |
|---|---|
| VPC, node groups, IAM, the cluster | Terraform / eksctl / CAPI |
| Day-zero bootstrap: CNI, CRDs, secrets tooling, GitOps controller, readiness gates | **khook** |
| In-cluster objects tied to cloud resources (IRSA, DNS, reviewed quotas) | Terraform providers |
| Ongoing reconciliation, drift correction, app delivery | Argo CD / Flux |
