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

  it('defines exactly three tabs in the spec-mandated order', () => {
    // The feature 22 spec pins three tabs (Job / Workflow / ML
    // Workflow) in that order. Guards against a tab accidentally
    // being removed or reordered — the sidebar + URL deep-links
    // rely on these paths being stable.
    expect(component.tabs.length).toBe(3);
    expect(component.tabs.map(t => t.path)).toEqual(['job', 'workflow', 'ml-workflow']);
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
    const anchors = fixture.nativeElement.querySelectorAll('a.submit-tab');
    expect(anchors.length).toBe(3);
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
