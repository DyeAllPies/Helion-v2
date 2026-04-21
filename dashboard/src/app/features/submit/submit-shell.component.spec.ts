// src/app/features/submit/submit-shell.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { HttpErrorResponse } from '@angular/common/http';
import { of, throwError } from 'rxjs';

import { SubmitShellComponent } from './submit-shell.component';
import { ApiService } from '../../core/services/api.service';
import { WorkflowDraftService } from '../../core/services/workflow-draft.service';
import { SubmitWorkflowRequest, Workflow } from '../../shared/models';

const draftBody: SubmitWorkflowRequest = {
  id: 'wf-mnist-parallel',
  name: 'mnist parallel',
  jobs: [
    { name: 'ingest',     command: 'python' },
    { name: 'preprocess', command: 'python', depends_on: ['ingest'] },
    { name: 'train',      command: 'python', depends_on: ['preprocess'] },
  ],
};

describe('SubmitShellComponent', () => {
  let fixture:   ComponentFixture<SubmitShellComponent>;
  let component: SubmitShellComponent;

  beforeEach(async () => {
    sessionStorage.clear();
    await TestBed.configureTestingModule({
      imports: [SubmitShellComponent],
      providers: [provideRouter([]), provideAnimations()],
    }).compileComponents();
    fixture   = TestBed.createComponent(SubmitShellComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => {
    fixture.destroy();
    sessionStorage.clear();
  });

  it('creates', () => {
    expect(component).toBeTruthy();
  });

  it('defines four tabs in the expected order', () => {
    // Job / Workflow / ML Workflow were the original three tabs;
    // DAG Builder was promoted out of the spec's "Deferred" list
    // at user request. Order is load-bearing — the sidebar hover
    // order + URL deep-links rely on these paths being stable.
    expect(component.tabs.length).toBe(4);
    expect(component.tabs.map(t => t.path)).toEqual([
      'job', 'workflow', 'ml-workflow', 'dag-builder',
    ]);
  });

  it('each tab carries a non-empty label, icon and hint', () => {
    // Tooltip text (hint) is important for accessibility AND as a
    // plain-language reminder of which route the tab lives on —
    // missing hints degrade the operator experience.
    for (const tab of component.tabs) {
      expect(tab.label.length).toBeGreaterThan(0);
      expect(tab.icon.length).toBeGreaterThan(0);
      expect(tab.hint.length).toBeGreaterThan(0);
    }
  });

  it('renders one anchor per tab', () => {
    // Expected count mirrors the tabs array above. If a tab is
    // added or removed, update BOTH the tabs.length assertion and
    // this DOM count so the two stay in lockstep.
    const anchors = fixture.nativeElement.querySelectorAll('a.submit-tab');
    expect(anchors.length).toBe(component.tabs.length);
  });

  it('renders the router outlet for the active child route', () => {
    // The shell hosts the <router-outlet>; the child routes
    // resolve to the placeholder (or later, real forms). The
    // presence of the outlet is what makes the shell "a shell"
    // rather than a static page — guard it.
    const outlet = fixture.nativeElement.querySelector('router-outlet');
    expect(outlet).not.toBeNull();
  });

  it('renders the page title and subtitle', () => {
    const title = fixture.nativeElement.querySelector('.page-title');
    const sub   = fixture.nativeElement.querySelector('.page-sub');
    expect(title?.textContent?.trim()).toBe('SUBMIT');
    expect(sub?.textContent?.toLowerCase()).toContain('start a new run');
  });

  // ── Feature 41 — Resume draft card ────────────────────────────

  it('does not render the Resume-draft card when sessionStorage is empty', () => {
    const card = fixture.nativeElement.querySelector('[data-testid="resume-draft-card"]');
    expect(card).toBeNull();
  });
});

describe('SubmitShellComponent — Resume draft card', () => {
  let fixture:   ComponentFixture<SubmitShellComponent>;
  let component: SubmitShellComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;
  let navigateSpy: jasmine.Spy;

  async function setupFixture(url = '/submit/job') {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', ['submitWorkflow']);
    await TestBed.configureTestingModule({
      imports: [SubmitShellComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();

    const router = TestBed.inject(Router);
    // Lock router.url so the shell's `startsWith('/submit/dag-builder')`
    // check is deterministic in each spec.
    spyOnProperty(router, 'url', 'get').and.returnValue(url);
    navigateSpy = spyOn(router, 'navigate').and.returnValue(Promise.resolve(true));

    fixture = TestBed.createComponent(SubmitShellComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  }

  beforeEach(() => {
    sessionStorage.clear();
    // Seed a draft before component creation so snapshot$'s initial
    // emission drives the card visible.
    sessionStorage.setItem(WorkflowDraftService.STORAGE_KEY, JSON.stringify({
      schema: 'v1',
      savedAt: new Date().toISOString(),
      body: draftBody,
    }));
  });

  afterEach(() => {
    fixture.destroy();
    sessionStorage.clear();
  });

  it('renders the card when a draft is saved', async () => {
    await setupFixture();
    expect(component.draft).not.toBeNull();
    const card = fixture.nativeElement.querySelector('[data-testid="resume-draft-card"]');
    expect(card).not.toBeNull();
  });

  it('shows the draft workflow name and job count in the card', async () => {
    await setupFixture();
    const card = fixture.nativeElement.querySelector('[data-testid="resume-draft-card"]');
    const text = card?.textContent ?? '';
    expect(text).toContain('mnist parallel');  // draft body name
    expect(text).toContain('3 jobs');          // draft body jobs length
  });

  it('hides the card when active route is /submit/dag-builder', async () => {
    await setupFixture('/submit/dag-builder');
    expect(component.hideCard).toBeTrue();
    const card = fixture.nativeElement.querySelector('[data-testid="resume-draft-card"]');
    expect(card).toBeNull();
  });

  it('onSubmitDraft posts the draft body and clears + navigates on success', async () => {
    await setupFixture();
    apiSpy.submitWorkflow.and.returnValue(of({
      id: draftBody.id, name: draftBody.name, status: 'pending',
      jobs: [], created_at: '',
    } as Workflow));

    component.onSubmitDraft();

    expect(apiSpy.submitWorkflow).toHaveBeenCalledOnceWith(draftBody);
    expect(navigateSpy).toHaveBeenCalledWith(['/ml/pipelines', draftBody.id]);
    // Draft cleared — next snapshot$ emission drops the card.
    expect(TestBed.inject(WorkflowDraftService).load()).toBeNull();
  });

  it('onSubmitDraft surfaces server errors and keeps the draft', async () => {
    await setupFixture();
    apiSpy.submitWorkflow.and.returnValue(throwError(() => new HttpErrorResponse({
      error: { error: 'coordinator rejected' }, status: 400,
    })));

    component.onSubmitDraft();

    expect(component.submitError).toBe('coordinator rejected');
    expect(navigateSpy).not.toHaveBeenCalled();
    expect(TestBed.inject(WorkflowDraftService).load()).not.toBeNull();
  });

  it('onDiscard clears the draft without posting', async () => {
    await setupFixture();
    component.onDiscard();
    expect(apiSpy.submitWorkflow).not.toHaveBeenCalled();
    expect(TestBed.inject(WorkflowDraftService).load()).toBeNull();
    fixture.detectChanges();
    const card = fixture.nativeElement.querySelector('[data-testid="resume-draft-card"]');
    expect(card).toBeNull();
  });
});
