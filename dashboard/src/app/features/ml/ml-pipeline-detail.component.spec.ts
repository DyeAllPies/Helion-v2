// src/app/features/ml/ml-pipeline-detail.component.spec.ts
//
// The mermaid render path is async + DOM-heavy and is not unit-
// tested here (the test would only verify we invoke mermaid.render
// with the right spec — `buildMermaidSpec` is the exported,
// testable piece). Coverage of the visual render is delegated to
// the Playwright e2e suite when the iris demo (step 19) ships.

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { ActivatedRoute, convertToParamMap, provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';

import {
  MlPipelineDetailComponent, buildMermaidSpec,
} from './ml-pipeline-detail.component';
import { ApiService } from '../../core/services/api.service';
import { WorkflowLineage } from '../../shared/models';

function makeLineage(over: Partial<WorkflowLineage> = {}): WorkflowLineage {
  return {
    workflow_id: 'wf-1',
    name: 'train-serve',
    status: 'running',
    jobs: [
      {
        name: 'train', job_id: 'wf-1/train', status: 'completed',
        outputs: [{ name: 'MODEL', uri: 's3://b/m.pt', size: 1024 }],
        models_produced: [{ name: 'resnet', version: 'v1' }],
      },
      {
        name: 'serve', job_id: 'wf-1/serve', status: 'running',
        depends_on: ['train'],
      },
    ],
    artifact_edges: [
      { from_job: 'train', from_output: 'MODEL', to_job: 'serve', to_input: 'CHECKPOINT' },
    ],
    ...over,
  };
}

describe('MlPipelineDetailComponent', () => {
  let fixture: ComponentFixture<MlPipelineDetailComponent>;
  let component: MlPipelineDetailComponent;
  let apiSpy: jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', ['getWorkflowLineage']);
    apiSpy.getWorkflowLineage.and.returnValue(of(makeLineage()));

    await TestBed.configureTestingModule({
      imports: [MlPipelineDetailComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
        {
          provide: ActivatedRoute,
          useValue: { snapshot: { paramMap: convertToParamMap({ id: 'wf-1' }) } },
        },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(MlPipelineDetailComponent);
    component = fixture.componentInstance;
  });

  afterEach(() => fixture.destroy());

  it('creates and reads workflow id from the route', () => {
    fixture.detectChanges();
    expect(component.workflowId).toBe('wf-1');
  });

  it('loads lineage on init', () => {
    fixture.detectChanges();
    expect(apiSpy.getWorkflowLineage).toHaveBeenCalledWith('wf-1');
    expect(component.lineage?.workflow_id).toBe('wf-1');
    expect(component.loading).toBeFalse();
  });

  it('surfaces API errors', () => {
    apiSpy.getWorkflowLineage.and.returnValue(throwError(() => ({
      error: { error: 'workflow not found' },
    })));
    fixture.detectChanges();
    expect(component.error).toBe('workflow not found');
    expect(component.lineage).toBeNull();
  });

  it('surfaces an empty-id error without hitting the API', () => {
    TestBed.resetTestingModule();
    TestBed.configureTestingModule({
      imports: [MlPipelineDetailComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
        {
          provide: ActivatedRoute,
          useValue: { snapshot: { paramMap: convertToParamMap({}) } },
        },
      ],
    });
    apiSpy.getWorkflowLineage.calls.reset();
    const f2 = TestBed.createComponent(MlPipelineDetailComponent);
    f2.detectChanges();
    expect(f2.componentInstance.error).toBe('missing workflow id');
    expect(apiSpy.getWorkflowLineage).not.toHaveBeenCalled();
    f2.destroy();
  });

  it('formatBytes handles every tier', () => {
    fixture.detectChanges();
    expect(component.formatBytes(undefined)).toBe('—');
    expect(component.formatBytes(0)).toBe('—');
    expect(component.formatBytes(500)).toBe('500 B');
    expect(component.formatBytes(2048)).toBe('2.0 KiB');
    expect(component.formatBytes(3 * 1024 * 1024)).toBe('3.0 MiB');
  });

  it('statusChipClass pins every known status to a distinct class', () => {
    fixture.detectChanges();
    // Terminal states.
    expect(component.statusChipClass('running')).toContain('chip-running');
    expect(component.statusChipClass('completed')).toContain('chip-completed');
    expect(component.statusChipClass('failed')).toContain('chip-failed');
    expect(component.statusChipClass('cancelled')).toContain('chip-failed');
    expect(component.statusChipClass('timeout')).toContain('chip-failed');
    expect(component.statusChipClass('lost')).toContain('chip-failed');
    // Transitional states each get their own class so the DAG view
    // colour-codes progress (pending → scheduled → dispatching →
    // running). Regression guard against the previous behaviour
    // where all three transitional states shared chip-pending grey.
    expect(component.statusChipClass('pending')).toContain('chip-pending');
    expect(component.statusChipClass('scheduled')).toContain('chip-scheduled');
    expect(component.statusChipClass('dispatching')).toContain('chip-dispatching');
    // Unknown falls through to the neutral default.
    expect(component.statusChipClass('unknown')).toContain('chip-default');
  });
});

describe('buildMermaidSpec', () => {
  it('emits a flowchart header', () => {
    const spec = buildMermaidSpec(makeLineage());
    expect(spec.split('\n')[0]).toBe('flowchart LR');
  });

  it('declares a node per job with its status rendered inline', () => {
    const spec = buildMermaidSpec(makeLineage());
    expect(spec).toContain('train[');
    expect(spec).toContain('serve[');
    expect(spec).toContain('completed');
    expect(spec).toContain('running');
  });

  it('emits solid arrows for depends_on', () => {
    const spec = buildMermaidSpec(makeLineage());
    expect(spec).toContain('train --> serve');
  });

  it('emits dashed arrows with labels for artifact edges', () => {
    const spec = buildMermaidSpec(makeLineage());
    expect(spec).toContain('train -. "MODEL → CHECKPOINT" .-> serve');
  });

  it('sanitizes job names with non-identifier characters', () => {
    const lineage = makeLineage({
      jobs: [
        { name: 'wf-1/train', status: 'completed', depends_on: [] },
        { name: 'wf-1/serve', status: 'running', depends_on: ['wf-1/train'] },
      ],
      artifact_edges: [],
    });
    const spec = buildMermaidSpec(lineage);
    // Mermaid identifiers cannot contain `/` or `-`; they become `_`.
    expect(spec).toContain('wf_1_train');
    expect(spec).toContain('wf_1_serve');
    expect(spec).toContain('wf_1_train --> wf_1_serve');
  });

  it('handles a workflow with no artifact edges', () => {
    const spec = buildMermaidSpec(makeLineage({ artifact_edges: [] }));
    expect(spec).not.toContain('-.');
  });

  it('handles jobs with no depends_on field', () => {
    const spec = buildMermaidSpec(makeLineage({
      jobs: [{ name: 'solo', status: 'completed' }],
      artifact_edges: [],
    }));
    // Only the node line plus the flowchart header.
    expect(spec.split('\n').length).toBe(2);
  });
});
