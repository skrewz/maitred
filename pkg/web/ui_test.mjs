// Web UI validation tests for the maitred trigger dashboard.
//
// Run against a running maitred instance:
//   node pkg/web/ui_test.mjs --base-url http://localhost:18090
//
// These tests validate the standard behavior of the UI — page structure,
// data rendering, interactive controls, and error handling. They are
// designed to be run as part of CI alongside the Go test suite.

import { chromium } from 'playwright';
import { createServer } from 'node:http';
import { createHmac } from 'node:crypto';

// ─── Argument parsing ────────────────────────────────────────────────

const args = process.argv.slice(2);
const baseUrl = args.find(a => a.startsWith('--base-url='))?.split('=')[1]
    ?? process.env.MAITRED_WEB_URL
    ?? 'http://localhost:18090';
const apiUrl = args.find(a => a.startsWith('--api-url='))?.split('=')[1]
    ?? 'http://localhost:18091';
const forgejoPort = parseInt(args.find(a => a.startsWith('--forgejo-port='))?.split('=')[1] ?? '18092', 10);
const forgejoSecret = args.find(a => a.startsWith('--forgejo-secret='))?.split('=')[1]
    ?? 'ui-test-secret';
// Base URLs of the two disabled-engine instances the Makefile starts
// alongside the main one: one configured but unable to serve, one with
// nothing configured at all. The engine-states suite is skipped when
// they are absent, so a hand-started single instance still runs.
const faultUrl = args.find(a => a.startsWith('--fault-url='))?.split('=')[1] ?? null;
const unconfiguredUrl = args.find(a => a.startsWith('--unconfigured-url='))?.split('=')[1] ?? null;

// ─── Mock Forgejo API ────────────────────────────────────────────────

// A minimal Forgejo REST API v1 serving one maitred-enabled repo (o/r)
// with one open issue (#7). The maitred instance under test points
// MAITRED_FORGEJO_URL at it, so the Forgejo engine is enabled and its
// re-fetches hit this mock (see the Makefile test-ui target).
const mockIssue = {
  number: 7,
  title: 'Test issue',
  state: 'open',
  labels: [],
  updated_at: '2026-09-16T13:25:18Z',
  html_url: 'https://forge.example.com/o/r/issues/7',
  pull_request: null,
  repository: { full_name: 'o/r' },
};

// The pull request the fan-out test opens: mergeable and open, so the
// PR-opened row is the review fan-out — one dispatch per configured
// lens, each recorded in its own subject slot of the watermark
// (§forgejo/decisions/review-lenses, §forgejo/state/watermark).
const mockPull = {
  number: 9,
  title: 'Test pull request',
  state: 'open',
  merged: false,
  mergeable: true,
  head: { sha: 'sha-abc' },
  user: { login: 'implementer' },
  labels: [],
  body: '',
  updated_at: '2026-09-16T13:25:18Z',
  html_url: 'https://forge.example.com/o/r/pulls/9',
};

function startMockForgejo(port) {
  const server = createServer((req, res) => {
    const send = (code, obj) => {
      res.writeHead(code, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify(obj));
    };
    const p = new URL(req.url, `http://localhost:${port}`).pathname;
    if (p === '/api/v1/orgs/o/repos') mockReposListings++;
    switch (p) {
      case '/api/v1/orgs/o/repos':
        return send(200, [{
          id: 1, name: 'r', full_name: 'o/r',
          owner: { login: 'o' },
          html_url: 'https://forge.example.com/o/r',
          topics: ['maitred-enabled'],
        }]);
      case '/api/v1/repos/o/r/issues':
        return send(200, [mockIssue]);
      case '/api/v1/repos/o/r/pulls':
        return send(200, []);
      case '/api/v1/repos/o/r/pulls/9':
        return send(200, mockPull);
      case '/api/v1/repos/o/r/pulls/9/reviews':
        return send(200, []);
      case '/api/v1/repos/o/r/issues/7':
        return send(200, mockIssue);
      case '/api/v1/repos/o/r/issues/7/dependencies':
        return send(200, []);
      case '/api/v1/repos/o/r/issues/7/blocks':
        return send(200, []);
      default:
        return send(404, { message: `not found: ${p}` });
    }
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, () => resolve(server));
  });
}

function forgejoSignature(body) {
  return createHmac('sha256', forgejoSecret).update(body).digest('hex');
}

// Counts the org-repository listings the mock has served. The sweep
// lists the org's repositories as its first act, so the count rising
// is how a test sees a manually triggered sweep run
// (§forgejo/observability/triggering-a-sweep).
let mockReposListings = 0;

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

async function testPersonaDisplay(page, results) {
  console.log('  Persona display');

  // Ensure we are on the main page
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(3000);

  // Check for persona meta labels (should appear on triggers that have persona set)
  const metaLabels = await page.locator('.meta-label').allTextContents();
  const hasPersonaLabel = metaLabels.includes('Persona');
  results.push(assert(
    hasPersonaLabel,
    `Persona meta label present on trigger cards (found labels: ${metaLabels.join(', ')})`
  ));

  if (hasPersonaLabel) {
    // Check that persona value is displayed
    const metaValues = await page.locator('.meta-value').allTextContents();
    const hasPersonaValue = metaValues.some(v => v.includes('issue-reviewer') || v.includes('implementer'));
    results.push(assert(
      hasPersonaValue,
      `Persona value displayed (found values: ${metaValues.join(', ')})`
    ));
  }
}

async function testForgejoTracking(page, results) {
  console.log('  Forgejo tracking view');

  // Deliver a signed "issue opened" webhook to the engine
  // (§forgejo/webhook/the-pipeline).
  const body = JSON.stringify({
    action: 'opened',
    repository: { full_name: 'o/r' },
    issue: { number: 7 },
    sender: { login: 'alice' },
  });
  const delivery = await fetch(`${apiUrl}/v1/forgejo/all_events`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Forgejo-Event-Type': 'issues',
      'X-Forgejo-Signature': forgejoSignature(body),
    },
    body,
  });
  results.push(assert(delivery.status === 204, `Webhook delivery acknowledged (got: ${delivery.status})`));

  // Poll the dashboard endpoint until the tracked entry appears. The
  // response is an object carrying the tracked list alongside the one
  // engine-status field (§forgejo/observability/the-dashboard-view).
  let view = {};
  for (let i = 0; i < 40; i++) {
    const r = await fetch(`${baseUrl}/api/forgejo`).catch(() => ({ ok: false }));
    if (r.ok) view = await r.json();
    if ((view.items ?? []).length > 0) break;
    await new Promise(res => setTimeout(res, 500));
  }
  const tracked = view.items ?? [];
  results.push(assert(tracked.length === 1, `One tracked entry in /api/forgejo (got: ${tracked.length})`));
  results.push(assert(
    view.engine_status === 'enabled',
    `Engine status field reads "enabled" for the running engine (got: "${view.engine_status}")`
  ));

  const entry = tracked[0] ?? {};
  results.push(assert(
    entry.repo === 'o/r' && entry.kind === 'issue' && entry.number === 7,
    `Tracked entry is o/r issue #7 (got: ${entry.repo}/${entry.kind}/${entry.number})`
  ));
  results.push(assert(entry.state === 'open', `Tracked entry state is open (got: "${entry.state}")`));
  results.push(assert(
    entry.watermark?.action === 'implement',
    `Watermark action is implement (got: "${entry.watermark?.action}")`
  ));
  results.push(assert(
    typeof entry.decision?.reason === 'string' && entry.decision.reason.length > 0,
    `Last decision carries a reason (got: "${entry.decision?.reason}")`
  ));

  // The review fan-out: opening a pull request dispatches one task
  // per configured lens and records one subject slot per lens while
  // the main watermark slot stays untouched
  // (§forgejo/decisions/review-lenses, §forgejo/state/watermark).
  // The tracked row must make those slots visible
  // (§forgejo/observability/the-dashboard-view).
  const prBody = JSON.stringify({
    action: 'opened',
    repository: { full_name: 'o/r' },
    pull_request: { number: 9 },
    sender: { login: 'alice' },
  });
  const prDelivery = await fetch(`${apiUrl}/v1/forgejo/all_events`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      'X-Forgejo-Event-Type': 'pull_request',
      'X-Forgejo-Signature': forgejoSignature(prBody),
    },
    body: prBody,
  });
  results.push(assert(prDelivery.status === 204, `PR webhook delivery acknowledged (got: ${prDelivery.status})`));

  let prEntry = {};
  for (let i = 0; i < 40; i++) {
    const r = await fetch(`${baseUrl}/api/forgejo`).catch(() => ({ ok: false }));
    const viewNow = r.ok ? await r.json() : {};
    const found = (viewNow.items ?? []).find(item => item.kind === 'pr' && item.number === 9);
    if (found) { prEntry = found; break; }
    await new Promise(res => setTimeout(res, 500));
  }
  const subjectIds = Object.keys(prEntry.watermark?.subjects ?? {}).sort();
  results.push(assert(
    subjectIds.length === 3,
    `Fan-out records one subject slot per configured lens (got: ${subjectIds.join(', ')})`
  ));
  results.push(assert(
    subjectIds.every(id =>
      prEntry.watermark?.subjects?.[id]?.action === 'review' &&
      prEntry.watermark?.subjects?.[id]?.revision === 'sha-abc'),
    'Each subject slot records review at the head sha'
  ));

  // The dashboard section renders the tracked issue
  // (§forgejo/observability/the-dashboard-view).
  await page.goto(baseUrl);
  await page.waitForSelector('#forgejo-section', { state: 'visible', timeout: 20000 });
  const sectionText = await page.locator('#forgejo-section').textContent();
  results.push(assert(sectionText.includes('o/r'), 'Forgejo section shows the repository'));
  results.push(assert(sectionText.includes('issue #7'), 'Forgejo section shows issue #7'));
  results.push(assert(sectionText.includes('implement'), 'Forgejo section shows the implement watermark'));
  results.push(assert(sectionText.includes('pr #9'), 'Forgejo section shows the tracked pull request'));
  results.push(assert(
    sectionText.includes('review: reviewer-maintainability, reviewer-minimalism, reviewer-security @ sha-abc'),
    'Watermark cell lists the fan-out subject slots (§forgejo/state/watermark)'
  ));

  const stateBadges = await page.locator('#forgejo-section .badge').allTextContents();
  results.push(assert(stateBadges.some(b => b.toLowerCase() === 'open'), `Forgejo section shows an open state badge (got: ${stateBadges.join(', ')})`));

  // The by-repo layout (§forgejo/observability/the-dashboard-view):
  // each tracked repo forms a section introduced by a heading-level
  // anchor, and under each repo two tabs split the rows into issues
  // and pull requests.
  const repoHeadings = await page.locator('#forgejo-section h3.forgejo-repo-heading');
  const headingTexts = await repoHeadings.allTextContents();
  results.push(assert(
    headingTexts.includes('o/r'),
    `Tracked repos get heading-level sections (headings: ${headingTexts.join(', ')})`
  ));
  const anchorId = headingTexts.length > 0 ? await repoHeadings.first().getAttribute('id') : null;
  results.push(assert(
    typeof anchorId === 'string' && anchorId.length > 0,
    `The repo heading carries an anchor id (got: "${anchorId}")`
  ));

  const tabButtons = await page.locator('#forgejo-section .forgejo-tab');
  const tabTexts = await tabButtons.allTextContents();
  results.push(assert(
    tabTexts.length === 2
      && tabTexts.some(t => t.toLowerCase().includes('issue'))
      && tabTexts.some(t => t.toLowerCase().includes('pull request')),
    `Each repo gets an issues tab and a pull-requests tab (got: ${tabTexts.join(' | ')})`
  ));

  // The pull requests tab is the other panel: with the issues tab
  // active the PR card is not shown, and clicking the pull-requests
  // tab swaps the panels.
  const prTab = page.locator('#forgejo-section .forgejo-tab[data-kind="pr"]');
  const issuePanel = page.locator('#forgejo-section .forgejo-panel[data-repo="o/r"][data-kind="issue"]');
  const prPanel = page.locator('#forgejo-section .forgejo-panel[data-repo="o/r"][data-kind="pr"]');
  const issueVisibleFirst = await issuePanel.isVisible().catch(() => false);
  const prVisibleFirst = await prPanel.isVisible().catch(() => false);
  results.push(assert(
    issueVisibleFirst && !prVisibleFirst,
    `Issues tab is active by default and the PR panel is hidden (issue: ${issueVisibleFirst}, pr: ${prVisibleFirst})`
  ));
  await prTab.click();
  await page.waitForTimeout(200);
  const prVisibleAfter = await prPanel.isVisible().catch(() => false);
  const issueVisibleAfter = await issuePanel.isVisible().catch(() => false);
  results.push(assert(
    prVisibleAfter && !issueVisibleAfter,
    `Clicking the pull-requests tab swaps the panels (pr: ${prVisibleAfter}, issue: ${issueVisibleAfter})`
  ));
  // Restore the issues tab for anything that follows.
  await page.locator('#forgejo-section .forgejo-tab[data-kind="issue"]').click();

  // The watermark and reason cells are click-to-open: collapsed by
  // default, one click opens them (§forgejo/observability/the-dashboard-view).
  const detailsTotal = await page.locator('#forgejo-section details.forgejo-details').count();
  results.push(assert(
    detailsTotal >= 2,
    `Watermark and reason render as click-to-open elements per row (got: ${detailsTotal})`
  ));
  const openBefore = await page.locator('#forgejo-section details.forgejo-details[open]').count();
  results.push(assert(
    openBefore === 0,
    `Click-to-open elements are collapsed by default (got: ${openBefore} open)`
  ));
  await page.locator('#forgejo-section details.forgejo-details[data-what="watermark"] summary').first().click();
  await page.waitForTimeout(200);
  const openAfter = await page.locator('#forgejo-section details.forgejo-details[open]').count();
  results.push(assert(
    openAfter === 1,
    `One click opens the watermark element (got: ${openAfter} open)`
  ));
  await page.locator('#forgejo-section details.forgejo-details[data-what="watermark"] summary').first().click();

  // Newline handling and per-repo grouping, exercised on a synthetic
  // payload (two repos, a multi-row watermark, a reason with an
  // embedded newline) rendered through the page's own renderForgejo:
  // the live instance cannot produce those shapes (§forgejo/observability/the-dashboard-view).
  const synthetic = await page.evaluate(() => {
    const payload = {
      engine_status: 'enabled',
      sweeps: [],
      items: [
        {
          repo: 'a/x', kind: 'issue', number: 1, state: 'open', merged: false,
          watermark: { action: 'implement', revision: 'rev-1', subjects: { 'reviewer-x': { action: 'review', revision: 'rev-2' } } },
          decision: { event: 'issues', dispatched: true, action: 'implement', reason: 'first line\nsecond line', at: '2026-11-12T00:00:00Z' },
        },
        {
          repo: 'a/x', kind: 'pr', number: 2, state: 'open', merged: false,
          watermark: { action: 'review', revision: 'rev-3' },
          decision: { event: 'pull_request', dispatched: false, action: '', reason: 'holding', at: '2026-11-12T00:00:00Z' },
        },
        {
          repo: 'b/y', kind: 'pr', number: 3, state: 'closed', merged: true,
          decision: { event: 'pull_request', dispatched: false, action: '', reason: 'nothing to do', at: '2026-11-12T00:00:00Z' },
        },
      ],
    };
    renderForgejo(payload);
    const section = document.getElementById('forgejo-section');
    const headings = [...section.querySelectorAll('h3.forgejo-repo-heading')].map(h => h.textContent.trim());
    const bPr = section.querySelector('.forgejo-panel[data-repo="b/y"][data-kind="pr"]');
    const bIssue = section.querySelector('.forgejo-panel[data-repo="b/y"][data-kind="issue"]');
    const watermark = section.querySelector('details.forgejo-details[data-what="watermark"] .forgejo-details-content');
    const reason = section.querySelector('details.forgejo-details[data-what="reason"] .forgejo-details-content');
    return {
      headings,
      bPrDefaultVisible: bPr && !bPr.hasAttribute('hidden'),
      bIssueDefaultHidden: bIssue && bIssue.hasAttribute('hidden'),
      watermarkText: watermark ? watermark.textContent : '',
      watermarkWhitespace: watermark ? getComputedStyle(watermark).whiteSpace : '',
      reasonText: reason ? reason.textContent : '',
      reasonWhitespace: reason ? getComputedStyle(reason).whiteSpace : '',
    };
  });
  results.push(assert(
    synthetic.headings.length === 2 && synthetic.headings.includes('a/x') && synthetic.headings.includes('b/y'),
    `Synthetic payload splits into one anchored section per repo (got: ${synthetic.headings.join(', ')})`
  ));
  results.push(assert(
    synthetic.bPrDefaultVisible && synthetic.bIssueDefaultHidden,
    'A repo with only pull requests defaults to the pull-requests tab'
  ));
  results.push(assert(
    synthetic.watermarkText.includes('\n') && synthetic.watermarkWhitespace === 'pre-wrap',
    'Watermark rows keep their newlines and render them as breaks'
  ));
  results.push(assert(
    synthetic.reasonText.includes('first line\nsecond line') && synthetic.reasonWhitespace === 'pre-wrap',
    'An embedded newline in the reason is respected'
  ));

  // The active tab is module state, so a re-render (the auto-refresh
  // rebuilds the section every few seconds) keeps the operator's
  // choice: open the a/x pull-requests tab, re-render, and it stays.
  const tabPersistence = await page.evaluate(() => {
    const before = document.querySelector('#forgejo-section .forgejo-tab[data-repo="a/x"][data-kind="pr"]');
    before.click();
    renderForgejo({
      engine_status: 'enabled',
      sweeps: [],
      items: [
        { repo: 'a/x', kind: 'issue', number: 1, state: 'open', decision: { event: 'issues', dispatched: false, reason: 'r', at: '' } },
        { repo: 'a/x', kind: 'pr', number: 2, state: 'open', decision: { event: 'pull_request', dispatched: false, reason: 'r', at: '' } },
      ],
    });
    const pr = document.querySelector('#forgejo-section .forgejo-panel[data-repo="a/x"][data-kind="pr"]');
    const issue = document.querySelector('#forgejo-section .forgejo-panel[data-repo="a/x"][data-kind="issue"]');
    return { prVisible: pr && !pr.hasAttribute('hidden'), issueHidden: issue && issue.hasAttribute('hidden') };
  });
  results.push(assert(
    tabPersistence.prVisible && tabPersistence.issueHidden,
    'The chosen tab survives a re-render'
  ));

  // Navigate back and let the page settle for the remaining suites.
  await page.waitForLoadState('networkidle').catch(() => {});
  await page.waitForTimeout(1000);
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

// testForgejoEngineStates exercises the fault render of the
// dashboard's Forgejo view — the one user-visible behaviour this view
// adds — against the two disabled-engine instances the Makefile starts
// alongside the running one: a configured engine that could not be
// constructed must name its category, and an engine nothing configured
// must leave the view hidden (§forgejo/observability/the-dashboard-view,
// §forgejo/observability/why-the-engine-is-off).
async function testForgejoEngineStates(page, results) {
  console.log('  Forgejo engine states on the dashboard');

  // Configured but broken: the config is there, the credentials are
  // not, so construction fails with the credentials category.
  const faultView = await (await fetch(`${faultUrl}/api/forgejo`)).json();
  results.push(assert(
    faultView.engine_status === 'credentials missing',
    `Fault instance reports "credentials missing" (got: "${faultView.engine_status}")`
  ));
  results.push(assert(
    (faultView.items ?? []).length === 0,
    `Fault instance tracks nothing (got: ${(faultView.items ?? []).length})`
  ));
  results.push(assert(
    (faultView.sweeps ?? []).length === 0,
    `Fault instance offers no sweeps to trigger (got: ${JSON.stringify(faultView.sweeps)})`
  ));
  const faultFire = await fetch(`${faultUrl}/api/forgejo/sweeps/reconcile`, { method: 'POST' });
  results.push(assert(
    faultFire.status === 503,
    `Triggering a sweep without an engine answers 503 (got: ${faultFire.status})`
  ));

  await page.goto(faultUrl);
  await page.waitForSelector('#forgejo-section', { state: 'visible', timeout: 20000 })
    .catch(() => {});
  const faultVisible = await page.locator('#forgejo-section').isVisible();
  results.push(assert(faultVisible, 'Fault instance shows the Forgejo section with nothing tracked'));
  if (faultVisible) {
    const faultText = await page.locator('#forgejo-section').textContent();
    results.push(assert(
      faultText.includes('credentials missing'),
      `Fault instance names the category on the dashboard (got: "${faultText?.trim()}")`
    ));
    results.push(assert(
      !faultText.includes('MAITRED_FORGEJO') && !faultText.includes('environment incomplete'),
      'Fault instance names no raw error text or env var'
    ));
  }

  // Nothing configured at all: the view stays hidden, so the fault
  // render is not just "the section is always there".
  const noneView = await (await fetch(`${unconfiguredUrl}/api/forgejo`)).json();
  results.push(assert(
    noneView.engine_status === 'not configured',
    `Unconfigured instance reports "not configured" (got: "${noneView.engine_status}")`
  ));
  results.push(assert(
    noneView.engine_status !== faultView.engine_status,
    'A broken engine does not read as an unconfigured one on the API'
  ));

  await page.goto(unconfiguredUrl);
  await page.waitForTimeout(1500);
  const noneHidden = await page.evaluate(() =>
    getComputedStyle(document.getElementById('forgejo-section')).display === 'none');
  results.push(assert(noneHidden, 'Unconfigured instance keeps the Forgejo section hidden'));

  const noneSweepsHidden = await page.evaluate(() =>
    getComputedStyle(document.getElementById('sweeps-section')).display === 'none');
  results.push(assert(noneSweepsHidden, 'Unconfigured instance keeps the sweep section hidden'));

  // Return to the main instance for anything that follows.
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
}

// testSweepTriggering covers the manual sweep trigger: the catalog in
// the forgejo view, the one-button-per-sweep section, the confirmation
// modal (which runs nothing until confirmed), and the POST that starts
// the sweep now (§forgejo/observability/triggering-a-sweep).
async function testSweepTriggering(page, results) {
  console.log('  Sweep triggering');

  // The API: the view carries the sweep catalog with the
  // reconciliation sweep.
  const view = await (await fetch(`${baseUrl}/api/forgejo`)).json();
  const sweeps = view.sweeps ?? [];
  results.push(assert(
    sweeps.some(s => s.id === 'reconcile' && s.title),
    `The forgejo view lists the reconciliation sweep (got: ${JSON.stringify(sweeps)})`
  ));

  await page.goto(baseUrl);
  await page.waitForTimeout(1500);

  const sectionVisible = await page.locator('#sweeps-section').isVisible();
  results.push(assert(sectionVisible, 'Sweep section is visible with the engine up'));
  const buttons = await page.locator('#sweeps-section .sweep-trigger').count();
  results.push(assert(buttons >= 1, `One button per sweep operation (got: ${buttons})`));

  // The modal asks for confirmation and says why; cancelling sends
  // nothing.
  const listingsBeforeCancel = mockReposListings;
  await page.locator('#sweeps-section .sweep-trigger').first().click();
  await page.waitForSelector('#sweep-modal', { state: 'visible', timeout: 5000 });
  const modalText = await page.locator('#sweep-modal').textContent();
  results.push(assert(
    modalText?.includes('Reconciliation sweep'),
    `Modal names the sweep (got: "${modalText?.trim().slice(0, 80)}")`
  ));
  results.push(assert(
    modalText?.includes('steps'),
    'Modal asks whether the steps to avoid the button were taken'
  ));

  await page.click('#sweep-modal-cancel');
  await page.waitForTimeout(500);
  const modalHidden = await page.evaluate(() =>
    getComputedStyle(document.getElementById('sweep-modal')).display === 'none');
  results.push(assert(modalHidden, 'Cancel closes the modal'));
  results.push(assert(
    mockReposListings === listingsBeforeCancel,
    'A cancelled modal triggers no sweep'
  ));

  // Confirming starts the sweep now: the mock sees the sweep's org
  // repository listing.
  const listingsBefore = mockReposListings;
  await page.locator('#sweeps-section .sweep-trigger').first().click();
  await page.waitForSelector('#sweep-modal', { state: 'visible', timeout: 5000 });
  await page.click('#sweep-modal-confirm');
  let sweepRan = false;
  for (let i = 0; i < 40; i++) {
    if (mockReposListings > listingsBefore) { sweepRan = true; break; }
    await new Promise(res => setTimeout(res, 250));
  }
  results.push(assert(sweepRan, 'Confirming the modal starts the sweep immediately'));

  // The endpoint's refusals: unknown id 404, non-POST 405
  // (§forgejo/observability/triggering-a-sweep).
  const unknown = await fetch(`${baseUrl}/api/forgejo/sweeps/no-such-sweep`, { method: 'POST' });
  results.push(assert(unknown.status === 404, `Unknown sweep id answers 404 (got: ${unknown.status})`));
  const wrongMethod = await fetch(`${baseUrl}/api/forgejo/sweeps/reconcile`);
  results.push(assert(wrongMethod.status === 405, `GET on the trigger answers 405 (got: ${wrongMethod.status})`));
}

// ─── Main ────────────────────────────────────────────────────────────

async function main() {
  // Start the mock Forgejo API before the page tests: the maitred
  // instance under test has its Forgejo engine pointed at it.
  const mockForgejo = await startMockForgejo(forgejoPort);

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
  await testPersonaDisplay(page, results);
  await testPromptSection(page, results);
  await testForgejoTracking(page, results);
  await testSweepTriggering(page, results);
  if (faultUrl && unconfiguredUrl) {
    await testForgejoEngineStates(page, results);
  }

  await browser.close();
  mockForgejo.close();

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
