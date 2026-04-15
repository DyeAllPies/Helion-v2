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

  it('statusChipClass maps statuses to colour classes', () => {
    expect(component.statusChipClass('running')).toContain('chip-running');
    expect(component.statusChipClass('completed')).toContain('chip-completed');
    expect(component.statusChipClass('failed')).toContain('chip-failed');
    expect(component.statusChipClass('cancelled')).toContain('chip-failed');
    expect(component.statusChipClass('whatever')).toContain('chip-default');
  });
});
