// src/app/features/submit/submit-placeholder.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { ActivatedRoute } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';

import { SubmitPlaceholderComponent } from './submit-placeholder.component';

function makeRoute(tab: string | undefined): Partial<ActivatedRoute> {
  // ActivatedRoute.snapshot has many fields; we only need `data`
  // here, so we cast through unknown rather than pulling in the
  // full ActivatedRouteSnapshot shape (no `any`, keeps lint clean).
  const snapshotStub = {
    data: tab !== undefined ? { tab } : {},
  };
  return { snapshot: snapshotStub as unknown as ActivatedRoute['snapshot'] };
}

function createWith(tab: string | undefined): ComponentFixture<SubmitPlaceholderComponent> {
  TestBed.resetTestingModule();
  TestBed.configureTestingModule({
    imports: [SubmitPlaceholderComponent],
    providers: [
      provideAnimations(),
      { provide: ActivatedRoute, useValue: makeRoute(tab) },
    ],
  });
  const fixture = TestBed.createComponent(SubmitPlaceholderComponent);
  fixture.detectChanges();
  return fixture;
}

describe('SubmitPlaceholderComponent', () => {
  afterEach(() => {
    // Each test builds its own TestBed via createWith; reset so
    // later describes don't inherit this one's providers.
    TestBed.resetTestingModule();
  });

  it('picks the job tab by default', () => {
    const f = createWith(undefined);
    expect(f.componentInstance.tabKey).toBe('job');
    expect(f.componentInstance.info.title).toBe('Single job');
  });

  it('routes data "tab=workflow" -> workflow info', () => {
    const f = createWith('workflow');
    expect(f.componentInstance.tabKey).toBe('workflow');
    expect(f.componentInstance.info.title).toBe('Workflow DAG');
  });

  it('routes data "tab=ml-workflow" -> ml info', () => {
    const f = createWith('ml-workflow');
    expect(f.componentInstance.tabKey).toBe('ml-workflow');
    expect(f.componentInstance.info.title).toBe('ML workflow');
  });

  it('ignores an unknown tab value and falls back to the job default', () => {
    // Belt-and-braces: a typo in routes data (e.g. "jobs" with
    // an s) must not crash. The placeholder keeps the default
    // instead of throwing.
    const f = createWith('bogus');
    expect(f.componentInstance.tabKey).toBe('job');
  });

  it('renders a "waiting on" blurb naming the blocking features', () => {
    // Readers of the dashboard should see *why* a tab is empty
    // without having to grep the code. Assert the blurb mentions
    // the specific prerequisite feature numbers.
    const f = createWith('job');
    const waiting = f.nativeElement.querySelector('.placeholder__waiting');
    expect(waiting?.textContent).toContain('feature 24');
    expect(waiting?.textContent).toContain('feature 25');
    expect(waiting?.textContent).toContain('feature 26');
  });

  it('has a link to the planned-features doc for context', () => {
    const f = createWith('job');
    const link = f.nativeElement.querySelector('.placeholder__hint a');
    expect(link?.getAttribute('href')).toContain('22-ui-submission-tab.md');
    expect(link?.getAttribute('rel')).toContain('noopener');
  });
});
