// src/app/features/ml/register-dataset-dialog.component.spec.ts

import { ComponentFixture, TestBed } from '@angular/core/testing';
import { provideAnimations } from '@angular/platform-browser/animations';
import { MatDialogRef } from '@angular/material/dialog';

import {
  RegisterDatasetDialogComponent, parseTags,
} from './register-dataset-dialog.component';
import { DatasetRegisterRequest } from '../../shared/models';

describe('RegisterDatasetDialogComponent', () => {
  let fixture: ComponentFixture<RegisterDatasetDialogComponent>;
  let component: RegisterDatasetDialogComponent;
  let dialogRef: jasmine.SpyObj<MatDialogRef<unknown, DatasetRegisterRequest | undefined>>;

  beforeEach(async () => {
    dialogRef = jasmine.createSpyObj<MatDialogRef<unknown, DatasetRegisterRequest | undefined>>('MatDialogRef', ['close']);

    await TestBed.configureTestingModule({
      imports: [RegisterDatasetDialogComponent],
      providers: [
        provideAnimations(),
        { provide: MatDialogRef, useValue: dialogRef },
      ],
    }).compileComponents();

    fixture = TestBed.createComponent(RegisterDatasetDialogComponent);
    component = fixture.componentInstance;
    fixture.detectChanges();
  });

  afterEach(() => fixture.destroy());

  it('creates', () => expect(component).toBeTruthy());

  it('canSubmit gates on required fields and URI scheme', () => {
    expect(component.canSubmit()).toBeFalse();
    component.name = 'iris'; component.version = 'v1';
    expect(component.canSubmit()).toBeFalse();
    component.uri = 'http://evil/x';
    expect(component.canSubmit()).toBeFalse();   // wrong scheme
    component.uri = 's3://b/iris/v1';
    expect(component.canSubmit()).toBeTrue();
    component.uri = 'file:///tmp/iris/v1';
    expect(component.canSubmit()).toBeTrue();
  });

  it('submit closes with a built request including optional fields', () => {
    component.name = 'iris';
    component.version = 'v1';
    component.uri = 's3://b/iris/v1';
    component.sizeBytes = 4096;
    component.sha256 = 'a'.repeat(64);
    component.submit();

    expect(dialogRef.close).toHaveBeenCalled();
    const req = dialogRef.close.calls.mostRecent().args[0] as DatasetRegisterRequest;
    expect(req.name).toBe('iris');
    expect(req.version).toBe('v1');
    expect(req.uri).toBe('s3://b/iris/v1');
    expect(req.size_bytes).toBe(4096);
    expect(req.sha256).toBe('a'.repeat(64));
    expect(req.tags).toBeUndefined();
  });

  it('submit serialises tags input into a tag map', () => {
    component.name = 'iris'; component.version = 'v1'; component.uri = 's3://b/k';
    component.tagsRaw = 'team:ml, env:prod, owner:dennis';
    component.submit();
    const req = dialogRef.close.calls.mostRecent().args[0] as DatasetRegisterRequest;
    expect(req.tags).toEqual({ team: 'ml', env: 'prod', owner: 'dennis' });
  });

  it('submit surfaces tag parse errors and does NOT close', () => {
    component.name = 'iris'; component.version = 'v1'; component.uri = 's3://b/k';
    component.tagsRaw = 'badtag-without-colon';
    component.submit();
    expect(component.tagError).toContain('badtag-without-colon');
    expect(dialogRef.close).not.toHaveBeenCalled();
  });

  it('submit no-ops when canSubmit is false', () => {
    component.submit();
    expect(dialogRef.close).not.toHaveBeenCalled();
  });

  it('cancel closes with undefined', () => {
    component.cancel();
    expect(dialogRef.close).toHaveBeenCalledWith(undefined);
  });
});

describe('parseTags', () => {
  it('returns undefined tags when input is whitespace', () => {
    expect(parseTags('   ').tags).toBeUndefined();
  });

  it('parses a single key:value', () => {
    expect(parseTags('team:ml').tags).toEqual({ team: 'ml' });
  });

  it('parses multiple comma-separated entries with surrounding spaces', () => {
    expect(parseTags(' team:ml , env:prod ').tags).toEqual({ team: 'ml', env: 'prod' });
  });

  it('skips empty entries from trailing or doubled commas', () => {
    expect(parseTags('a:b,,c:d,').tags).toEqual({ a: 'b', c: 'd' });
  });

  it('errors on entry missing the colon', () => {
    const out = parseTags('orphan');
    expect(out.error).toContain('orphan');
    expect(out.tags).toBeUndefined();
  });

  it('errors on entry with empty key or empty value', () => {
    expect(parseTags(':novalue').error).toBeDefined();
    expect(parseTags('nokey:').error).toBeDefined();
  });

  it('values may contain colons (only the first colon is the separator)', () => {
    expect(parseTags('url:https://example.org/path').tags)
      .toEqual({ url: 'https://example.org/path' });
  });
});
