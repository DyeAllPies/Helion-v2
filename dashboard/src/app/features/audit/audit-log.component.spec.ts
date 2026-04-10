// src/app/features/audit/audit-log.component.spec.ts
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideAnimations } from '@angular/platform-browser/animations';
import { provideRouter } from '@angular/router';
import { of, throwError } from 'rxjs';

import { AuditLogComponent } from './audit-log.component';
import { ApiService } from '../../core/services/api.service';
import { AuditEvent, AuditPage } from '../../shared/models';

function makeEvent(type: string, actor = 'system'): AuditEvent {
  return {
    id: Math.random().toString(36).slice(2),
    type: type as AuditEvent['type'],
    timestamp: new Date().toISOString(),
    actor,
    message: `${type} occurred`,
  };
}

const mockPage: AuditPage = {
  events: [
    makeEvent('job_submit', 'api-user'),
    makeEvent('job_state_transition'),
    makeEvent('node_register', 'node-1'),
    makeEvent('node_revoke', 'admin'),
    makeEvent('security_violation', 'node-2'),
    makeEvent('rate_limit_hit', 'node-3'),
    makeEvent('auth_failure', 'unknown'),
    makeEvent('coordinator_start'),
    makeEvent('coordinator_stop'),
  ],
  total: 9, page: 0, size: 50,
};

describe('AuditLogComponent', () => {
  let fixture:   ComponentFixture<AuditLogComponent>;
  let component: AuditLogComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj('ApiService', ['getAudit']);
    apiSpy.getAudit.and.returnValue(of(mockPage));

    await TestBed.configureTestingModule({
      imports: [AuditLogComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();

    fixture   = TestBed.createComponent(AuditLogComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('should create', () => expect(component).toBeTruthy());

  it('should load events on init', () => {
    expect(apiSpy.getAudit).toHaveBeenCalled();
    expect(component.events.length).toBe(9);
    expect(component.total).toBe(9);
  });

  it('should show error banner on API failure', () => {
    apiSpy.getAudit.and.returnValue(throwError(() => new Error('fetch failed')));
    component.load();
    fixture.detectChanges();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.error-banner')).toBeTruthy();
  });

  it('should pass type param to API when filter is set', () => {
    component.typeFilter = 'security_violation';
    component.load();
    expect(apiSpy.getAudit).toHaveBeenCalledWith(0, 50, 'security_violation');
  });

  it('should reset to page 0 on filter change', () => {
    component.pageIndex  = 3;
    component.typeFilter = 'node_register';
    component.onFilterChange();
    expect(component.pageIndex).toBe(0);
    expect(apiSpy.getAudit).toHaveBeenCalledWith(0, 50, 'node_register');
  });

  // ── eventClass() — CSS badge mapping ──────────────────────────────────────

  it('eventClass: security_violation → evt-security (red badge)', () => {
    expect(component.eventClass('security_violation')).toBe('evt-security');
  });

  it('eventClass: rate_limit_hit → evt-rate', () => {
    expect(component.eventClass('rate_limit_hit')).toBe('evt-rate');
  });

  it('eventClass: coordinator_start → evt-coordinator', () => {
    expect(component.eventClass('coordinator_start')).toBe('evt-coordinator');
  });

  it('eventClass: coordinator_stop → evt-coordinator', () => {
    expect(component.eventClass('coordinator_stop')).toBe('evt-coordinator');
  });

  it('eventClass: job_submit → evt-job', () => {
    expect(component.eventClass('job_submit')).toBe('evt-job');
  });

  it('eventClass: job_state_transition → evt-job', () => {
    expect(component.eventClass('job_state_transition')).toBe('evt-job');
  });

  it('eventClass: node_register → evt-node', () => {
    expect(component.eventClass('node_register')).toBe('evt-node');
  });

  it('eventClass: node_revoke → evt-node', () => {
    expect(component.eventClass('node_revoke')).toBe('evt-node');
  });

  it('eventClass: auth_failure → evt-auth', () => {
    expect(component.eventClass('auth_failure')).toBe('evt-auth');
  });

  it('eventClass: unknown future type falls back to first-word prefix', () => {
    expect(component.eventClass('foo_bar_baz')).toBe('evt-foo');
  });

  // ── Filter dropdown contains all real Go event types ──────────────────────

  it('eventTypes list includes security_violation', () => {
    expect(component.eventTypes).toContain('security_violation');
  });

  it('eventTypes list includes rate_limit_hit', () => {
    expect(component.eventTypes).toContain('rate_limit_hit');
  });

  it('eventTypes list includes job_submit (not job_submitted)', () => {
    expect(component.eventTypes).toContain('job_submit');
    expect(component.eventTypes).not.toContain('job_submitted');
  });

  it('eventTypes list includes node_register (not node_registered)', () => {
    expect(component.eventTypes).toContain('node_register');
    expect(component.eventTypes).not.toContain('node_registered');
  });
});
