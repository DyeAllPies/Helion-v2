// src/app/features/submit/workflow-shape-validator.spec.ts

import { validateWorkflowShape } from './workflow-shape-validator';

describe('validateWorkflowShape', () => {

  function wf(over: Record<string, unknown> = {}): unknown {
    return {
      id:   'wf-1',
      name: 'test',
      jobs: [{ name: 'a', command: 'echo' }],
      ...over,
    };
  }

  // ── Happy path ──────────────────────────────────────────────────

  it('accepts a minimal valid workflow', () => {
    const { errors } = validateWorkflowShape(wf());
    expect(errors).toEqual([]);
  });

  it('accepts a multi-job workflow with depends_on edges', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [
        { name: 'a', command: 'echo' },
        { name: 'b', command: 'echo', depends_on: ['a'] },
        { name: 'c', command: 'echo', depends_on: ['a', 'b'] },
      ],
    }));
    expect(errors).toEqual([]);
  });

  // ── Structural ──────────────────────────────────────────────────

  it('rejects a non-object root', () => {
    const { errors } = validateWorkflowShape('just a string');
    expect(errors.some(e => e.includes('must be a JSON object'))).toBeTrue();
  });

  it('rejects a null root', () => {
    const { errors } = validateWorkflowShape(null);
    expect(errors.some(e => e.includes('must be a JSON object'))).toBeTrue();
  });

  // ── Required fields ──────────────────────────────────────────────

  it('requires id', () => {
    const { errors } = validateWorkflowShape(wf({ id: undefined }));
    expect(errors.some(e => e.includes('id is required'))).toBeTrue();
  });

  it('rejects empty id', () => {
    const { errors } = validateWorkflowShape(wf({ id: '' }));
    expect(errors.some(e => e.includes('id must be non-empty'))).toBeTrue();
  });

  it('requires name', () => {
    const { errors } = validateWorkflowShape(wf({ name: undefined }));
    expect(errors.some(e => e.includes('name is required'))).toBeTrue();
  });

  it('requires jobs (array)', () => {
    const { errors } = validateWorkflowShape(wf({ jobs: 'not an array' }));
    expect(errors.some(e => e.includes('jobs is required'))).toBeTrue();
  });

  it('rejects empty jobs array', () => {
    const { errors } = validateWorkflowShape(wf({ jobs: [] }));
    expect(errors.some(e => e.includes('at least one job'))).toBeTrue();
  });

  // ── Per-job shape ────────────────────────────────────────────────

  it('requires each job to have a name', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [{ command: 'echo' }],
    }));
    expect(errors.some(e => e.includes('jobs[0]: name is required'))).toBeTrue();
  });

  it('rejects duplicate job names', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [
        { name: 'a', command: 'echo' },
        { name: 'a', command: 'echo' },
      ],
    }));
    expect(errors.some(e => e.includes('duplicate name'))).toBeTrue();
  });

  it('rejects depends_on pointing at a missing job', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [
        { name: 'a', command: 'echo', depends_on: ['nope'] },
      ],
    }));
    expect(errors.some(e => e.includes('unknown job "nope"'))).toBeTrue();
  });

  it('flags timeout_seconds > 3600', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [{ name: 'a', command: 'echo', timeout_seconds: 9999 }],
    }));
    expect(errors.some(e => e.includes('timeout_seconds must be a number'))).toBeTrue();
  });

  it('flags negative timeout_seconds', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [{ name: 'a', command: 'echo', timeout_seconds: -1 }],
    }));
    expect(errors.some(e => e.includes('timeout_seconds'))).toBeTrue();
  });

  // ── Env denylist ─────────────────────────────────────────────────
  // Mirrors the Job form + eventual feature-25 server rules.

  it('rejects LD_PRELOAD in a job env', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [{ name: 'a', command: 'echo', env: { LD_PRELOAD: '/tmp/evil.so' } }],
    }));
    expect(errors.some(e => e.includes('LD_PRELOAD'))).toBeTrue();
    expect(errors.some(e => e.includes('dynamic-loader'))).toBeTrue();
  });

  it('rejects DYLD_INSERT_LIBRARIES prefix', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [{ name: 'a', command: 'echo', env: { DYLD_INSERT_LIBRARIES: '/x' } }],
    }));
    expect(errors.some(e => e.includes('dynamic-loader'))).toBeTrue();
  });

  it('rejects GCONV_PATH exact match', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [{ name: 'a', command: 'echo', env: { GCONV_PATH: '/x' } }],
    }));
    expect(errors.some(e => e.includes('module-loading'))).toBeTrue();
  });

  it('accepts a typical ML workflow env', () => {
    const { errors } = validateWorkflowShape(wf({
      jobs: [{
        name: 'a', command: 'python',
        env: {
          PYTHONPATH:     '/app/ml-mnist',
          HELION_TOKEN:   'tok',
          HELION_API_URL: 'http://coordinator:8080',
        },
      }],
    }));
    expect(errors).toEqual([]);
  });
});
