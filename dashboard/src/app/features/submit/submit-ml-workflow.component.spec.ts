// src/app/features/submit/submit-ml-workflow.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter, Router } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of } from 'rxjs';

import { SubmitMlWorkflowComponent } from './submit-ml-workflow.component';
import { ApiService } from '../../core/services/api.service';
import { Workflow } from '../../shared/models';
import { MNIST_TEMPLATE, TEMPLATE_CARDS } from './ml-templates';

describe('SubmitMlWorkflowComponent', () => {
  let fixture:   ComponentFixture<SubmitMlWorkflowComponent>;
  let component: SubmitMlWorkflowComponent;
  let apiSpy:    jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', ['submitWorkflow']);
    apiSpy.submitWorkflow.and.returnValue(of({
      id: 'mnist-wf-1', name: 'x', status: 'pending', jobs: [], created_at: '',
    } as Workflow));

    await TestBed.configureTestingModule({
      imports: [SubmitMlWorkflowComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
        { provide: Router, useValue: jasmine.createSpyObj<Router>('Router', ['navigate']) },
      ],
    }).compileComponents();
    fixture   = TestBed.createComponent(SubmitMlWorkflowComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('renders one card per template', () => {
    const cards = fixture.nativeElement.querySelectorAll('button.card');
    expect(cards.length).toBe(TEMPLATE_CARDS.length);
  });

  it('picking the MNIST template fills the textarea with its JSON', () => {
    const mnistCard = TEMPLATE_CARDS.find(c => c.key === 'mnist')!;
    component.pickTemplate(mnistCard);

    const text = component.form.value.text as string;
    expect(text.length).toBeGreaterThan(0);

    // Parse the filled textarea and assert MNIST-specific features
    // made it through — id + the train step's runtime=rust
    // node_selector. This is the regression guard the spec calls
    // out in the ML tab section.
    const parsed = JSON.parse(text);
    expect(parsed.id).toBe('mnist-wf-1');
    const train = parsed.jobs.find((j: { name: string }) => j.name === 'train');
    expect(train.node_selector).toEqual({ runtime: 'rust' });
  });

  it('picking "Custom" clears the textarea', () => {
    // Arrange: put something in the textarea first.
    const mnist = TEMPLATE_CARDS.find(c => c.key === 'mnist')!;
    component.pickTemplate(mnist);
    expect((component.form.value.text as string).length).toBeGreaterThan(0);

    // Act: click Custom.
    const custom = TEMPLATE_CARDS.find(c => c.key === 'custom')!;
    component.pickTemplate(custom);

    // Assert: textarea now empty, activeKey tracks the choice.
    expect(component.form.value.text).toBe('');
    expect(component.activeKey).toBe('custom');
  });

  it('active card is highlighted via the card--active class', () => {
    const iris = TEMPLATE_CARDS.find(c => c.key === 'iris')!;
    component.pickTemplate(iris);
    fixture.detectChanges();
    const activeCards = fixture.nativeElement.querySelectorAll('.card--active');
    expect(activeCards.length).toBe(1);
    expect(activeCards[0].textContent).toContain(iris.title);
  });

  it('end-to-end: pick MNIST → Validate → Preview → Submit hits the API', () => {
    const mnist = TEMPLATE_CARDS.find(c => c.key === 'mnist')!;
    component.pickTemplate(mnist);
    component.onValidate();
    expect(component.validationOk).toBeTrue();

    component.openPreview();
    component.onPreviewResolved('confirm');

    expect(apiSpy.submitWorkflow).toHaveBeenCalledTimes(1);
    // The posted body is the parsed template — assert the
    // train-step pinning travels through unchanged (feature 21
    // regression guard in the submit path, not just the template).
    const body = apiSpy.submitWorkflow.calls.mostRecent().args[0];
    const train = body.jobs.find(j => j.name === 'train');
    expect(train?.node_selector).toEqual({ runtime: 'rust' });
    expect(body.id).toBe(MNIST_TEMPLATE.id);
  });
});
