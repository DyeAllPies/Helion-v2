// src/app/features/admin/operator-certs.component.spec.ts
//
// Feature 32 — component tests. Cover:
//
//   - Form validation mirrors server rules.
//   - Issue flow: POST body matches form; success path builds
//     a blob URL; password stored once in component state.
//   - Password-shown-once: ticking the confirmation checkbox
//     clears the password from view and locks the input.
//   - ngOnDestroy zeros the P12 + password state (the security
//     contract).
//   - 403 surfaces a recognisable admin-required message.
//   - Revoke happy path + idempotent repeat.
//
// Uses ApiService via TestBed + HttpTestingController so every
// request shape is asserted alongside the UI behaviour.

import { ComponentFixture, TestBed, fakeAsync, tick } from '@angular/core/testing';
import { provideHttpClient } from '@angular/common/http';
import {
  HttpTestingController, provideHttpClientTesting,
} from '@angular/common/http/testing';
import { NoopAnimationsModule } from '@angular/platform-browser/animations';

import { OperatorCertsComponent } from './operator-certs.component';
import { ApiService } from '../../core/services/api.service';

describe('OperatorCertsComponent', () => {
  let component: OperatorCertsComponent;
  let fixture: ComponentFixture<OperatorCertsComponent>;
  let httpMock: HttpTestingController;
  let createObjectURLSpy: jasmine.Spy;
  let revokeObjectURLSpy: jasmine.Spy;

  beforeEach(() => {
    TestBed.configureTestingModule({
      imports: [OperatorCertsComponent, NoopAnimationsModule],
      providers: [
        ApiService,
        provideHttpClient(),
        provideHttpClientTesting(),
      ],
    });
    fixture = TestBed.createComponent(OperatorCertsComponent);
    component = fixture.componentInstance;
    httpMock = TestBed.inject(HttpTestingController);

    // createObjectURL in jsdom/Karma-chrome returns a
    // string URL; we spy on it so assertions don't depend
    // on the specific format.
    createObjectURLSpy = spyOn(URL, 'createObjectURL')
      .and.returnValue('blob:https://helion.test/fake-uuid');
    revokeObjectURLSpy = spyOn(URL, 'revokeObjectURL');

    // Initial load triggers a revocations GET. Flush an
    // empty list so the subscription completes and every
    // per-test expectOne below starts from a clean slate.
    fixture.detectChanges();
    const initReq = httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs/revocations'));
    initReq.flush({ total: 0, revocations: [] });
  });

  afterEach(() => httpMock.verify());

  // ── Form validation ─────────────────────────────────

  it('issue form rejects empty common_name', () => {
    component.issueForm.patchValue({ common_name: '', p12_password: 'abcdefgh' });
    expect(component.issueForm.invalid).toBeTrue();
  });

  it('issue form rejects short password', () => {
    component.issueForm.patchValue({ common_name: 'alice', p12_password: 'short' });
    expect(component.issueForm.get('p12_password')?.invalid).toBeTrue();
  });

  it('issue form rejects common_name containing "="', () => {
    component.issueForm.patchValue({ common_name: 'cn=bad', p12_password: 'validpass' });
    expect(component.issueForm.get('common_name')?.errors).toBeTruthy();
  });

  it('issue form caps ttl_days at 365', () => {
    component.issueForm.patchValue({ ttl_days: 500 });
    expect(component.issueForm.get('ttl_days')?.invalid).toBeTrue();
  });

  // ── Password generator ──────────────────────────────

  it('generatePassword() fills a 24-char password into the form', () => {
    component.generatePassword();
    const pw = component.issueForm.value.p12_password as string;
    expect(pw.length).toBe(24);
    // Password alphabet excludes ambiguous chars.
    expect(pw).not.toContain('0');
    expect(pw).not.toContain('O');
    expect(pw).not.toContain('I');
  });

  // ── Issue flow ─────────────────────────────────────

  it('issue() sends the form values to POST /admin/operator-certs', () => {
    component.issueForm.patchValue({
      common_name: 'alice@ops',
      ttl_days: 90,
      p12_password: 'goodpassword',
    });
    component.issue();

    const req = httpMock.expectOne(r =>
      r.url.endsWith('/admin/operator-certs') && r.method === 'POST');
    expect(req.request.body).toEqual({
      common_name: 'alice@ops',
      ttl_days: 90,
      p12_password: 'goodpassword',
    });
    req.flush({
      common_name: 'alice@ops', serial_hex: 'abcd', fingerprint_hex: 'beef',
      not_before: '2026-04-20T00:00:00Z', not_after: '2026-07-19T00:00:00Z',
      cert_pem: '---', key_pem: '---', p12_base64: btoa('fake-p12-bytes'),
      audit_notice: 'logged',
    });

    expect(component.issued).toBeTruthy();
    expect(component.issuedPassword).toBe('goodpassword');
    expect(component.p12BlobUrl).toBe('blob:https://helion.test/fake-uuid');
    expect(createObjectURLSpy).toHaveBeenCalled();
    // Form password cleared so a reissue cycle can't quietly reuse it.
    expect(component.issueForm.value.p12_password).toBe('');
  });

  it('issue() maps 403 to an admin-required message', () => {
    component.issueForm.patchValue({
      common_name: 'alice',
      ttl_days: 90,
      p12_password: 'goodpassword',
    });
    component.issue();
    const req = httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs'));
    req.flush({ error: 'forbidden' }, { status: 403, statusText: 'Forbidden' });

    expect(component.issueError).toMatch(/admin role required/i);
    expect(component.issued).toBeNull();
  });

  it('issue() maps server 400 body to the error banner', () => {
    component.issueForm.patchValue({
      common_name: 'alice', ttl_days: 90, p12_password: 'goodpassword',
    });
    component.issue();
    const req = httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs'));
    req.flush({ error: 'common_name must be non-empty' },
              { status: 400, statusText: 'Bad Request' });
    expect(component.issueError).toContain('common_name must be non-empty');
  });

  // ── Password-shown-once / download ─────────────────

  it('onPasswordSaved() clears the password + locks the input', fakeAsync(() => {
    component.issueForm.patchValue({
      common_name: 'alice', ttl_days: 90, p12_password: 'goodpassword',
    });
    component.issue();
    httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs')).flush({
      common_name: 'alice', serial_hex: 'abcd', fingerprint_hex: 'beef',
      not_before: '', not_after: '', cert_pem: '', key_pem: '',
      p12_base64: btoa('x'), audit_notice: '',
    });
    tick();
    expect(component.issuedPassword).toBe('goodpassword');
    expect(component.passwordLocked).toBeFalse();

    component.passwordSaved = true;
    component.onPasswordSaved();
    expect(component.issuedPassword).toBe('');
    expect(component.passwordLocked).toBeTrue();
  }));

  it('ngOnDestroy zeros P12 blob + password state', () => {
    component.p12BlobUrl = 'blob:fake';
    component.issuedPassword = 'stillhere';
    component.issued = { serial_hex: 'abcd' } as any;
    component.ngOnDestroy();
    expect(component.p12BlobUrl).toBeNull();
    expect(component.issuedPassword).toBe('');
    expect(component.issued).toBeNull();
    expect(revokeObjectURLSpy).toHaveBeenCalledWith('blob:fake');
  });

  it('clearIssued() drops all issued state', () => {
    component.p12BlobUrl = 'blob:fake';
    component.issuedPassword = 'stillhere';
    component.issued = { serial_hex: 'abcd' } as any;
    component.passwordSaved = true;
    component.passwordLocked = true;

    component.clearIssued();

    expect(component.issued).toBeNull();
    expect(component.p12BlobUrl).toBeNull();
    expect(component.issuedPassword).toBe('');
    expect(component.passwordSaved).toBeFalse();
    expect(component.passwordLocked).toBeFalse();
  });

  it('downloadP12() triggers a click + revokes the blob URL', () => {
    component.p12BlobUrl = 'blob:fake';
    component.issued = { common_name: 'alice@ops' } as any;
    // Spy on the real <a>.click so we observe the invocation
    // without stubbing document.createElement (which would
    // break appendChild + break Karma's DOM setup).
    const realAnchor = document.createElement('a');
    const createSpy = spyOn(document, 'createElement').and.callFake((tag: string) => {
      if (tag === 'a') return realAnchor;
      return new (document as any).defaultView.Document().createElement(tag);
    });
    spyOn(realAnchor, 'click');

    component.downloadP12();

    expect(createSpy).toHaveBeenCalledWith('a');
    expect(realAnchor.click).toHaveBeenCalled();
    expect(realAnchor.download).toBe('alice@ops.p12');
    expect(revokeObjectURLSpy).toHaveBeenCalledWith('blob:fake');
    expect(component.p12BlobUrl).toBeNull();
  });

  // ── Revoke flow ────────────────────────────────────

  it('revoke() sends POST to the revoke endpoint with the reason', () => {
    component.revokeForm.patchValue({ serial: 'deadbeef', reason: 'alice left' });
    component.revoke();

    const req = httpMock.expectOne(r =>
      r.url.endsWith('/admin/operator-certs/deadbeef/revoke') && r.method === 'POST');
    expect(req.request.body).toEqual({ reason: 'alice left' });
    req.flush({
      serial_hex: 'deadbeef', revoked_at: '2026-04-20T00:00:00Z',
      revoked_by: 'user:root', reason: 'alice left', idempotent: false,
    });

    // After a successful revoke the form reloads the list.
    const listReq = httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs/revocations'));
    listReq.flush({ total: 1, revocations: [
      { serial_hex: 'deadbeef', common_name: '', revoked_at: '',
        revoked_by: '', reason: 'alice left' },
    ] });

    expect(component.revokeSuccess?.serial_hex).toBe('deadbeef');
    expect(component.revokeSuccess?.idempotent).toBeFalse();
    // Reason is cleared so accidental double-submit doesn't
    // reuse the same text.
    expect(component.revokeForm.value.reason).toBe('');
  });

  it('revoke() strips an 0x prefix from the serial before sending', () => {
    component.revokeForm.patchValue({ serial: '0xBEEF', reason: 'x' });
    component.revoke();
    httpMock.expectOne(r =>
      r.url.endsWith('/admin/operator-certs/BEEF/revoke') && r.method === 'POST'
    ).flush({ serial_hex: 'beef', revoked_at: '', revoked_by: '', idempotent: false });
    httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs/revocations'))
      .flush({ total: 0, revocations: [] });
  });

  it('revoke form rejects non-hex serial', () => {
    component.revokeForm.patchValue({ serial: 'not-hex', reason: 'x' });
    expect(component.revokeForm.get('serial')?.invalid).toBeTrue();
  });

  it('revoke form requires a reason', () => {
    component.revokeForm.patchValue({ serial: 'abcd', reason: '' });
    expect(component.revokeForm.invalid).toBeTrue();
  });

  // ── List ───────────────────────────────────────────

  it('loadRevocations() populates the revocations array', () => {
    component.loadRevocations();
    const req = httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs/revocations'));
    req.flush({
      total: 2,
      revocations: [
        { serial_hex: 'a1', common_name: 'alice', revoked_at: '', revoked_by: '', reason: '' },
        { serial_hex: 'b2', common_name: 'bob',   revoked_at: '', revoked_by: '', reason: '' },
      ],
    });
    expect(component.revocations?.length).toBe(2);
  });

  it('loadRevocations() surfaces a 403 error inline', () => {
    component.loadRevocations();
    const req = httpMock.expectOne(r => r.url.endsWith('/admin/operator-certs/revocations'));
    req.flush({ error: 'forbidden' }, { status: 403, statusText: 'Forbidden' });
    expect(component.revocationsLoadError).toMatch(/admin role required/i);
  });
});
