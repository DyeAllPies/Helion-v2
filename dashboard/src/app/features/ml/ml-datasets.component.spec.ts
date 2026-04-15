// src/app/features/ml/ml-datasets.component.spec.ts
//
// Unit tests for the Datasets view. Covers the init → list flow,
// pagination, register-modal happy-path, delete happy-path, and
// error-surface behaviour.

import { ComponentFixture, TestBed, fakeAsync, tick } from '@angular/core/testing';
import { provideAnimations } from '@angular/platform-browser/animations';
import { Observable, of, throwError } from 'rxjs';
import { MatDialog, MatDialogRef } from '@angular/material/dialog';

import { MlDatasetsComponent } from './ml-datasets.component';
import { ApiService } from '../../core/services/api.service';
import { Dataset, DatasetListResponse, DatasetRegisterRequest } from '../../shared/models';


function makeDataset(name: string, version: string): Dataset {
  return {
    name, version,
    uri: `s3://b/${name}/${version}`,
    size_bytes: 1024,
    created_at: '2026-04-14T10:00:00Z',
    created_by: 'tester',
  };
}

function emptyList(): DatasetListResponse {
  return { datasets: [], total: 0, page: 1, size: 25 };
}

describe('MlDatasetsComponent', () => {
  let fixture: ComponentFixture<MlDatasetsComponent>;
  let component: MlDatasetsComponent;
  let apiSpy: jasmine.SpyObj<ApiService>;
  let nextAfterClosed: Observable<DatasetRegisterRequest | undefined> = of(undefined);

  beforeEach(async () => {
    apiSpy = jasmine.createSpyObj<ApiService>('ApiService', [
      'getDatasets', 'registerDataset', 'deleteDataset',
    ]);
    apiSpy.getDatasets.and.returnValue(of(emptyList()));
    apiSpy.registerDataset.and.returnValue(of(makeDataset('d', 'v1')));
    apiSpy.deleteDataset.and.returnValue(of(void 0));

    nextAfterClosed = of(undefined);
    const dialogStub = {
      open: () => ({
        afterClosed: () => nextAfterClosed,
      } as MatDialogRef<unknown, DatasetRegisterRequest | undefined>),
    };

    await TestBed.configureTestingModule({
      imports: [MlDatasetsComponent],
      providers: [
        provideAnimations(),
        { provide: ApiService, useValue: apiSpy },
        // MatDialog is a root-level service; this provider override
        // replaces it for the standalone component's tests so we
        // don't have to wire MatDialog's overlay container.
        { provide: MatDialog, useValue: dialogStub },
      ],
    }).overrideComponent(MlDatasetsComponent, {
      // Override the dialog provider at the component level so the
      // component's own injector resolves the same stub. Standalone
      // components import MatDialogModule which provides MatDialog
      // at the component level, shadowing the TestBed root override.
      add: { providers: [{ provide: MatDialog, useValue: dialogStub }] },
    }).compileComponents();

    fixture = TestBed.createComponent(MlDatasetsComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('creates', () => expect(component).toBeTruthy());

  it('loads datasets on init (page 0, default size 25)', () => {
    expect(apiSpy.getDatasets).toHaveBeenCalledWith(0, 25);
    expect(component.loading).toBeFalse();
    expect(component.datasets.length).toBe(0);
  });

  it('renders loaded datasets', () => {
    apiSpy.getDatasets.and.returnValue(of({
      datasets: [makeDataset('iris', 'v1'), makeDataset('iris', 'v2')],
      total: 2, page: 1, size: 25,
    }));
    component.reload();
    expect(component.datasets.length).toBe(2);
    expect(component.total).toBe(2);
  });

  it('surfaces API errors', () => {
    apiSpy.getDatasets.and.returnValue(throwError(() => ({
      error: { error: 'registry rate limit exceeded' },
    })));
    component.reload();
    expect(component.error).toBe('registry rate limit exceeded');
    expect(component.loading).toBeFalse();
  });

  it('paginates via onPage', () => {
    component.onPage({ pageIndex: 2, pageSize: 50, length: 200 });
    expect(apiSpy.getDatasets).toHaveBeenCalledWith(2, 50);
    expect(component.page).toBe(2);
    expect(component.pageSize).toBe(50);
  });

  it('openRegister calls register and reloads on success', fakeAsync(() => {
    nextAfterClosed = of({ name: 'd', version: 'v1', uri: 's3://b/d/v1' });

    apiSpy.getDatasets.calls.reset();
    apiSpy.getDatasets.and.returnValue(of(emptyList()));

    component.openRegister();
    tick();

    expect(apiSpy.registerDataset).toHaveBeenCalled();
    // Register → reload from page 0.
    expect(apiSpy.getDatasets).toHaveBeenCalledWith(0, 25);
    expect(component.page).toBe(0);
  }));

  it('cancelling the register modal does not call register', fakeAsync(() => {
    nextAfterClosed = of(undefined);
    component.openRegister();
    tick();
    expect(apiSpy.registerDataset).not.toHaveBeenCalled();
  }));

  it('onDelete only deletes when confirm() returns true', () => {
    spyOn(window, 'confirm').and.returnValue(false);
    component.onDelete(makeDataset('d', 'v1'));
    expect(apiSpy.deleteDataset).not.toHaveBeenCalled();

    (window.confirm as jasmine.Spy).and.returnValue(true);
    component.onDelete(makeDataset('d', 'v1'));
    expect(apiSpy.deleteDataset).toHaveBeenCalledWith('d', 'v1');
  });

  it('formatBytes handles every size tier', () => {
    expect(component.formatBytes(undefined)).toBe('—');
    expect(component.formatBytes(0)).toBe('—');
    expect(component.formatBytes(512)).toBe('512 B');
    expect(component.formatBytes(2048)).toBe('2.0 KiB');
    expect(component.formatBytes(5 * 1024 * 1024)).toBe('5.0 MiB');
    expect(component.formatBytes(2 * 1024 * 1024 * 1024)).toBe('2.00 GiB');
  });
});
