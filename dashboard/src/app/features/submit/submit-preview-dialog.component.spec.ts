// src/app/features/submit/submit-preview-dialog.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideAnimations } from '@angular/platform-browser/animations';

import { SubmitPreviewDialogComponent, PreviewResult } from './submit-preview-dialog.component';

describe('SubmitPreviewDialogComponent', () => {
  let fixture: ComponentFixture<SubmitPreviewDialogComponent>;

  beforeEach(async () => {
    await TestBed.configureTestingModule({
      imports: [SubmitPreviewDialogComponent],
      providers: [provideAnimations()],
    }).compileComponents();
    fixture = TestBed.createComponent(SubmitPreviewDialogComponent);
    fixture.componentInstance.title = 'Submit job test';
    fixture.componentInstance.body  = { id: 'test', command: 'echo', args: ['hi'] };
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('renders the title + a preview of the body JSON', () => {
    const el = fixture.nativeElement as HTMLElement;
    expect(el.querySelector('.dialog__title')?.textContent?.trim())
      .toBe('Submit job test');
    const body = el.querySelector('.dialog__body')?.textContent ?? '';
    // JSON.stringify indent=2 → key lines start with two spaces
    expect(body).toContain('"id": "test"');
    expect(body).toContain('"command": "echo"');
  });

  it('emits "confirm" when the Submit button is clicked', () => {
    const results: PreviewResult[] = [];
    fixture.componentInstance.resolved.subscribe(r => results.push(r));

    // Grab the Submit button. The order is Cancel, Submit.
    const buttons = fixture.nativeElement.querySelectorAll('button.btn');
    (buttons[1] as HTMLButtonElement).click();
    expect(results).toEqual(['confirm']);
  });

  it('emits "dismiss" when the Cancel button is clicked', () => {
    const results: PreviewResult[] = [];
    fixture.componentInstance.resolved.subscribe(r => results.push(r));

    const buttons = fixture.nativeElement.querySelectorAll('button.btn');
    (buttons[0] as HTMLButtonElement).click();
    expect(results).toEqual(['dismiss']);
  });

  it('emits "dismiss" when the backdrop is clicked outside the dialog', () => {
    const results: PreviewResult[] = [];
    fixture.componentInstance.resolved.subscribe(r => results.push(r));

    const backdrop = fixture.nativeElement.querySelector('.backdrop') as HTMLElement;
    backdrop.click();
    expect(results).toEqual(['dismiss']);
  });

  it('does NOT emit when a click lands on the dialog itself', () => {
    // Regression: the dialog stops propagation on its own click
    // handler — otherwise a click on (say) the preview body would
    // bubble up to the backdrop handler and dismiss the modal.
    const results: PreviewResult[] = [];
    fixture.componentInstance.resolved.subscribe(r => results.push(r));

    const dialog = fixture.nativeElement.querySelector('.dialog') as HTMLElement;
    dialog.click();
    expect(results).toEqual([]);
  });

  it('emits "dismiss" on Escape keydown', () => {
    const results: PreviewResult[] = [];
    fixture.componentInstance.resolved.subscribe(r => results.push(r));

    const event = new KeyboardEvent('keydown', { key: 'Escape' });
    document.dispatchEvent(event);
    expect(results).toEqual(['dismiss']);
  });

  it('serialises a body containing a null gracefully', () => {
    fixture.componentInstance.body = { a: null, b: 'ok' };
    fixture.detectChanges();
    const body = fixture.nativeElement.querySelector('.dialog__body')?.textContent ?? '';
    expect(body).toContain('"a": null');
  });
});
