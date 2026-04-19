// src/app/features/submit/ml-templates.spec.ts

import { IRIS_TEMPLATE, MNIST_TEMPLATE, TEMPLATE_CARDS } from './ml-templates';
import { validateWorkflowShape } from './workflow-shape-validator';

describe('ML workflow templates', () => {

  it('exposes iris, mnist, custom cards in that order', () => {
    expect(TEMPLATE_CARDS.map(c => c.key)).toEqual(['iris', 'mnist', 'custom']);
  });

  it('iris and mnist cards carry a template; custom does not', () => {
    const cards = [...TEMPLATE_CARDS];
    expect(cards.find(c => c.key === 'iris')?.template).toBeDefined();
    expect(cards.find(c => c.key === 'mnist')?.template).toBeDefined();
    expect(cards.find(c => c.key === 'custom')?.template).toBeUndefined();
  });

  // ── Iris ───────────────────────────────────────────────────────────

  it('iris template passes the client-side shape validator', () => {
    const { errors } = validateWorkflowShape(IRIS_TEMPLATE);
    expect(errors).toEqual([]);
  });

  it('iris has the 4-job ingest → preprocess → train → register DAG', () => {
    expect(IRIS_TEMPLATE.jobs.map(j => j.name))
      .toEqual(['ingest', 'preprocess', 'train', 'register']);
    // Every job except ingest has an edge; the chain is linear.
    expect(IRIS_TEMPLATE.jobs[1].depends_on).toEqual(['ingest']);
    expect(IRIS_TEMPLATE.jobs[2].depends_on).toEqual(['preprocess']);
    expect(IRIS_TEMPLATE.jobs[3].depends_on).toEqual(['train']);
  });

  // ── MNIST ──────────────────────────────────────────────────────────

  it('mnist template passes the client-side shape validator', () => {
    const { errors } = validateWorkflowShape(MNIST_TEMPLATE);
    expect(errors).toEqual([]);
  });

  // Feature 21 load-bearing regression guard. Flipping train back
  // to runtime=go silently disables the heterogeneous-scheduling
  // demo; fail loudly if the template drifts.
  it('mnist train step pins to the Rust-runtime node (feature 21)', () => {
    const train = MNIST_TEMPLATE.jobs.find(j => j.name === 'train');
    expect(train).toBeDefined();
    expect(train!.node_selector).toEqual({ runtime: 'rust' });
  });

  it('mnist non-train steps pin to the Go-runtime nodes', () => {
    for (const name of ['ingest', 'preprocess', 'register']) {
      const job = MNIST_TEMPLATE.jobs.find(j => j.name === name);
      expect(job?.node_selector).toEqual({ runtime: 'go' });
    }
  });

  it('every mnist job carries PATH in env (Rust runtime env_clears)', () => {
    // Regression guard for the bug we hit shipping feature 21:
    // the Rust runtime env_clear()s before spawn, so PATH has to
    // be set explicitly or `python` can't resolve.
    for (const job of MNIST_TEMPLATE.jobs) {
      expect(job.env?.['PATH']).toContain('/usr/local/bin');
    }
  });
});
