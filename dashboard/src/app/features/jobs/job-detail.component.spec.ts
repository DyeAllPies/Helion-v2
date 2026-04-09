// src/app/features/jobs/job-detail.component.spec.ts
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, Subject } from 'rxjs';

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
});
