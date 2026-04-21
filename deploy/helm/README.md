# Helion Helm chart — design scaffolding, not a deployment artefact

This chart describes what a Kubernetes deployment of Helion **would**
look like:

- `coordinator-deployment.yaml` — single-replica `Deployment`
- `node-daemonset.yaml` — one `DaemonSet` pod per cluster node
- `coordinator-service.yaml` / `coordinator-ingress.yaml` — REST
  entry point
- `configmap.yaml` — coordinator configuration
- `coordinator-pvc.yaml` — persistence volume for BadgerDB state
- `coordinator-networkpolicy.yaml` — egress/ingress pinning
- `coordinator-serviceaccount.yaml` / `node-serviceaccount.yaml` —
  workload identity
- `values-eks.yaml` / `values-gke.yaml` / `values-do.yaml` —
  per-cloud parameter overlays

## Status

**The chart has never been installed against a real Kubernetes
cluster.** Helion runs under Docker Compose locally and under
GitHub Actions CI; no deployment pipeline exists, and no CI job
exercises `helm install` or `kubectl apply` against these
manifests. The manifests are kept as a reference for what
production would look like, not as a supported install path.

Treat them as:

- A worked example of the `Deployment` + `DaemonSet` split the
  architecture doc describes.
- A target shape for a future project that wants to pick up
  Helion and actually deploy it.
- Lint-friendly YAML (`helm lint deploy/helm/` runs clean), so
  the scaffold stays internally consistent even without a real
  cluster to install it on.

If you are the future reader taking Helion to production: expect
to exercise every manifest under a real cluster, reconcile it
against today's Kubernetes APIs, and wire a CI job that runs
`helm lint` + a dry-run install on every change before trusting
any of these to drive real infrastructure.

## Would-look-like commands (not part of any validated path)

```bash
helm install helion ./deploy/helm
helm install helion ./deploy/helm -f deploy/helm/values-eks.yaml
helm install helion ./deploy/helm -f deploy/helm/values-gke.yaml
helm install helion ./deploy/helm -f deploy/helm/values-do.yaml
```

See the project [`docs/architecture/`](../../docs/architecture/)
for the architectural shape the chart is modelling.
