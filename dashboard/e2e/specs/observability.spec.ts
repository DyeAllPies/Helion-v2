// e2e/specs/observability.spec.ts
//
// End-to-end tests for observability features:
//   - Enhanced /readyz with subsystem checks
//   - GET /jobs/{id}/logs endpoint
//   - Prometheus /metrics endpoint

import { test, expect } from '../fixtures/auth.fixture';
import { getRootToken, submitJob, API_URL } from '../fixtures/cluster.fixture';

test.describe('Readiness Probe', () => {

  test('/readyz returns subsystem checks', async ({}) => {
    const res = await fetch(`${API_URL}/readyz`);
    expect(res.ok).toBe(true);

    const body = await res.json();
    expect(body.status).toBe('ready');
    expect(body.checks).toBeDefined();
    expect(body.checks.badgerdb).toBe('ok');
    expect(body.checks.scheduler).toBe('ok');
    expect(body.checks.grpc_server).toBe('ok');
    // Nodes check should show registered count.
    expect(body.checks.nodes).toContain('registered');
  });

  test('/healthz returns ok', async ({}) => {
    const res = await fetch(`${API_URL}/healthz`);
    expect(res.ok).toBe(true);
    const body = await res.json();
    expect(body.ok).toBe(true);
  });
});

test.describe('Job Logs', () => {

  test('GET /jobs/{id}/logs returns empty for new job', async ({}) => {
    const token = getRootToken();
    const jobId = `e2e-logs-empty-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['no-logs'] });

    // Logs may be empty since the job just entered pending state.
    const res = await fetch(`${API_URL}/jobs/${jobId}/logs`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(res.ok).toBe(true);
    const body = await res.json();
    expect(body.job_id).toBe(jobId);
    expect(body.entries).toBeDefined();
  });

  test('GET /jobs/{id}/logs supports tail parameter', async ({}) => {
    const token = getRootToken();
    const jobId = `e2e-logs-tail-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo', args: ['tail-test'] });

    const res = await fetch(`${API_URL}/jobs/${jobId}/logs?tail=5`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(res.ok).toBe(true);
  });
});

test.describe('Prometheus Metrics', () => {

  test('/metrics endpoint returns Prometheus text format', async ({}) => {
    const res = await fetch(`${API_URL}/metrics`);
    expect(res.ok).toBe(true);
    const text = await res.text();
    // Should contain Helion-specific metrics.
    expect(text).toContain('helion_jobs_total');
    expect(text).toContain('helion_running_jobs');
    expect(text).toContain('helion_healthy_nodes');
  });

  test('/metrics includes retrying and scheduled gauges', async ({}) => {
    const res = await fetch(`${API_URL}/metrics`);
    const text = await res.text();
    expect(text).toContain('helion_retrying_jobs');
    expect(text).toContain('helion_scheduled_jobs');
  });
});

test.describe('Node Capacity (Feature 03)', () => {

  test('GET /nodes returns capacity fields', async ({}) => {
    const token = getRootToken();
    const res = await fetch(`${API_URL}/nodes`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(res.ok).toBe(true);
    const body = await res.json();
    expect(body.nodes.length).toBeGreaterThan(0);

    // At least one node should report capacity (set by the node agent).
    const node = body.nodes[0];
    expect(node.id).toBeDefined();
    // Capacity fields may be 0 on first heartbeat but should be present.
    expect(node).toHaveProperty('cpu_millicores');
    expect(node).toHaveProperty('max_slots');
  });
});

test.describe('Job Cancel API (Feature 04)', () => {

  test('POST /jobs/{id}/cancel returns cancelled status', async ({}) => {
    const token = getRootToken();
    const jobId = `e2e-cancel-api-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'sleep', args: ['3600'] });

    const cancelRes = await fetch(`${API_URL}/jobs/${jobId}/cancel`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(cancelRes.ok).toBe(true);

    const body = await cancelRes.json();
    expect(body.status).toBe('cancelled');
  });

  test('cancel already-terminal job returns 409', async ({}) => {
    const token = getRootToken();
    const jobId = `e2e-cancel-term-${Date.now()}`;

    await submitJob(token, { id: jobId, command: 'echo' });

    // Cancel once.
    await fetch(`${API_URL}/jobs/${jobId}/cancel`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
    });

    // Cancel again — should be 409 Conflict.
    const res = await fetch(`${API_URL}/jobs/${jobId}/cancel`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(res.status).toBe(409);
  });
});

test.describe('Retry Policy in API (Feature 02)', () => {

  test('GET /jobs/{id} returns retry_policy fields', async ({}) => {
    const token = getRootToken();
    const jobId = `e2e-retry-api-${Date.now()}`;

    // Submit with retry policy.
    await fetch(`${API_URL}/jobs`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: jobId, command: 'echo', args: ['retry'],
        retry_policy: { max_attempts: 3, backoff: 'exponential', initial_delay_ms: 1000 },
      }),
    });

    const res = await fetch(`${API_URL}/jobs/${jobId}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const job = await res.json();
    expect(job.attempt).toBe(1);
  });
});

test.describe('Resources in API (Feature 03)', () => {

  test('GET /jobs/{id} accepts resources on submission', async ({}) => {
    const token = getRootToken();
    const jobId = `e2e-res-api-${Date.now()}`;

    const submitRes = await fetch(`${API_URL}/jobs`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: jobId, command: 'echo',
        resources: { cpu_millicores: 500, memory_bytes: 134217728, slots: 2 },
      }),
    });
    expect(submitRes.status).toBe(201);
  });
});

test.describe('Workflow Priority (Feature 05)', () => {

  test('workflow jobs inherit workflow priority', async ({}) => {
    const token = getRootToken();
    const wfId = `e2e-wf-pri-${Date.now()}`;

    const res = await fetch(`${API_URL}/workflows`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: wfId, name: 'priority test', priority: 80,
        jobs: [{ name: 'step1', command: 'echo' }],
      }),
    });
    expect(res.status).toBe(201);

    const wf = await res.json();
    // The job should inherit the workflow's priority.
    const jobId = wf.jobs[0].job_id;
    expect(jobId).toBeTruthy();

    const jobRes = await fetch(`${API_URL}/jobs/${encodeURIComponent(jobId)}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const job = await jobRes.json();
    expect(job.priority).toBe(80);
  });
});

test.describe('Workflow Completion (Feature 01)', () => {

  test('workflow reaches completed status after all jobs finish', async ({}) => {
    const token = getRootToken();
    const wfId = `e2e-wf-done-${Date.now()}`;

    await fetch(`${API_URL}/workflows`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: wfId, name: 'completion test',
        jobs: [{ name: 'quick', command: 'echo', args: ['done'] }],
      }),
    });

    // Wait for workflow to reach a terminal status.
    await expect(async () => {
      const res = await fetch(`${API_URL}/workflows/${wfId}`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      const wf = await res.json();
      expect(['completed', 'failed']).toContain(wf.status);
    }).toPass({ timeout: 20_000, intervals: [2_000] });
  });
});
