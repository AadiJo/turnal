'use strict';

const COLLECTOR_URL = 'https://telemetry.turnal.dev/v1/batch';

function resolveTelemetryReleaseConfig(env = process.env) {
  const endpoint = env.TURNAL_TELEMETRY_ENDPOINT || '';
  const percentText = env.TURNAL_TELEMETRY_ROLLOUT_PERCENT || '0';
  if (endpoint && endpoint !== COLLECTOR_URL) {
    throw new Error('TURNAL_TELEMETRY_ENDPOINT is not the allowlisted collector URL');
  }
  const rolloutPercent = Number.parseInt(percentText, 10);
  if (!/^\d+$/.test(percentText) || rolloutPercent < 0 || rolloutPercent > 100) {
    throw new Error('TURNAL_TELEMETRY_ROLLOUT_PERCENT must be an integer from 0 to 100');
  }
  if (!endpoint && rolloutPercent !== 0) {
    throw new Error('telemetry rollout must be 0 when the collector endpoint is empty');
  }
  return { endpoint, rolloutPercent };
}

module.exports = { COLLECTOR_URL, resolveTelemetryReleaseConfig };
