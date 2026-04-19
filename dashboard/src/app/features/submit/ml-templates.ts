// src/app/features/submit/ml-templates.ts
//
// Feature 22 step 4 — inline ML workflow templates.
//
// Mirrors the two shipped pipelines:
//   - examples/ml-iris/workflow.yaml
//   - examples/ml-mnist/workflow.yaml
//
// Templates are hard-coded as TypeScript constants so the
// dashboard bundle does not need a YAML parser on day one. When
// the YAML-editor upgrade lands (feature 22's open question),
// this file will be replaced by a runtime fetch + parse of the
// actual YAML files. Until then, keep the JSON and the YAML in
// sync — any change to examples/ml-*/workflow.yaml should be
// reflected here. A unit test guards the mnist template's
// runtime=rust selector on the train step (feature 21) so a
// drift silently disabling heterogeneous scheduling fails CI.

import { SubmitWorkflowRequest } from '../../shared/models';

// Shared env map carried by every script job. When running via
// submit.py the submitter injects HELION_API_URL + HELION_TOKEN
// so the in-workflow scripts (register.py etc.) can call back to
// the coordinator. The UI flow is equivalent — the dashboard
// user's active token is injected at submit time (TODO: land
// that injection when the UI wires real auth plumbing; today
// operators must pre-populate the env themselves). PATH is
// needed because the Rust runtime env_clear()s.
const JOB_ENV_BASE = {
  HELION_API_URL: 'http://coordinator:8080',
  PATH: '/usr/local/bin:/usr/bin:/bin',
} as const;

/**
 * Iris pipeline — 4 batch jobs + a separate serve step.
 *
 * Kept in sync with `examples/ml-iris/workflow.yaml`.
 */
export const IRIS_TEMPLATE: SubmitWorkflowRequest = {
  id:   'iris-wf-1',
  name: 'iris-end-to-end',
  jobs: [
    {
      name:            'ingest',
      command:         'python',
      args:            ['/app/ml-iris/ingest.py'],
      env:             { ...JOB_ENV_BASE },
      timeout_seconds: 60,
    },
    {
      name:            'preprocess',
      command:         'python',
      args:            ['/app/ml-iris/preprocess.py'],
      env:             { ...JOB_ENV_BASE },
      timeout_seconds: 60,
      depends_on:      ['ingest'],
    },
    {
      name:            'train',
      command:         'python',
      args:            ['/app/ml-iris/train.py'],
      env:             { ...JOB_ENV_BASE },
      timeout_seconds: 120,
      depends_on:      ['preprocess'],
    },
    {
      name:    'register',
      command: 'python',
      args:    ['/app/ml-iris/register.py'],
      env: {
        ...JOB_ENV_BASE,
        HELION_WORKFLOW_ID:    'iris-wf-1',
        HELION_TRAIN_JOB_NAME: 'train',
      },
      timeout_seconds: 60,
      depends_on:      ['train'],
    },
  ],
};

/**
 * MNIST pipeline — heavier demo + feature 21 heterogeneous
 * scheduling. Each job carries a `node_selector` mirroring
 * `examples/ml-mnist/workflow.yaml`:
 *
 *   - ingest / preprocess / register → runtime: go
 *   - train                          → runtime: rust
 *
 * The train pinning is the load-bearing demonstration for the
 * multi-node video; a drift that flips it back to Go silently
 * breaks the walkthrough's narrative. Covered by a unit test.
 */
export const MNIST_TEMPLATE: SubmitWorkflowRequest = {
  id:   'mnist-wf-1',
  name: 'mnist-end-to-end',
  jobs: [
    {
      name:            'ingest',
      command:         'python',
      args:            ['/app/ml-mnist/ingest.py'],
      env:             { ...JOB_ENV_BASE },
      timeout_seconds: 180,
      node_selector:   { runtime: 'go' },
    },
    {
      name:            'preprocess',
      command:         'python',
      args:            ['/app/ml-mnist/preprocess.py'],
      env:             { ...JOB_ENV_BASE },
      timeout_seconds: 60,
      depends_on:      ['ingest'],
      node_selector:   { runtime: 'go' },
    },
    {
      name:            'train',
      command:         'python',
      args:            ['/app/ml-mnist/train.py'],
      env:             { ...JOB_ENV_BASE },
      timeout_seconds: 180,
      depends_on:      ['preprocess'],
      node_selector:   { runtime: 'rust' },
    },
    {
      name:    'register',
      command: 'python',
      args:    ['/app/ml-mnist/register.py'],
      env: {
        ...JOB_ENV_BASE,
        HELION_WORKFLOW_ID:    'mnist-wf-1',
        HELION_TRAIN_JOB_NAME: 'train',
      },
      timeout_seconds: 60,
      depends_on:      ['train'],
      node_selector:   { runtime: 'go' },
    },
  ],
};

/**
 * Metadata for rendering the template picker cards.
 */
export interface TemplateCard {
  key:        'iris' | 'mnist' | 'custom';
  title:      string;
  blurb:      string;
  icon:       string;
  template?:  SubmitWorkflowRequest;  // omitted for "custom"
}

export const TEMPLATE_CARDS: readonly TemplateCard[] = [
  {
    key:   'iris',
    title: 'Iris pipeline',
    blurb: '4-job DAG (ingest → preprocess → train → register). ~10 s end to end. Sanity demo.',
    icon:  'local_florist',
    template: IRIS_TEMPLATE,
  },
  {
    key:   'mnist',
    title: 'MNIST pipeline',
    blurb: 'Heavier 4-job DAG. Train step pins to the Rust-runtime node via node_selector. ~60 s.',
    icon:  'auto_awesome',
    template: MNIST_TEMPLATE,
  },
  {
    key:   'custom',
    title: 'Custom',
    blurb: 'Skip the template — paste your own workflow JSON directly into the editor.',
    icon:  'edit_note',
  },
];
