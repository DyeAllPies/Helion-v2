// src/app/features/ml/ml-pipelines.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';

import { MlPipelinesComponent } from './ml-pipelines.component';
import { ApiService } from '../../core/services/api.service';
import { Workflow, WorkflowsPage, WorkflowStatus } from '../../shared/models';

function makeWorkflow(over: Partial<Workflow> = {}): Workflow {
  return {
    id: 'wf-1',
    name: 'train-serve',
    status: 'running' as WorkflowStatus,
    jobs: [],
    created_at: '2026-04-14T12:00:00Z',
    ...over,
  };
}

function page(items: Workflow[]): WorkflowsPage {
  return { workflows: items, total: items.length, page: 1, size: 20 };
}

describe('MlPipelinesComponent', () => {
  let fixture: ComponentFixture<MlPipelinesComponent>;
  let component: MlPipelinesComponent;
  let apiSpy: jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', ['getWorkflows']);
    apiSpy.getWorkflows.and.returnValue(of(page([])));

    await TestBed.configureTestingModule({
      imports: [MlPipelinesComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(MlPipelinesComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('creates', () => expect(component).toBeTruthy());

  it('loads workflows on init (page 0, default size 20)', () => {
    expect(apiSpy.getWorkflows).toHaveBeenCalledWith(0, 20);
  });

  it('renders loaded workflows', () => {
    apiSpy.getWorkflows.and.returnValue(of(page([makeWorkflow(), makeWorkflow({ id: 'wf-2' })])));
    component.reload();
    expect(component.workflows.length).toBe(2);
  });

  it('surfaces API errors', () => {
    apiSpy.getWorkflows.and.returnValue(throwError(() => ({ error: { error: 'boom' } })));
    component.reload();
    expect(component.error).toBe('boom');
    expect(component.loading).toBeFalse();
  });

  it('paginates via onPage', () => {
    component.onPage({ pageIndex: 2, pageSize: 50, length: 100 });
    expect(apiSpy.getWorkflows).toHaveBeenCalledWith(2, 50);
  });

  it('countCompleted counts only completed + skipped jobs', () => {
    // Mixed workflow: two done (one completed, one skipped via
    // on_failure branch), one running, one pending. "Jobs" column
    // in the UI should render 2/4 for this shape.
    const mk = (name: string, s: string): Workflow['jobs'][number] => ({
      name, command: '', condition: 'on_success', job_status: s,
    });
    expect(component.countCompleted([
      mk('a', 'completed'), mk('b', 'running'),
      mk('c', 'skipped'),   mk('d', 'pending'),
    ])).toBe(2);

    // Empty/undefined inputs must not crash — the list row renders
    // `countCompleted / length` on every tick, and getWorkflows can
    // return a workflow with no jobs yet if it was just submitted.
    expect(component.countCompleted(undefined)).toBe(0);
    expect(component.countCompleted([])).toBe(0);
  });

  it('statusChipClass maps statuses to distinct colour classes', () => {
    expect(component.statusChipClass('running')).toContain('chip-running');
    expect(component.statusChipClass('completed')).toContain('chip-completed');
    expect(component.statusChipClass('failed')).toContain('chip-failed');
    expect(component.statusChipClass('cancelled')).toContain('chip-failed');
    expect(component.statusChipClass('timeout')).toContain('chip-failed');
    expect(component.statusChipClass('lost')).toContain('chip-failed');
    // Each transitional state gets its own class so the list row
    // doesn't collapse pending/scheduled/dispatching into one
    // neutral chip (feature 21 walkthrough needed the distinction).
    expect(component.statusChipClass('pending')).toContain('chip-pending');
    expect(component.statusChipClass('scheduled')).toContain('chip-scheduled');
    expect(component.statusChipClass('dispatching')).toContain('chip-dispatching');
    expect(component.statusChipClass('whatever')).toContain('chip-default');
  });
});
