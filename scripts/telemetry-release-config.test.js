'use strict';

const assert = require('node:assert/strict');
const test = require('node:test');
const { COLLECTOR_URL, resolveTelemetryReleaseConfig } = require('./telemetry-release-config');

test('telemetry release config is dark by default', () => {
  assert.deepEqual(resolveTelemetryReleaseConfig({}), { endpoint: '', rolloutPercent: 0 });
});

test('telemetry release config accepts allowlisted canary percentages', () => {
  assert.deepEqual(resolveTelemetryReleaseConfig({
    TURNAL_TELEMETRY_ENDPOINT: COLLECTOR_URL,
    TURNAL_TELEMETRY_ROLLOUT_PERCENT: '10',
  }), { endpoint: COLLECTOR_URL, rolloutPercent: 10 });
});

test('telemetry release config rejects redirects, malformed values, and rollout without endpoint', () => {
  for (const env of [
    { TURNAL_TELEMETRY_ENDPOINT: 'https://example.com/v1/batch', TURNAL_TELEMETRY_ROLLOUT_PERCENT: '10' },
    { TURNAL_TELEMETRY_ENDPOINT: COLLECTOR_URL, TURNAL_TELEMETRY_ROLLOUT_PERCENT: '-1' },
    { TURNAL_TELEMETRY_ENDPOINT: COLLECTOR_URL, TURNAL_TELEMETRY_ROLLOUT_PERCENT: '101' },
    { TURNAL_TELEMETRY_ENDPOINT: COLLECTOR_URL, TURNAL_TELEMETRY_ROLLOUT_PERCENT: '10.5' },
    { TURNAL_TELEMETRY_ROLLOUT_PERCENT: '10' },
  ]) {
    assert.throws(() => resolveTelemetryReleaseConfig(env));
  }
});
