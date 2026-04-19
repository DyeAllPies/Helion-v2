// src/app/features/submit/submit-job.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';
import { HttpErrorResponse } from '@angular/common/http';

import { SubmitJobComponent } from './submit-job.component';
import { ApiService } from '../../core/services/api.service';
import { Job, SubmitJobRequest } from '../../shared/models';

function stubJob(over: Partial<Job> = {}): Job {
  return {
    id:         'test-job',
    node_id:    '',
    command:    'echo',
    args:       ['hi'],
    status:     'pending' as Job['status'],
    created_at: new Date().toISOString(),
    ...over,
  };
}

describe('SubmitJobComponent', () => {
  let fixture: ComponentFixture<SubmitJobComponent>;
  let component: SubmitJobComponent;
  let apiSpy: jasmine.SpyObj<ApiService>;
  let routerSpy: jasmine.SpyObj<Router>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', ['submitJob']);
    apiSpy.submitJob.and.returnValue(of(stubJob()));
    routerSpy = jasmine.createSpyObj<Router>('Router', ['navigate']);

    await TestBed.configureTestingModule({
      imports: [SubmitJobComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
        { provide: Router, useValue: routerSpy },
      ],
    }).compileComponents();
    fixture = TestBed.createComponent(SubmitJobComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('renders with an empty form and zero validation errors', () => {
    expect(component.form.value.id).toBe('');
    expect(component.form.value.command).toBe('');
    expect(component.validationErrors).toEqual([]);
    expect(component.validationOk).toBeFalse();
  });

  // ── Client-side validators ──────────────────────────────────────

  it('Validate flags missing id + command', () => {
    component.onValidate();
    // At minimum both required-field errors must surface; others
    // may also fire (e.g. GPUs must be ≥ 0 — that defaults to 0
    // so doesn't, but the test is robust to extra messages).
    expect(component.validationErrors.some(e => e.includes('JOB ID'))).toBeTrue();
    expect(component.validationErrors.some(e => e.includes('COMMAND'))).toBeTrue();
    expect(component.validationOk).toBeFalse();
  });

  it('Validate succeeds with just id + command filled', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.onValidate();
    expect(component.validationErrors).toEqual([]);
    expect(component.validationOk).toBeTrue();
  });

  it('rejects env key "LD_PRELOAD" with a useful error message', () => {
    // Feature 25 ships the server-side denylist; this client-
    // side check is the UX complement. Regression guard: if the
    // deny list drifts from the server, change both.
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.addEnvEntry();
    component.envControls[0].patchValue({ key: 'LD_PRELOAD', value: '/tmp/evil.so' });
    component.onValidate();
    expect(component.validationErrors.some(e => e.includes('LD_PRELOAD')))
      .toBeTrue();
    expect(component.validationErrors.some(e => e.includes('dynamic-loader')))
      .toBeTrue();
    expect(component.validationOk).toBeFalse();
  });

  it('rejects env key "DYLD_INSERT_LIBRARIES" (macOS LD_PRELOAD equivalent)', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.addEnvEntry();
    component.envControls[0].patchValue({
      key: 'DYLD_INSERT_LIBRARIES', value: '/tmp/evil.dylib',
    });
    component.onValidate();
    expect(component.validationOk).toBeFalse();
  });

  it('rejects env key "GCONV_PATH" (glibc exact match)', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.addEnvEntry();
    component.envControls[0].patchValue({ key: 'GCONV_PATH', value: '/x' });
    component.onValidate();
    expect(component.validationOk).toBeFalse();
  });

  it('accepts legitimate env keys like PYTHONPATH and HELION_TOKEN', () => {
    component.form.patchValue({ id: 'j-1', command: 'python' });
    component.addEnvEntry();
    component.envControls[0].patchValue({ key: 'PYTHONPATH', value: '/app' });
    component.addEnvEntry();
    component.envControls[1].patchValue({ key: 'HELION_TOKEN', value: 'tok' });
    component.onValidate();
    expect(component.validationErrors).toEqual([]);
    expect(component.validationOk).toBeTrue();
  });

  it('rejects malformed env keys (must match shell-identifier shape)', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.addEnvEntry();
    component.envControls[0].patchValue({ key: '1BAD', value: 'x' });
    component.onValidate();
    expect(component.validationOk).toBeFalse();
  });

  it('flags duplicate env keys', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.addEnvEntry();
    component.envControls[0].patchValue({ key: 'FOO', value: 'a' });
    component.addEnvEntry();
    component.envControls[1].patchValue({ key: 'FOO', value: 'b' });
    component.onValidate();
    expect(component.validationErrors.some(e => e.includes('duplicate'))).toBeTrue();
    expect(component.validationOk).toBeFalse();
  });

  it('flags timeout out of [0, 3600]', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo', timeout_seconds: 9999 });
    component.onValidate();
    expect(component.validationErrors.some(e => e.includes('TIMEOUT'))).toBeTrue();
    expect(component.validationOk).toBeFalse();
  });

  it('flags node_selector lines without an = separator', () => {
    component.form.patchValue({
      id: 'j-1', command: 'echo',
      nodeSelectorRaw: 'this is not valid',
    });
    component.onValidate();
    expect(component.validationErrors.some(e => e.includes('key=value'))).toBeTrue();
    expect(component.validationOk).toBeFalse();
  });

  it('parses node_selector "key=value" lines into the request body', () => {
    component.form.patchValue({
      id: 'j-1', command: 'echo',
      nodeSelectorRaw: 'runtime=rust\nrole=heavy',
    });
    component.onValidate();
    expect(component.validationOk).toBeTrue();
    // Peek at the build output by opening the preview.
    component.openPreview();
    const body = component.previewBody as SubmitJobRequest & { resources?: { gpus?: number } };
    expect(body.node_selector).toEqual({ runtime: 'rust', role: 'heavy' });
  });

  // ── Secret toggle UX ──────────────────────────────────────────────

  it('secret toggle flips the value input to type="password"', () => {
    // Ship feature 26 and the server will also redact on GET; for
    // now the toggle is pure client-side masking. Asserting the
    // type attribute is enough — no `value=` leaks to a visible
    // DOM attribute.
    component.addEnvEntry();
    component.envControls[0].patchValue({ key: 'API_KEY', value: 'sekret', secret: false });
    fixture.detectChanges();
    let input = fixture.nativeElement.querySelector('.env-entry__value') as HTMLInputElement;
    expect(input.type).toBe('text');

    component.envControls[0].patchValue({ secret: true });
    fixture.detectChanges();
    input = fixture.nativeElement.querySelector('.env-entry__value') as HTMLInputElement;
    expect(input.type).toBe('password');
  });

  // ── Preview + Submit flow ────────────────────────────────────────

  it('Preview button is disabled until client-side Validate passes', () => {
    const btn = fixture.nativeElement.querySelector('.btn--primary') as HTMLButtonElement;
    expect(btn.disabled).toBeTrue();

    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.onValidate();
    fixture.detectChanges();
    expect(btn.disabled).toBeFalse();
  });

  it('openPreview populates previewBody with the would-be POST shape', () => {
    component.form.patchValue({
      id: 'hello',
      command: 'python',
      argsRaw: '/app/script.py\n--flag',
      timeout_seconds: 60,
      priority: 70,
      gpus: 1,
    });
    component.onValidate();
    component.openPreview();

    expect(component.previewOpen).toBeTrue();
    const body = component.previewBody as SubmitJobRequest & { resources?: { gpus?: number } };
    expect(body.id).toBe('hello');
    expect(body.command).toBe('python');
    expect(body.args).toEqual(['/app/script.py', '--flag']);
    expect(body.timeout_seconds).toBe(60);
    expect(body.priority).toBe(70);
    expect(body.resources).toEqual({ gpus: 1 });
  });

  it('onPreviewResolved "dismiss" closes the modal without submitting', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.onValidate();
    component.openPreview();
    expect(component.previewOpen).toBeTrue();

    component.onPreviewResolved('dismiss');
    expect(component.previewOpen).toBeFalse();
    expect(apiSpy.submitJob).not.toHaveBeenCalled();
  });

  it('onPreviewResolved "confirm" POSTs and navigates on 200', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.onValidate();
    component.openPreview();
    component.onPreviewResolved('confirm');

    expect(apiSpy.submitJob).toHaveBeenCalledTimes(1);
    const args = apiSpy.submitJob.calls.mostRecent().args[0];
    expect(args.id).toBe('j-1');
    expect(routerSpy.navigate).toHaveBeenCalledWith(['/jobs', 'test-job']);
    expect(component.submitError).toBeNull();
  });

  it('surfaces the server error on 400', () => {
    apiSpy.submitJob.and.returnValue(throwError(() => new HttpErrorResponse({
      error: { error: 'command is required' },
      status: 400,
    })));
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.onValidate();
    component.openPreview();
    component.onPreviewResolved('confirm');

    expect(component.submitError).toBe('command is required');
    expect(routerSpy.navigate).not.toHaveBeenCalled();
  });

  // ── Editing resets the OK indicator ──────────────────────────────

  it('clears validationOk when the user edits any field after Validate', () => {
    component.form.patchValue({ id: 'j-1', command: 'echo' });
    component.onValidate();
    expect(component.validationOk).toBeTrue();

    component.form.patchValue({ command: 'python' });
    expect(component.validationOk).toBeFalse();
  });

  // ── UI sanity: the "waiting on feature X" notice is rendered ─────

  it('renders the notice pointing to feature 24 + feature 26', () => {
    const notice = fixture.nativeElement.querySelector('.notice');
    const text = notice?.textContent ?? '';
    expect(text).toContain('feature 24');
    expect(text).toContain('feature 26');
  });
});
