// src/app/features/submit/submit-workflow.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';
import { HttpErrorResponse } from '@angular/common/http';

import { SubmitWorkflowComponent } from './submit-workflow.component';
import { ApiService } from '../../core/services/api.service';
import { Workflow } from '../../shared/models';

function stubWorkflow(over: Partial<Workflow> = {}): Workflow {
  return {
    id:         'wf-1',
    name:       'test',
    status:     'pending',
    jobs:       [],
    created_at: new Date().toISOString(),
    ...over,
  };
}

describe('SubmitWorkflowComponent', () => {
  let fixture:   ComponentFixture<SubmitWorkflowComponent>;
  let component: SubmitWorkflowComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;
  let routerSpy: jasmine.SpyObj<Router>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', ['submitWorkflow']);
    apiSpy.submitWorkflow.and.returnValue(of(stubWorkflow()));
    routerSpy = jasmine.createSpyObj<Router>('Router', ['navigate']);

    await TestBed.configureTestingModule({
      imports: [SubmitWorkflowComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
        { provide: Router, useValue: routerSpy },
      ],
    }).compileComponents();
    fixture = TestBed.createComponent(SubmitWorkflowComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  const validBody = () => JSON.stringify({
    id: 'wf-1', name: 'test',
    jobs: [{ name: 'hello', command: 'echo', args: ['hi'] }],
  });

  it('renders with empty state', () => {
    expect(component.parseError).toBeNull();
    expect(component.validationErrors).toEqual([]);
    expect(component.validationOk).toBeFalse();
  });

  it('flags empty body as a parse error (not a schema error)', () => {
    component.form.patchValue({ text: '   ' });
    component.onValidate();
    expect(component.parseError).toContain('empty');
    expect(component.validationOk).toBeFalse();
  });

  it('surfaces JSON.parse errors verbatim', () => {
    component.form.patchValue({ text: '{not valid json' });
    component.onValidate();
    expect(component.parseError).not.toBeNull();
    expect(component.validationErrors).toEqual([]);
  });

  it('passes a valid workflow body through the shape validator', () => {
    component.form.patchValue({ text: validBody() });
    component.onValidate();
    expect(component.parseError).toBeNull();
    expect(component.validationErrors).toEqual([]);
    expect(component.validationOk).toBeTrue();
    expect(component.parsedId).toBe('wf-1');
  });

  it('flags a workflow missing jobs', () => {
    component.form.patchValue({ text: JSON.stringify({ id: 'x', name: 'x' }) });
    component.onValidate();
    expect(component.validationOk).toBeFalse();
    expect(component.validationErrors.length).toBeGreaterThan(0);
  });

  it('rejects LD_PRELOAD inside a job env (same denylist as form)', () => {
    component.form.patchValue({
      text: JSON.stringify({
        id: 'wf-1', name: 'test',
        jobs: [{ name: 'a', command: 'echo', env: { LD_PRELOAD: '/tmp/evil.so' } }],
      }),
    });
    component.onValidate();
    expect(component.validationOk).toBeFalse();
    expect(component.validationErrors.some(e => e.includes('LD_PRELOAD'))).toBeTrue();
  });

  it('editing the textarea clears the OK indicator', () => {
    component.form.patchValue({ text: validBody() });
    component.onValidate();
    expect(component.validationOk).toBeTrue();

    component.form.patchValue({ text: validBody() + ' ' });
    expect(component.validationOk).toBeFalse();
  });

  it('openPreview populates the dialog body with the parsed workflow', () => {
    component.form.patchValue({ text: validBody() });
    component.onValidate();
    component.openPreview();
    expect(component.previewOpen).toBeTrue();
    const body = component.previewBody as { id: string };
    expect(body.id).toBe('wf-1');
  });

  it('Submit confirm posts to /workflows and navigates to the pipeline detail', () => {
    component.form.patchValue({ text: validBody() });
    component.onValidate();
    component.openPreview();
    component.onPreviewResolved('confirm');

    expect(apiSpy.submitWorkflow).toHaveBeenCalledTimes(1);
    expect(routerSpy.navigate).toHaveBeenCalledWith(['/ml/pipelines', 'wf-1']);
  });

  it('Preview "dismiss" does not post', () => {
    component.form.patchValue({ text: validBody() });
    component.onValidate();
    component.openPreview();
    component.onPreviewResolved('dismiss');
    expect(apiSpy.submitWorkflow).not.toHaveBeenCalled();
  });

  it('surfaces the server error on 400', () => {
    apiSpy.submitWorkflow.and.returnValue(throwError(() => new HttpErrorResponse({
      error: { error: 'duplicate workflow id' },
      status: 400,
    })));
    component.form.patchValue({ text: validBody() });
    component.onValidate();
    component.openPreview();
    component.onPreviewResolved('confirm');

    expect(component.submitError).toBe('duplicate workflow id');
    expect(routerSpy.navigate).not.toHaveBeenCalled();
  });

  it('renders the notice pointing to feature 24', () => {
    const notice = fixture.nativeElement.querySelector('.notice');
    expect(notice?.textContent).toContain('feature 24');
  });
});
