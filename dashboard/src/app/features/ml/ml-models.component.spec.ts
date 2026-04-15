// src/app/features/ml/ml-models.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideRouter } from '@angular/router';
import { provideAnimations } from '@angular/platform-browser/animations';
import { of, throwError } from 'rxjs';

import { MlModelsComponent } from './ml-models.component';
import { ApiService } from '../../core/services/api.service';
import { MLModel, ModelListResponse } from '../../shared/models';

function makeModel(over: Partial<MLModel> = {}): MLModel {
  return {
    name: 'resnet',
    version: 'v1',
    uri: 's3://b/resnet/v1',
    framework: 'pytorch',
    source_job_id: 'train-1',
    source_dataset: { name: 'imagenet', version: 'v2' },
    metrics: { top1: 0.76, top5: 0.93 },
    size_bytes: 1024 * 1024,
    created_at: '2026-04-14T10:00:00Z',
    created_by: 'tester',
    ...over,
  };
}

function list(models: MLModel[]): ModelListResponse {
  return { models, total: models.length, page: 1, size: 25 };
}

describe('MlModelsComponent', () => {
  let fixture: ComponentFixture<MlModelsComponent>;
  let component: MlModelsComponent;
  let apiSpy: jasmine.SpyObj<ApiService>;

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', [
      'getModels', 'deleteModel',
    ]);
    apiSpy.getModels.and.returnValue(of(list([])));
    apiSpy.deleteModel.and.returnValue(of(void 0));

    await TestBed.configureTestingModule({
      imports: [MlModelsComponent],
      providers: [
        provideRouter([]),
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(MlModelsComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('creates', () => expect(component).toBeTruthy());

  it('loads models on init', () => {
    expect(apiSpy.getModels).toHaveBeenCalledWith(0, 25);
    expect(component.models.length).toBe(0);
  });

  it('renders loaded models', () => {
    apiSpy.getModels.and.returnValue(of(list([makeModel(), makeModel({ version: 'v2' })])));
    component.reload();
    expect(component.models.length).toBe(2);
  });

  it('surfaces API errors', () => {
    apiSpy.getModels.and.returnValue(throwError(() => ({
      error: { error: 'registry not configured' },
    })));
    component.reload();
    expect(component.error).toBe('registry not configured');
    expect(component.loading).toBeFalse();
  });

  it('hasMetrics false when metrics are absent or empty', () => {
    expect(component.hasMetrics(makeModel({ metrics: undefined }))).toBeFalse();
    expect(component.hasMetrics(makeModel({ metrics: {} }))).toBeFalse();
    expect(component.hasMetrics(makeModel())).toBeTrue();
  });

  it('metricsList sorts by key', () => {
    const got = component.metricsList(makeModel({
      metrics: { top5: 0.93, accuracy: 0.82, top1: 0.76 },
    }));
    expect(got.map(e => e.key)).toEqual(['accuracy', 'top1', 'top5']);
  });

  it('formatMetric renders ints without decimals, floats at 3 places', () => {
    expect(component.formatMetric(5)).toBe('5');
    expect(component.formatMetric(0.7642)).toBe('0.764');
  });

  it('onDelete respects confirm()', () => {
    spyOn(window, 'confirm').and.returnValue(false);
    component.onDelete(makeModel());
    expect(apiSpy.deleteModel).not.toHaveBeenCalled();

    (window.confirm as jasmine.Spy).and.returnValue(true);
    component.onDelete(makeModel());
    expect(apiSpy.deleteModel).toHaveBeenCalledWith('resnet', 'v1');
  });

  it('paginates via onPage', () => {
    component.onPage({ pageIndex: 1, pageSize: 50, length: 100 });
    expect(apiSpy.getModels).toHaveBeenCalledWith(1, 50);
  });
});
