// src/app/features/jobs/job-list.component.spec.ts
import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';

import { JobListComponent } from './job-list.component';
import { ApiService } from '../../core/services/api.service';
import { Job, JobsPage } from '../../shared/models';

const makeJob = (id: string, status: Job['status']): Job => ({
  id, node_id: 'n1', command: 'echo', args: ['hi'],
  status, created_at: new Date().toISOString(),
});

const mockPage: JobsPage = {
  jobs: [
    makeJob('j1', 'completed'),
    makeJob('j2', 'failed'),
    makeJob('j3', 'running'),
  ],
  total: 3, page: 0, size: 25,
};

describe('JobListComponent', () => {
  let fixture:   ComponentFixture<JobListComponent>;
  let component: JobListComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj('ApiService', ['getJobs']);
    apiSpy.getJobs.and.returnValue(of(mockPage));

    await TestBed.configureTestingModule({
      imports: [JobListComponent],
      providers: [
        provideRouter([{ path: 'jobs/:id', component: {} as never }]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();

    fixture   = TestBed.createComponent(JobListComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('should create', () => expect(component).toBeTruthy());

  it('should load jobs on init', () => {
    expect(apiSpy.getJobs).toHaveBeenCalledWith(0, 25, undefined);
    expect(component.jobs.length).toBe(3);
    expect(component.total).toBe(3);
  });

  it('should render all status badges', () => {
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.badge-completed')).toBeTruthy();
    expect(el.querySelector('.badge-failed')).toBeTruthy();
    expect(el.querySelector('.badge-running')).toBeTruthy();
  });

  it('should show error banner on API failure', () => {
    apiSpy.getJobs.and.returnValue(throwError(() => new Error('timeout')));
    component.load();
    fixture.detectChanges();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.error-banner')).toBeTruthy();
  });

  it('should pass status filter to API', () => {
    component.statusFilter = 'failed';
    component.load();
    expect(apiSpy.getJobs).toHaveBeenCalledWith(0, 25, 'failed');
  });

  it('should reset to page 0 on filter change', () => {
    component.pageIndex    = 2;
    component.statusFilter = 'running';
    component.onFilterChange();
    expect(component.pageIndex).toBe(0);
    expect(apiSpy.getJobs).toHaveBeenCalledWith(0, 25, 'running');
  });

  it('should pass undefined status when filter is cleared', () => {
    component.statusFilter = '';
    component.load();
    expect(apiSpy.getJobs).toHaveBeenCalledWith(0, 25, undefined);
  });

  it('should show empty-state when no jobs and no error', () => {
    apiSpy.getJobs.and.returnValue(of({ jobs: [], total: 0, page: 0, size: 25 }));
    component.load();
    fixture.detectChanges();
    const el: HTMLElement = fixture.nativeElement;
    expect(el.querySelector('.empty-state')).toBeTruthy();
  });

  it('onPage should update pageIndex/pageSize and reload', () => {
    apiSpy.getJobs.calls.reset();
    component.onPage({ pageIndex: 3, pageSize: 50, length: 200 });
    expect(component.pageIndex).toBe(3);
    expect(component.pageSize).toBe(50);
    expect(apiSpy.getJobs).toHaveBeenCalledWith(3, 50, undefined);
  });

  it('statuses list should include all expected values', () => {
    expect(component.statuses).toContain('pending');
    expect(component.statuses).toContain('running');
    expect(component.statuses).toContain('completed');
    expect(component.statuses).toContain('failed');
    expect(component.statuses).toContain('timeout');
    expect(component.statuses).toContain('lost');
  });
});
