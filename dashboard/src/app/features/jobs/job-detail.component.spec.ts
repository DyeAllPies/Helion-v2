// src/app/features/jobs/job-detail.component.spec.ts
import { ElementRef } from '@angular/core';
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, Subject, throwError } from 'rxjs';

import { JobDetailComponent } from './job-detail.component';
import { ApiService } from '../../core/services/api.service';
import { WebSocketService } from '../../core/services/websocket.service';
import { Job, LogChunk } from '../../shared/models';

const mockJob: Job = {
  id: 'job-test-001', node_id: 'node-abc', command: 'echo',
  args: ['hello'], status: 'running',
  created_at: new Date().toISOString(),
};

const mockCompletedJob: Job = { ...mockJob, status: 'completed', finished_at: new Date().toISOString(), exit_code: 0 };

describe('JobDetailComponent', () => {
  let fixture:   ComponentFixture<JobDetailComponent>;
  let component: JobDetailComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;
  let wsSpy:     jasmine.SpyObj<WebSocketService>;
  let logSubject: Subject<LogChunk>;

  beforeEach(async () => {
    logSubject = new Subject<LogChunk>();

    apiSpy = jasmine.createSpyObj('ApiService', ['getJob']);
    apiSpy.getJob.and.returnValue(of(mockJob));

    wsSpy = jasmine.createSpyObj('WebSocketService', ['jobLogs']);
    wsSpy.jobLogs.and.returnValue(logSubject.asObservable());

    await TestBed.configureTestingModule({
      imports: [JobDetailComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService,      useValue: apiSpy },
        { provide: WebSocketService, useValue: wsSpy },
      ],
    }).compileComponents();

    fixture   = TestBed.createComponent(JobDetailComponent);
    component = fixture.componentInstance;
    component.id = 'job-test-001';
    fixture.detectChanges();
  });

  afterEach(() => {
    logSubject.complete();
    fixture.destroy();
  });

  it('should create', () => expect(component).toBeTruthy());

  it('should load job metadata on init', () => {
    expect(apiSpy.getJob).toHaveBeenCalledWith('job-test-001');
    expect(component.job).toEqual(mockJob);
  });

  it('should show wsConnected = true while job is running and WS is open', () => {
    expect(component.wsConnected).toBeTrue();
  });

  it('should append log lines from WebSocket', () => {
    const chunk: LogChunk = {
      job_id: 'job-test-001', sequence: 1,
      text: 'line one\nline two', timestamp: new Date().toISOString(),
    };
    logSubject.next(chunk);
    expect(component.logLines).toEqual(['line one', 'line two']);
  });

  it('should set wsEnded = true and wsConnected = false on WS complete', () => {
    logSubject.complete();
    expect(component.wsConnected).toBeFalse();
    expect(component.wsEnded).toBeTrue();
  });

  it('should NOT connect WebSocket for a completed job', () => {
    apiSpy.getJob.and.returnValue(of(mockCompletedJob));
    component.ngOnInit();
    fixture.detectChanges();
    // wsConnected stays false for terminal jobs
    expect(wsSpy.jobLogs).toHaveBeenCalledTimes(1); // only first call (running job)
  });

  it('should populate error and clear loading when getJob fails', () => {
    apiSpy.getJob.and.returnValue(throwError(() => new Error('boom')));
    component.loadJob();
    expect(component.loading).toBeFalse();
    // AUDIT 2026-04-12-01/M2: error message is now generic (no raw details).
    expect(component.error).toContain('Job not found');
    // Restore so afterEach's logSubject.complete() -> refreshMeta() doesn't
    // resubscribe to the error observable and surface as an unhandled throw.
    apiSpy.getJob.and.returnValue(of(mockJob));
  });

  it('refreshMeta should re-fetch the job and update state', () => {
    const updated: Job = { ...mockJob, status: 'completed', exit_code: 0 };
    apiSpy.getJob.calls.reset();
    apiSpy.getJob.and.returnValue(of(updated));
    component.refreshMeta();
    expect(apiSpy.getJob).toHaveBeenCalledWith('job-test-001');
    expect(component.job?.status).toBe('completed');
  });

  it('scrollToBottom should set scrollTop to scrollHeight on the log body', () => {
    const el = { scrollTop: 0, scrollHeight: 999 } as HTMLDivElement;
    component.logBodyRef = { nativeElement: el } as ElementRef<HTMLDivElement>;
    component.scrollToBottom();
    expect(el.scrollTop).toBe(999);
  });

  it('scrollToBottom should be a no-op if logBodyRef is missing', () => {
    component.logBodyRef = undefined as unknown as ElementRef<HTMLDivElement>;
    expect(() => component.scrollToBottom()).not.toThrow();
  });

  it('should mark wsEnded and refresh metadata on WS error', () => {
    apiSpy.getJob.calls.reset();
    apiSpy.getJob.and.returnValue(of(mockCompletedJob));
    logSubject.error(new Error('socket dropped'));
    expect(component.wsConnected).toBeFalse();
    expect(component.wsEnded).toBeTrue();
    expect(apiSpy.getJob).toHaveBeenCalledWith('job-test-001');
  });

  it('ngOnDestroy should unsubscribe from the WS subscription', () => {
    // The component subscribed in beforeEach via loadJob -> connectLogs.
    // Pushing after destroy must not append further lines.
    component.ngOnDestroy();
    logSubject.next({
      job_id: 'job-test-001', sequence: 99,
      text: 'after-destroy', timestamp: new Date().toISOString(),
    });
    expect(component.logLines).not.toContain('after-destroy');
  });

  it('ngAfterViewChecked should scroll when needsScroll flips true', () => {
    const el = { scrollTop: 0, scrollHeight: 1234 } as HTMLDivElement;
    component.logBodyRef = { nativeElement: el } as ElementRef<HTMLDivElement>;
    logSubject.next({
      job_id: 'job-test-001', sequence: 1,
      text: 'a\nb', timestamp: new Date().toISOString(),
    });
    component.ngAfterViewChecked();
    expect(el.scrollTop).toBe(1234);
  });
});
