// src/app/features/submit/submit-dag-builder.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';
import { HttpErrorResponse } from '@angular/common/http';

import { SubmitDagBuilderComponent } from './submit-dag-builder.component';
import { ApiService } from '../../core/services/api.service';
import { Workflow, SubmitWorkflowJobRequest } from '../../shared/models';

describe('SubmitDagBuilderComponent', () => {
  let fixture:   ComponentFixture<SubmitDagBuilderComponent>;
  let component: SubmitDagBuilderComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;
  let routerSpy: jasmine.SpyObj<Router>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', ['submitWorkflow']);
    apiSpy.submitWorkflow.and.returnValue(of({
      id: 'wf-1', name: 't', status: 'pending', jobs: [], created_at: '',
    } as Workflow));
    routerSpy = jasmine.createSpyObj<Router>('Router', ['navigate']);

    await TestBed.configureTestingModule({
      imports: [SubmitDagBuilderComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
        { provide: Router, useValue: routerSpy },
      ],
    }).compileComponents();
    fixture = TestBed.createComponent(SubmitDagBuilderComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  // ── Initial state ──────────────────────────────────────────────

  it('renders with no jobs and an empty generated-JSON preview', () => {
    expect(component.jobControls.length).toBe(0);
    expect(component.activeIdx).toBe(-1);
  });

  // ── Adding + removing jobs ────────────────────────────────────

  it('addJob appends a blank job and selects it', () => {
    component.addJob();
    expect(component.jobControls.length).toBe(1);
    expect(component.activeIdx).toBe(0);
    expect(component.activeJob).not.toBeNull();
  });

  it('addJob assigns a default name scoped to the current count', () => {
    component.addJob();
    component.addJob();
    expect(component.jobControls[0].value.name).toBe('job-1');
    expect(component.jobControls[1].value.name).toBe('job-2');
  });

  it('removeJob strips the job from every other job\'s depends_on', () => {
    // Regression: removing an upstream must not leave downstream
    // jobs referencing a missing name — otherwise the shape
    // validator would reject the workflow as soon as the user
    // tried to submit.
    component.addJob();  // job-1
    component.addJob();  // job-2
    component.toggleDep(component.jobControls[1], 'job-1', true);
    expect(component.jobControls[1].value.depends_on).toEqual(['job-1']);

    component.removeJob(0);
    expect(component.jobControls.length).toBe(1);
    expect(component.jobControls[0].value.depends_on).toEqual([]);
  });

  // ── depends_on ──────────────────────────────────────────────────

  it('dependsOnCandidates excludes the current job itself', () => {
    component.addJob();  // job-1
    component.addJob();  // job-2
    component.addJob();  // job-3
    const cands = component.dependsOnCandidates(component.jobControls[1]);
    expect(cands.sort()).toEqual(['job-1', 'job-3']);
  });

  it('toggleDep adds and removes entries', () => {
    component.addJob();  // job-1
    component.addJob();  // job-2
    const j2 = component.jobControls[1];
    component.toggleDep(j2, 'job-1', true);
    expect(component.isDependedOn(j2, 'job-1')).toBeTrue();
    component.toggleDep(j2, 'job-1', false);
    expect(component.isDependedOn(j2, 'job-1')).toBeFalse();
  });

  // ── Body build shape ──────────────────────────────────────────

  it('buildBody assembles the form into a valid SubmitWorkflowRequest', () => {
    component.metaForm.patchValue({ id: 'my-wf', name: 'demo' });
    component.addJob();
    component.jobControls[0].patchValue({
      name:    'ingest',
      command: 'python',
      argsRaw: '/app/script.py\n--flag',
      envRaw:  'PYTHONPATH=/app\nHELION_API_URL=http://coordinator:8080',
      timeout_seconds: 60,
      selectorRaw: 'runtime=rust',
    });
    component.addJob();
    component.jobControls[1].patchValue({
      name:    'train',
      command: 'python',
      argsRaw: '/app/train.py',
    });
    component.toggleDep(component.jobControls[1], 'ingest', true);

    component.onValidate();
    expect(component.validationErrors).toEqual([]);
    expect(component.validationOk).toBeTrue();

    const body = component.previewBody as { id: string; jobs: SubmitWorkflowJobRequest[] };
    expect(body.id).toBe('my-wf');
    expect(body.jobs.length).toBe(2);
    const ingest = body.jobs[0];
    expect(ingest.name).toBe('ingest');
    expect(ingest.args).toEqual(['/app/script.py', '--flag']);
    expect(ingest.env).toEqual({
      PYTHONPATH:     '/app',
      HELION_API_URL: 'http://coordinator:8080',
    });
    expect(ingest.timeout_seconds).toBe(60);
    expect(ingest.node_selector).toEqual({ runtime: 'rust' });
    expect(body.jobs[1].depends_on).toEqual(['ingest']);
  });

  // ── Validation ─────────────────────────────────────────────────

  it('reports errors when validator rejects the built body', () => {
    // No id, no jobs.
    component.onValidate();
    expect(component.validationOk).toBeFalse();
    expect(component.validationErrors.length).toBeGreaterThan(0);
  });

  it('any edit clears the validationOk indicator', () => {
    component.metaForm.patchValue({ id: 'wf-1', name: 't' });
    component.addJob();
    component.jobControls[0].patchValue({ name: 'a', command: 'echo' });
    component.onValidate();
    expect(component.validationOk).toBeTrue();

    component.jobControls[0].patchValue({ command: 'python' });
    expect(component.validationOk).toBeFalse();
  });

  // ── Submit flow ─────────────────────────────────────────────────

  it('Submit posts through the shared /workflows path and navigates on success', () => {
    component.metaForm.patchValue({ id: 'wf-1', name: 't' });
    component.addJob();
    component.jobControls[0].patchValue({ name: 'a', command: 'echo' });
    component.onValidate();
    component.openPreview();
    component.onPreviewResolved('confirm');

    expect(apiSpy.submitWorkflow).toHaveBeenCalledTimes(1);
    expect(routerSpy.navigate).toHaveBeenCalledWith(['/ml/pipelines', 'wf-1']);
  });

  it('surfaces server errors', () => {
    apiSpy.submitWorkflow.and.returnValue(throwError(() => new HttpErrorResponse({
      error: { error: 'server rejected' },
      status: 400,
    })));
    component.metaForm.patchValue({ id: 'wf-1', name: 't' });
    component.addJob();
    component.jobControls[0].patchValue({ name: 'a', command: 'echo' });
    component.onValidate();
    component.openPreview();
    component.onPreviewResolved('confirm');

    expect(component.submitError).toBe('server rejected');
    expect(routerSpy.navigate).not.toHaveBeenCalled();
  });

  // ── Live preview ───────────────────────────────────────────────

  it('generatedJSON updates as the user edits', () => {
    const before = component.generatedJSON;
    component.metaForm.patchValue({ id: 'fresh-id', name: 'x' });
    expect(component.generatedJSON).not.toBe(before);
    expect(component.generatedJSON).toContain('"fresh-id"');
  });
});
