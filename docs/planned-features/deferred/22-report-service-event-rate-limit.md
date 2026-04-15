# Deferred: Rate-limit ReportServiceEvent on the coordinator

**Priority:** P3
**Status:** Deferred
**Originating feature:** [feature 17 — ML inference jobs](../17-ml-inference-jobs.md)
**Audit reference:** [2026-04-14-02](../../audits/2026-04-14-02.md) finding L1

## Context

The standard node-RPC rate limiter (`internal/ratelimit`) caps `Register` and `ReportResult` submissions per node so a compromised or misbehaving node can't DoS the coordinator. `ReportServiceEvent` is a new RPC introduced in feature 17 and is **not** on that limiter. A misbehaving or malicious node could flap a service's readiness state and flood the coordinator's audit log and Prometheus metrics.

## Why deferred

The emission path is already edge-triggered: the prober in `internal/nodeserver/service_prober.go` only emits when `ready` changes value. A node would have to actively alternate transitions to drive event volume, which is harder than just spamming the limiter on a broken one. In practice:

- A legitimate flapping service generates O(flaps) events per minute — maybe a handful in the worst observed case.
- A compromised node trying to flood the coordinator would be caught by the existing `LogSecurityViolation` path anyway (via the cross-node-poison check in `ReportServiceEvent`).
- Adding a limiter costs an audit-policy decision (what's the right threshold? per-node or per-job? what's the response — drop, reject, or trip revocation?) that we don't have answers to yet.

The lowest-risk path is to ship feature 17 without the limiter, watch production audit-event rates for a release, and add a limiter only if actual traffic shows the gap matters.

## Revisit trigger

- A production deployment reports service.* events dominating the audit log volume.
- Or: a synthetic load test shows a single malicious node can make the audit store grow faster than `Logger`'s BadgerDB compaction keeps up.
- Or: the existing node-RPC limiter grows a per-RPC-type matrix (it's currently one bucket per node across all RPCs), at which point adding the service-event slot is one line.
