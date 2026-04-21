// src/app/core/services/workflow-draft.service.spec.ts
//
// Feature 41 — draft persistence unit spec.

import { TestBed } from '@angular/core/testing';
import { firstValueFrom, take, toArray } from 'rxjs';

import { WorkflowDraftService } from './workflow-draft.service';
import { SubmitWorkflowRequest } from '../../shared/models';

const STORAGE_KEY = WorkflowDraftService.STORAGE_KEY;

const nonTrivialBody: SubmitWorkflowRequest = {
  id:   'wf-mnist-1',
  name: 'mnist parallel',
  jobs: [
    { name: 'ingest',     command: 'python' },
    { name: 'preprocess', command: 'python', depends_on: ['ingest'] },
  ],
};

function freshService(): WorkflowDraftService {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({ providers: [WorkflowDraftService] });
  return TestBed.inject(WorkflowDraftService);
}

describe('WorkflowDraftService', () => {
  beforeEach(() => sessionStorage.clear());
  afterEach(() => sessionStorage.clear());

  it('should be created with no draft when storage is empty', async () => {
    const svc = freshService();
    expect(svc.load()).toBeNull();
    expect(await firstValueFrom(svc.snapshot$)).toBeNull();
  });

  it('round-trips a non-trivial draft through save → load', () => {
    const svc = freshService();
    svc.save(nonTrivialBody);

    const got = svc.load();
    expect(got).not.toBeNull();
    expect(got!.body).toEqual(nonTrivialBody);
    expect(got!.schema).toBe('v1');
    expect(new Date(got!.savedAt).getTime()).not.toBeNaN();
  });

  it('clears the slot when an effectively-empty body is saved', () => {
    const svc = freshService();
    svc.save(nonTrivialBody);
    expect(svc.load()).not.toBeNull();

    svc.save({ id: '', name: '', jobs: [] });
    expect(svc.load()).toBeNull();
  });

  it('treats a body with only a default "job-1" placeholder as empty', () => {
    const svc = freshService();
    svc.save({
      id:   '',
      name: '',
      jobs: [{ name: 'job-1', command: '' }],
    });
    // The DAG builder seeds a "job-1" row on "+ add job" before
    // any typing; a page-view auto-save on that shouldn't stick.
    expect(svc.load()).toBeNull();
  });

  it('emits the new draft on snapshot$ after save', async () => {
    const svc = freshService();
    const collected = firstValueFrom(svc.snapshot$.pipe(take(2), toArray()));
    svc.save(nonTrivialBody);
    const emissions = await collected;
    expect(emissions[0]).toBeNull();                          // initial (empty)
    expect(emissions[1]?.body.id).toBe(nonTrivialBody.id);    // after save
  });

  it('clear() wipes storage and emits null', async () => {
    const svc = freshService();
    svc.save(nonTrivialBody);
    svc.clear();
    expect(sessionStorage.getItem(STORAGE_KEY)).toBeNull();
    expect(await firstValueFrom(svc.snapshot$)).toBeNull();
  });

  it('rejects a schema-mismatched blob without hydrating the form', () => {
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify({
      schema: 'v99',
      savedAt: new Date().toISOString(),
      body: nonTrivialBody,
    }));
    const svc = freshService();
    expect(svc.load()).toBeNull();
    // Corrupt entry must be wiped so subsequent reads aren't
    // poisoned — otherwise a stale v99 blob would lurk forever.
    expect(sessionStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it('rejects corrupt JSON without hydrating the form', () => {
    sessionStorage.setItem(STORAGE_KEY, '{not-json');
    const svc = freshService();
    expect(svc.load()).toBeNull();
    expect(sessionStorage.getItem(STORAGE_KEY)).toBeNull();
  });

  it('rejects a tampered body with the wrong shape', () => {
    // jobs array missing the required command field on one entry —
    // would crash the DAG-builder hydration if we trusted it blind.
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify({
      schema: 'v1',
      savedAt: new Date().toISOString(),
      body: { id: 'x', name: 'y', jobs: [{ name: 'oops' }] },
    }));
    const svc = freshService();
    expect(svc.load()).toBeNull();
  });

  it('is stable across service re-instantiation (same tab/session)', () => {
    const first = freshService();
    first.save(nonTrivialBody);

    // Simulating a route-change re-injection. Same sessionStorage,
    // fresh BehaviorSubject — initial emission must be the saved
    // draft, not null.
    const second = freshService();
    const loaded = second.load();
    expect(loaded).not.toBeNull();
    expect(loaded!.body).toEqual(nonTrivialBody);
  });
});
