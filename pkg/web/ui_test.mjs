// Web UI validation tests for the maitred trigger dashboard.
//
// Run against a running maitred instance:
//   node pkg/web/ui_test.mjs --base-url http://localhost:18090
//
// These tests validate the standard behavior of the UI — page structure,
// data rendering, interactive controls, and error handling. They are
// designed to be run as part of CI alongside the Go test suite.

import { chromium } from 'playwright';

// ─── Argument parsing ────────────────────────────────────────────────

const args = process.argv.slice(2);
const baseUrl = args.find(a => a.startsWith('--base-url='))?.split('=')[1]
    ?? process.env.MAITRED_WEB_URL
    ?? 'http://localhost:18090';

// ─── Helpers ─────────────────────────────────────────────────────────

function assert(condition, label) {
  if (condition) return { pass: true, label };
  return { pass: false, label };
}

// ─── Test suites ─────────────────────────────────────────────────────

async function testPageLoads(page, results) {
  console.log('  Page loads');

  const resp = await page.goto(baseUrl);
  results.push(assert(resp.status() === 200, 'HTTP 200 on root URL'));
  results.push(assert(
    resp.headers()['content-type']?.includes('text/html'),
    'Content-Type is text/html'
  ));
}

async function testHeaderElements(page, results) {
  console.log('  Header elements');

  const title = await page.title();
  results.push(assert(title.includes('maitred'), 'Page title contains "maitred"'));
  results.push(assert(title.includes('Trigger Dashboard'), 'Page title contains "Trigger Dashboard"'));

  const h1Text = await page.locator('h1').textContent();
  results.push(assert(h1Text?.includes('maitred'), 'H1 contains "maitred"'));

  const versionEl = await page.locator('.version').textContent();
  results.push(assert(
    versionEl?.trim().length > 0 && versionEl.trim() !== 'loading...',
    `Version shows (got: "${versionEl}")`
  ));
}

async function testStatsSection(page, results) {
  console.log('  Stats section');

  const triggerCount = await page.locator('#trigger-count').textContent();
  results.push(assert(triggerCount === '2', `Trigger count shows 2 (got: "${triggerCount}")`));

  const successCount = await page.locator('#success-count').textContent();
  const failedCount = await page.locator('#failed-count').textContent();
  results.push(assert(/^\d+$/.test(successCount ?? ''), 'Success count is numeric'));
  results.push(assert(/^\d+$/.test(failedCount ?? ''), 'Failed count is numeric'));

  // Stats should add up to total triggers
  const totalStats = parseInt(successCount) + parseInt(failedCount);
  results.push(assert(totalStats === 2, `Success + Failed = Total triggers (got: ${totalStats})`));
}

async function testTriggerCards(page, results) {
  console.log('  Trigger cards');

  await page.waitForSelector('.trigger-card', { state: 'visible', timeout: 10000 });
  const cards = await page.locator('.trigger-card').count();
  results.push(assert(cards === 2, `Two trigger cards rendered (got: ${cards})`));

  const cardIds = await page.locator('.trigger-id').allTextContents();
  results.push(assert(cardIds.includes('new-open-weights-model'), 'Card for "new-open-weights-model" exists'));
  results.push(assert(cardIds.includes('daily-repo-review'), 'Card for "daily-repo-review" exists'));
}

async function testScheduleInfo(page, results) {
  console.log('  Schedule info');

  const schedules = await page.locator('.trigger-schedule').allTextContents();
  results.push(assert(schedules.includes('0 */6 * * *'), 'Schedule "0 */6 * * *" displayed'));
  results.push(assert(schedules.includes('@daily'), 'Schedule "@daily" displayed'));
}

async function testActionButtons(page, results) {
  console.log('  Action buttons');

  const fireButtons = await page.locator('button:has-text("Fire now")').count();
  results.push(assert(fireButtons === 2, `Two "Fire now" buttons (got: ${fireButtons})`));

  const pauseButtons = await page.locator('button:has-text("Pause")').count();
  results.push(assert(pauseButtons === 2, `Two "Pause" buttons (got: ${pauseButtons})`));
}

async function testMetadataFields(page, results) {
  console.log('  Metadata fields');

  const metaLabels = await page.locator('.meta-label').allTextContents();
  results.push(assert(metaLabels.includes('Last run'), 'Meta label "Last run" present'));
  results.push(assert(metaLabels.includes('Last task'), 'Meta label "Last task" present'));
  results.push(assert(metaLabels.includes('Last result'), 'Meta label "Last result" present'));
  results.push(assert(metaLabels.includes('Next run'), 'Meta label "Next run" present'));
}

async function testNeverRanState(page, results) {
  console.log('  Never-ran state');

  const cardTexts = await page.locator('.trigger-card').allTextContents();
  results.push(assert(
    cardTexts.some(t => t.includes('Never') || /^\d/.test(t)),
    'Shows last run time or "Never" on trigger cards'
  ));

  const badgeTexts = await page.locator('.badge').allTextContents();
  results.push(assert(badgeTexts.length >= 2, 'Shows at least 2 badge elements'));
  results.push(assert(
    badgeTexts.some(t => t.includes('No data')),
    'Shows "No data" badge for never-ran triggers'
  ));
}

async function testCountdownDisplay(page, results) {
  console.log('  Countdown display');

  const countdowns = await page.locator('.countdown').allTextContents();
  results.push(assert(countdowns.length === 2, `Two countdown elements (got: ${countdowns.length})`));
  results.push(assert(
    countdowns.some(c => c.includes('Cron') || c.includes('Due') || c.includes('Pending') || /\d+s$/.test(c)),
    `Countdown shows meaningful value (got: ${countdowns.join(', ')})`
  ));
}

async function testRefreshBar(page, results) {
  console.log('  Refresh bar');

  const statusText = await page.locator('#status-text').textContent();
  results.push(assert(
    statusText?.includes('Updated') || statusText?.includes('Error') || statusText?.includes('Connecting'),
    `Status text shows (got: "${statusText}")`
  ));

  const refreshButton = await page.locator('button:has-text("Refresh now")').count();
  results.push(assert(refreshButton === 1, 'Refresh now button exists'));
}

async function testSpaFallback(page, results) {
  console.log('  SPA fallback');

  const fallbackResp = await page.goto(`${baseUrl}/some-random-path`);
  results.push(assert(fallbackResp.status() === 200, 'SPA fallback returns 200 for unknown paths'));
  const fallbackTitle = await page.title();
  results.push(assert(fallbackTitle.includes('maitred'), 'SPA fallback page has correct title'));

  // Navigate back to main page
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(2000);
}

async function testCardStyling(page, results) {
  console.log('  Card styling');

  const hasValidClass = await page.locator('.trigger-card').all().then(async cards =>
    cards.some(async c => {
      const cls = await c.getAttribute('class');
      return typeof cls === 'string' && (
        cls.includes('ok') || cls.includes('failed') || cls.includes('paused') || cls.includes('never-ran')
      );
    })
  );
  results.push(assert(hasValidClass, 'Cards show a valid state class (ok/failed/paused/never-ran)'));
}

async function testTriggerDetailEndpoint(page, results) {
  console.log('  Trigger detail endpoint');

  const triggerDetail = await page.goto(`${baseUrl}/api/triggers/new-open-weights-model`);
  results.push(assert(triggerDetail.status() === 200, 'Trigger detail returns 200'));
  const detailJson = await triggerDetail.json();
  results.push(assert(detailJson.id === 'new-open-weights-model', 'Trigger detail has correct ID'));
  results.push(assert(detailJson.paused === false, 'Trigger is not paused'));
  results.push(assert(detailJson.def?.schedule === '0 */6 * * *', 'Trigger detail has correct schedule'));

  // Navigate back
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(2000);
}

async function testPauseResumeInteraction(page, results) {
  console.log('  Pause/Resume interaction');

  const pauseBtns = await page.locator('button:has-text("Pause")').all();
  results.push(assert(pauseBtns.length === 2, 'Two Pause buttons visible'));

  await pauseBtns[0].click();
  await page.waitForTimeout(3000);

  const resumeBtnsAfter = await page.locator('button:has-text("Resume")').count();
  results.push(assert(resumeBtnsAfter >= 1, `After pause, at least one button shows "Resume" (got: ${resumeBtnsAfter})`));

  // Click Resume
  const resumeBtns = await page.locator('button:has-text("Resume")').all();
  if (resumeBtns.length > 0) {
    await resumeBtns[0].click();
    await page.waitForTimeout(3000);
  }
}

async function testFireNowInteraction(page, results) {
  console.log('  Fire now interaction');

  const fireBtns = await page.locator('button:has-text("Fire now")').all();
  results.push(assert(fireBtns.length === 2, 'Two Fire now buttons visible'));

  await fireBtns[0].click();
  await page.waitForTimeout(3000);

  const statusAfterFire = await page.locator('#status-text').textContent();
  results.push(assert(
    statusAfterFire?.includes('Updated'),
    `After fire, status shows "Updated" (got: "${statusAfterFire}")`
  ));
}

async function testApiErrorHandling(page, results) {
  console.log('  API error handling');

  const notFoundResp = await page.goto(`${baseUrl}/api/triggers/nonexistent-trigger`);
  results.push(assert(notFoundResp.status() === 404, 'Non-existent trigger returns 404'));

  // Navigate back
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(1000);
}

async function testApiValidation(page, results) {
  console.log('  API validation');

  const badReqStatus = await page.evaluate(async () => {
    const r = await fetch('/api/triggers//pause', { method: 'POST' });
    return r.status;
  });
  results.push(assert(badReqStatus !== 200, `Bad trigger action returns error (got: ${badReqStatus})`));

  // Navigate back
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(1000);
}

async function testHttpMethodEnforcement(page, results) {
  console.log('  HTTP method enforcement');

  const wrongMethodResp = await page.evaluate(async () => {
    const r = await fetch('/api/triggers/new-open-weights-model/pause', { method: 'GET' });
    return r.status;
  });
  results.push(assert(wrongMethodResp === 405, 'GET on pause endpoint returns 405'));

  // Navigate back
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(1000);
}

async function testHistoryEndpoint(page, results) {
  console.log('  History endpoint');

  const historyResp = await page.evaluate(async () => {
    const r = await fetch('/api/history');
    return { status: r.status, json: await r.json() };
  });
  results.push(assert(historyResp.status === 200, 'History endpoint returns 200'));
  results.push(assert(typeof historyResp.json === 'object', 'History returns an object'));
}

async function testVersionEndpoint(page, results) {
  console.log('  Version endpoint');

  const versionResp = await page.evaluate(async () => {
    const r = await fetch('/api/version');
    return { status: r.status, text: await r.text() };
  });
  results.push(assert(versionResp.status === 200, 'Version endpoint returns 200'));
  results.push(assert(versionResp.text.length > 0, 'Version returns non-empty string'));
  results.push(assert(
    versionResp.text !== 'dev' || versionResp.text.includes('e088813'),
    `Version returns build info (got: "${versionResp.text}")`
  ));
}

async function testWebhookEndpoints(page, results) {
  console.log('  Webhook endpoints');

  // Check the /api/webhooks endpoint
  const whResp = await page.evaluate(async () => {
    const r = await fetch('/api/webhooks');
    return { status: r.status, json: await r.json() };
  });
  results.push(assert(whResp.status === 200, 'Webhooks endpoint returns 200'));
  results.push(assert(Array.isArray(whResp.json), 'Webhooks returns an array'));
  results.push(assert(whResp.json.length > 0, 'Webhooks has at least one provider'));

  // Check that webhook endpoint info is rendered on trigger cards
  const cardTexts = await page.locator('.trigger-card').allTextContents();
  const combinedText = cardTexts.join(' ');
  results.push(assert(
    combinedText.includes('/v1/forgejo/pull-request'),
    'Webhook URL /v1/forgejo/pull-request displayed on trigger card'
  ));
  results.push(assert(
    combinedText.includes('/v1/forgejo/push'),
    'Webhook URL /v1/forgejo/push displayed on trigger card'
  ));
  results.push(assert(
    combinedText.includes('Webhook endpoints'),
    'Webhook endpoints section header present'
  ));
  results.push(assert(
    combinedText.includes('forgejo'),
    'Provider name "forgejo" displayed on trigger card'
  ));

  // Check the webhook badge element
  const badges = await page.locator('.webhook-badge').count();
  results.push(assert(badges > 0, `Webhook badges rendered (got: ${badges})`));

  // Check the webhook URL element
  const webhookUrls = await page.locator('.webhook-url').allTextContents();
  results.push(assert(
    webhookUrls.some(u => u.includes('/v1/forgejo/pull-request')),
    'Webhook URL element contains pull-request path'
  ));
}

async function testWebhookEndpointApiError(page, results) {
  console.log('  Webhook endpoint API error handling');

  // Test method enforcement on webhook endpoint
  const wrongMethodResp = await page.evaluate(async () => {
    const r = await fetch('/api/webhooks', { method: 'POST' });
    return r.status;
  });
  results.push(assert(wrongMethodResp === 405, 'POST on webhooks endpoint returns 405'));
}

async function testPromptSection(page, results) {
  console.log('  Prompt section');

  // Ensure we are on the main page
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(3000);

  // Check that prompt sections exist on trigger cards
  const promptSections = await page.locator('.prompt-section').count();
  results.push(assert(promptSections === 2, `Two prompt sections rendered (got: ${promptSections})`));

  // Check that summary labels are present
  const summaryTexts = await page.locator('.prompt-section summary').allTextContents();
  results.push(assert(
    summaryTexts.every(t => t.includes('Prompt template')),
    'Prompt section summary labels say "Prompt template"'
  ));

  // Check that prompt content is collapsed by default (details element not open)
  const openCount = await page.locator('.prompt-section[open]').count();
  results.push(assert(openCount === 0, `Prompt sections collapsed by default (got: ${openCount} open)`));

  // Click to expand first prompt section
  const firstSummary = await page.locator('.prompt-section summary').first();
  await firstSummary.click();
  await page.waitForTimeout(500);

  // Check that it is now open
  const openAfterClick = await page.locator('.prompt-section[open]').count();
  results.push(assert(openAfterClick >= 1, `Prompt section expanded after click (got: ${openAfterClick} open)`));

  // Check that prompt content is visible and contains expected text
  const promptContents = await page.locator('.prompt-content').allTextContents();
  results.push(assert(
    promptContents.some(c => c.includes('open-weights') || c.includes('LLM') || c.includes('models')),
    'Expanded prompt contains expected content about models'
  ));

  // Click again to collapse
  await firstSummary.click();
  await page.waitForTimeout(500);

  const openAfterCollapse = await page.locator('.prompt-section[open]').count();
  results.push(assert(openAfterCollapse === 0, `Prompt section collapsed after second click (got: ${openAfterCollapse} open)`));

  // Check second trigger's prompt content
  const secondSummary = await page.locator('.prompt-section summary').nth(1);
  await secondSummary.click();
  await page.waitForTimeout(500);

  const allPromptContents = await page.locator('.prompt-content').allTextContents();
  results.push(assert(
    allPromptContents.some(c => c.includes('repositories') || c.includes('Review') || c.includes('PRs')),
    'Second prompt contains expected content about repo review'
  ));

  // Collapse again
  await secondSummary.click();
  await page.waitForTimeout(500);
}

// ─── Main ────────────────────────────────────────────────────────────

async function main() {
  const browser = await chromium.launch({ headless: true });
  const context = await browser.newContext();
  const page = await context.newPage();

  // Wait for page to be ready
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(2000);

  const results = [];

  // Run all test suites
  await testPageLoads(page, results);
  await testHeaderElements(page, results);
  await testStatsSection(page, results);
  await testTriggerCards(page, results);
  await testScheduleInfo(page, results);
  await testActionButtons(page, results);
  await testMetadataFields(page, results);
  await testNeverRanState(page, results);
  await testCountdownDisplay(page, results);
  await testRefreshBar(page, results);
  await testSpaFallback(page, results);
  await testCardStyling(page, results);
  await testTriggerDetailEndpoint(page, results);
  await testPauseResumeInteraction(page, results);
  await testFireNowInteraction(page, results);
  await testApiErrorHandling(page, results);
  await testApiValidation(page, results);
  await testHttpMethodEnforcement(page, results);
  await testHistoryEndpoint(page, results);
  await testVersionEndpoint(page, results);
  await testWebhookEndpoints(page, results);
  await testWebhookEndpointApiError(page, results);
  await testPromptSection(page, results);

  await browser.close();

  // Print results
  const passed = results.filter(r => r.pass).length;
  const failed = results.filter(r => !r.pass).length;
  const total = results.length;

  console.log('');
  console.log('='.repeat(60));
  console.log(`  Results: ${passed} passed, ${failed} failed, ${total} total`);
  console.log('='.repeat(60));

  for (const r of results) {
    if (r.pass) {
      console.log(`  ✅ ${r.label}`);
    } else {
      console.log(`  ❌ ${r.label}`);
    }
  }
  console.log('='.repeat(60));

  if (failed > 0) {
    console.log(`\n❌ ${failed} test(s) failed!`);
    process.exit(1);
  } else {
    console.log('\n✅ All UI tests passed!');
    process.exit(0);
  }
}

main().catch(err => {
  console.error('Test runner error:', err);
  process.exit(1);
});
