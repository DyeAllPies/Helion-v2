# Deferred: Hardware attestation of node labels

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 10 — minimal ML pipeline](../10-minimal-ml-pipeline.md)

## Context

The ML pipeline slice in [10-minimal-ml-pipeline.md](../10-minimal-ml-pipeline.md) lets nodes self-report labels (`gpu=a100`, `cuda=12.4`, `zone=us-east`) that the scheduler uses for `node_selector` matching. The trust boundary today is mTLS + ML-DSA node certificates: "this is node X that we issued a cert for." It does **not** cover "this node actually owns the hardware it claims." A compromised node under the existing cert can register with `gpu=a100` on a CPU-only host, win GPU-targeted jobs, and either run them incorrectly or exfiltrate the artifacts they stage.

Proper mitigation needs hardware attestation — TPM quotes, Intel SGX / TDX, AMD SEV-SNP, or confidential-VM attestation anchored in the cloud provider's root of trust. The coordinator would verify an attestation quote at Register time and bind the registered labels to the measured hardware.

## Why deferred

All four attestation paths add heavy dependencies and meaningful per-deployment setup. None of them is universally available — bare-metal clusters, mixed clouds, and ARM dev boxes don't all have the same attestation surface. Shipping minimal ML support should not block on choosing one. The operator mitigation in the interim is to set labels via deployment env (`HELION_LABEL_*`) from a trusted control plane (k8s Deployment, Nomad job spec, systemd unit), treating the node-agent's `nvidia-smi` auto-probe as best-effort metadata for friendly clusters only.

## Revisit trigger

Revisit when a target deployment standardises on a single attestation surface (TPM / SGX / TDX / SEV-SNP / confidential VM) that Helion can require.
