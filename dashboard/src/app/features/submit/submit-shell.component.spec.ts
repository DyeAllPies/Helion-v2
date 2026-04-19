// src/app/features/submit/submit-shell.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';

import { SubmitShellComponent } from './submit-shell.component';

describe('SubmitShellComponent', () => {
  let fixture:   ComponentFixture<SubmitShellComponent>;
  let component: SubmitShellComponent;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [SubmitShellComponent],
      providers: [provideRouter([]), provideAnimations()],
    }).compileComponents();
    fixture   = TestBed.createComponent(SubmitShellComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

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
});
