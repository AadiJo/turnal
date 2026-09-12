import { test, expect } from '@playwright/test';
import { startPreview } from '../../scripts/viewer-preview.mjs';

let preview;
test.beforeAll(async () => {
  test.setTimeout(180000);
  preview = await startPreview();
});
test.afterAll(async () => { await preview?.stop(); });

test('navigate recorded history, ignore a late turn response, and reload authenticated', async ({ page }) => {
  await page.goto(preview.url);
  await page.getByRole('link', { name: /turnal-demo/ }).first().click();
  await page.getByRole('link', { name: /Make the import queue resume/ }).click();
  await expect(page.locator('.turnrow')).toHaveCount(3);
  await page.locator('.turnrow').filter({ hasText: 'Make the import queue resume' }).click();
  await expect(page.locator('.file')).toContainText('internal/viewer/server.go');
  await expect(page.locator('.code')).toContainText('return host == expected');

  // Hold a real API response until the user has selected a different turn.
  let release;
  const held = new Promise(resolve => { release = resolve; });
  let fulfilled;
  const delivered = new Promise(resolve => { fulfilled = resolve; });
  let intercepted;
  const arrived = new Promise(resolve => { intercepted = resolve; });
  await page.route('**/api/v1/projects/**/turns/*', async route => {
    const response = await route.fetch();
    intercepted();
    await held;
    await route.fulfill({ response });
    fulfilled();
  }, { times: 1 });
  await page.locator('.turnrow').filter({ hasText: 'Bound batch payloads' }).click();
  await arrived;
  await page.locator('.turnrow').filter({ hasText: 'Use versioned keys' }).click();
  await expect(page.locator('.doc-body')).toContainText('Use versioned keys');
  release();
  await delivered;
  // Allow the fetch continuation and resulting render to settle after delivery.
  await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await expect(page.locator('.file')).toContainText('internal/viewer/identity.go');
  await expect(page.locator('.code')).toContainText('resourceKeyVersion');
  await expect(page.locator('.file')).not.toContainText('internal/viewer/service.go');

  expect(new URL(page.url()).hash).toBe('');
  await page.reload();
  await expect(page.locator('.doc-body')).toContainText('Use versioned keys');
  await expect(page.locator('.code')).toContainText('resourceKeyVersion');
  await expect(page.getByRole('alert')).toHaveCount(0);
});

// The proxy must not turn the backend's loopback check into an open relay.
test('reject unexpected preview Host and Origin values', async ({ request }) => {
  const url = new URL(preview.url);
  url.hash = '';
  expect((await request.get(url.href, { headers: { Host: 'attacker.invalid' } })).status()).toBe(403);
  expect((await request.get(url.href, { headers: { Origin: 'https://attacker.invalid' } })).status()).toBe(403);
});
