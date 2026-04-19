// src/app/features/submit/workflow-shape-validator.ts
//
// Feature 22 step 3 — client-side workflow shape validator.
//
// Pure function that checks a parsed JSON value against the
// shape the server's handleSubmitWorkflow expects. Mirrors the
// Go validator's ordering and vocabulary so operators see
// consistent messages across client-side Validate and the
// server-side submit response.
//
// Not a security boundary — the server's validator is the
// authoritative one. This exists to surface errors in the
// browser before a round-trip, which is a UX win.
//
// When feature 24 (dry-run preflight) lands, the Validate
// button will substitute a POST /workflows?dry_run=true call
// for this function; until then we keep the client-side check
// as the only pre-submit feedback loop.

import { SubmitWorkflowRequest } from '../../shared/models';

export interface WorkflowValidationOutput {
  /** Human-readable issues. Empty iff the body is valid. */
  errors: string[];
  /** The input cast to SubmitWorkflowRequest iff errors is empty. */
  normalised: SubmitWorkflowRequest | unknown;
}

// Same env-key denylist as the Job form — see
// submit-job.component.ts for the comment on why the two client-
// side lists must stay in sync with the feature 25 server list.
const ENV_DENYLIST_PREFIXES = ['LD_', 'DYLD_'];
const ENV_DENYLIST_EXACT = new Set([
  'GCONV_PATH', 'GIO_EXTRA_MODULES', 'HOSTALIASES', 'NLSPATH', 'RES_OPTIONS',
]);

const MAX_JOBS         = 100;
const MAX_ENV_ENTRIES  = 128;
const MAX_ARGS         = 512;
const MAX_TIMEOUT_SEC  = 3600;
const KEY_SHAPE        = /^[A-Za-z_][A-Za-z0-9_]*$/;

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x);
}
function isString(x: unknown): x is string { return typeof x === 'string'; }
function isArrayOfStrings(x: unknown): x is string[] {
  return Array.isArray(x) && x.every(e => typeof e === 'string');
}

function checkEnv(env: unknown, where: string, errors: string[]): void {
  if (env === undefined) return;
  if (!isRecord(env)) {
    errors.push(`${where}: env must be a key/value object`);
    return;
  }
  const keys = Object.keys(env);
  if (keys.length > MAX_ENV_ENTRIES) {
    errors.push(`${where}: env has ${keys.length} entries (max ${MAX_ENV_ENTRIES})`);
  }
  for (const k of keys) {
    if (!KEY_SHAPE.test(k)) {
      errors.push(`${where}: env key "${k}" must match ^[A-Za-z_][A-Za-z0-9_]*$`);
      continue;
    }
    for (const p of ENV_DENYLIST_PREFIXES) {
      if (k.startsWith(p)) {
        errors.push(`${where}: env key "${k}" is a dynamic-loader injection vector (${p}* prefix)`);
      }
    }
    if (ENV_DENYLIST_EXACT.has(k)) {
      errors.push(`${where}: env key "${k}" is a known module-loading / resolver env var`);
    }
    if (!isString(env[k])) {
      errors.push(`${where}: env["${k}"] must be a string`);
    }
  }
}

/**
 * Validates a parsed workflow body. The `parsed` argument is
 * `unknown` because the caller passes `JSON.parse(text)` directly.
 *
 * Returns a list of human-readable errors (empty iff valid) +
 * the parsed body cast to SubmitWorkflowRequest (caller must
 * gate on errors.length === 0 before using it).
 */
export function validateWorkflowShape(parsed: unknown): WorkflowValidationOutput {
  const errors: string[] = [];

  if (!isRecord(parsed)) {
    errors.push('workflow body must be a JSON object');
    return { errors, normalised: parsed };
  }

  // Required fields at the top level.
  if (!isString(parsed['id'])) errors.push('id is required (string)');
  else if ((parsed['id'] as string).length === 0) errors.push('id must be non-empty');

  if (!isString(parsed['name'])) errors.push('name is required (string)');

  if (!Array.isArray(parsed['jobs'])) {
    errors.push('jobs is required (array of job objects)');
    return { errors, normalised: parsed };
  }
  const jobs = parsed['jobs'] as unknown[];
  if (jobs.length === 0) errors.push('jobs must contain at least one job');
  if (jobs.length > MAX_JOBS) errors.push(`jobs has ${jobs.length} entries (max ${MAX_JOBS})`);

  // Per-job checks.
  const seenNames = new Set<string>();
  jobs.forEach((j, i) => {
    const where = `jobs[${i}]`;
    if (!isRecord(j)) {
      errors.push(`${where}: must be an object`);
      return;
    }
    if (!isString(j['name']) || (j['name'] as string).length === 0) {
      errors.push(`${where}: name is required`);
    } else {
      const name = j['name'] as string;
      if (seenNames.has(name)) errors.push(`${where}: duplicate name "${name}"`);
      seenNames.add(name);
    }
    if (!isString(j['command']) || (j['command'] as string).length === 0) {
      errors.push(`${where}: command is required`);
    }
    if (j['args'] !== undefined && !isArrayOfStrings(j['args'])) {
      errors.push(`${where}: args must be an array of strings`);
    }
    if (Array.isArray(j['args']) && (j['args'] as unknown[]).length > MAX_ARGS) {
      errors.push(`${where}: args has too many entries (max ${MAX_ARGS})`);
    }
    if (j['depends_on'] !== undefined && !isArrayOfStrings(j['depends_on'])) {
      errors.push(`${where}: depends_on must be an array of strings`);
    }
    if (j['timeout_seconds'] !== undefined) {
      const t = j['timeout_seconds'];
      if (typeof t !== 'number' || !Number.isFinite(t) || t < 0 || t > MAX_TIMEOUT_SEC) {
        errors.push(`${where}: timeout_seconds must be a number in [0, ${MAX_TIMEOUT_SEC}]`);
      }
    }
    checkEnv(j['env'], where, errors);
  });

  // Validate depends_on references exist in the same workflow. If
  // a job says depends_on: [X] but no job has name X, the server
  // rejects — surface it client-side too.
  jobs.forEach((j, i) => {
    if (!isRecord(j) || !isArrayOfStrings(j['depends_on'])) return;
    for (const dep of j['depends_on'] as string[]) {
      if (!seenNames.has(dep)) {
        errors.push(`jobs[${i}] (${(j['name'] as string) || 'unnamed'}): depends_on references unknown job "${dep}"`);
      }
    }
  });

  return { errors, normalised: parsed };
}
