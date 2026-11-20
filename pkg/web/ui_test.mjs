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

  // The view holds focusable controls, so an unchanged poll must not
  // rebuild it: the node the operator is on, and their focus, survive
  // a refresh cycle. Nothing in the harness changes the tracked set
  // between two consecutive polls, so only the skip can decide the
  // outcome (§forgejo/observability/the-dashboard-view).
  const unchangedPoll = await page.evaluate(async () => {
    await refresh();
    const tab = document.querySelector('#forgejo-section .forgejo-tab');
    if (!tab) return { sameNode: false, keptFocus: false };
    tab.focus();
    await refresh();
    const again = document.querySelector('#forgejo-section .forgejo-tab');
    return {
      sameNode: again === tab,
      keptFocus: document.activeElement === again,
    };
  });
  results.push(assert(
    unchangedPoll.sameNode,
    'A poll whose payload has not changed does not rebuild the view'
  ));
  results.push(assert(
    unchangedPoll.keptFocus,
    'Keyboard focus on a tab button survives a refresh cycle'
  ));

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

  // The open state is module state too, and this assertion is what
  // makes the toggle listener and the reopen attributes load-bearing:
  // opening and closing a watermark cell must survive the auto-
  // refresh's re-render, exactly as the tab choice above does
  // (§forgejo/observability/the-dashboard-view).
  const openPersistence = await page.evaluate(async () => {
    const payload = {
      engine_status: 'enabled',
      sweeps: [],
      items: [
        { repo: 'a/x', kind: 'issue', number: 1, state: 'open', decision: { event: 'issues', dispatched: false, reason: 'r', at: '' } },
        { repo: 'a/x', kind: 'pr', number: 2, state: 'open', decision: { event: 'pull_request', dispatched: false, reason: 'r', at: '' } },
      ],
    };
    const cell = () => document.querySelector('#forgejo-section details.forgejo-details[data-what="watermark"]');
    renderForgejo(payload);
    cell().open = true; // opens it exactly as a click does
    await new Promise(resolve => setTimeout(resolve, 0)); // the toggle event is queued as a task
    renderForgejo(payload);
    const stillOpen = cell().open;
    cell().open = false;
    await new Promise(resolve => setTimeout(resolve, 0));
    renderForgejo(payload);
    return { stillOpen, stayedClosed: cell().open === false };
  });
  results.push(assert(
    openPersistence.stillOpen,
    'An opened watermark cell survives a re-render'
  ));
  results.push(assert(
    openPersistence.stayedClosed,
    'A cell closed again stays closed across a re-render'
  ));

  // Which state is stale, and which merely could not be read: a
  // successful render forgets the rows and repos that left tracking,
  // a render of a payload that could not be read forgets nothing.
  // Deleting the pruning loops fails the last two assertions;
  // pruning regardless of whether a payload arrived fails the first
  // (§forgejo/observability/the-dashboard-view).
  const pruning = await page.evaluate(async () => {
    const tick = () => new Promise(resolve => setTimeout(resolve, 0));
    const decision = (event) => ({ event, dispatched: false, reason: 'r', at: '' });
    const issueRow = { repo: 'a/x', kind: 'issue', number: 1, state: 'open', decision: decision('issues') };
    const prRow = { repo: 'a/x', kind: 'pr', number: 2, state: 'open', decision: decision('pull_request') };
    const departedRow = { repo: 'b/y', kind: 'pr', number: 3, state: 'open', decision: decision('pull_request') };
    const view = (items) => ({ engine_status: 'enabled', sweeps: [], items });
    const all = view([issueRow, prRow, departedRow]);
    const cell = () => document.querySelector('#forgejo-section details.forgejo-details[data-key="a/x/issue/1"][data-what="watermark"]');
    const bOnPr = () => {
      const p = document.querySelector('#forgejo-section .forgejo-panel[data-repo="b/y"][data-kind="pr"]');
      return !!p && !p.hasAttribute('hidden');
    };
    renderForgejo(all);
    cell().open = true; // opens it exactly as a click does
    document.querySelector('#forgejo-section .forgejo-tab[data-repo="b/y"][data-kind="issue"]').click();
    await tick(); // the toggle event is queued as a task
    // A poll that failed says nothing about what is tracked.
    renderForgejo(null);
    renderForgejo(all);
    const survivedFailedPoll = cell().open === true && bOnPr() === false;
    // A repo that left tracking has its state forgotten, so returning
    // is a first sight of it and not a replay of the old choice.
    renderForgejo(view([issueRow, prRow]));
    renderForgejo(all);
    const repoForgotten = bOnPr();
    // Same for a single row that left and came back.
    renderForgejo(view([prRow]));
    renderForgejo(all);
    return { survivedFailedPoll, repoForgotten, rowForgotten: cell().open === false };
  });
  results.push(assert(
    pruning.survivedFailedPoll,
    'A poll that could not be read keeps the tab choice and the opened cells'
  ));
  results.push(assert(
    pruning.repoForgotten,
    'State for a repo that left tracking is not replayed when it returns'
  ));
  results.push(assert(
    pruning.rowForgotten,
    'State for a row that left tracking is not replayed when it returns'
  ));

  // Fail then recover through refresh() itself, not a direct
  // renderForgejo call: a poll that could not be read renders the
  // empty tracked set (so an implementation that cached the failure
  // without rendering would fail the first assertion) and forgets
  // nothing; the next successful read brings the rows back with the
  // tab choice and opened cell they had before the failure. Fetch
  // is pinned in phases — failing until lifted, not consumed once —
  // so a background 5s tick can neither swallow the failure nor
  // race the recovery (§forgejo/observability/the-dashboard-view).
  const failRecover = await page.evaluate(async () => {
    const tick = () => new Promise(resolve => setTimeout(resolve, 0));
    const decision = (event) => ({ event, dispatched: false, reason: 'r', at: '' });
    const payload = {
      engine_status: 'enabled',
      sweeps: [],
      items: [
        { repo: 'a/x', kind: 'issue', number: 1, state: 'open', decision: decision('issues') },
        { repo: 'a/x', kind: 'pr', number: 2, state: 'open', decision: decision('pull_request') },
      ],
    };
    const realFetch = window.fetch;
    let failing = false;
    window.fetch = (url, opts) => String(url) !== '/api/forgejo'
      ? realFetch(url, opts)
      : failing
        ? Promise.resolve(new Response('gone', { status: 500 }))
        : Promise.resolve(new Response(JSON.stringify(payload), { status: 200, headers: { 'Content-Type': 'application/json' } }));
    try {
      await refresh(); // the pinned payload is now on screen
      const cell = () => document.querySelector('#forgejo-section details.forgejo-details[data-key="a/x/issue/1"][data-what="watermark"]');
      cell().open = true; // opens it exactly as a click does
      document.querySelector('#forgejo-section .forgejo-tab[data-repo="a/x"][data-kind="pr"]').click();
      await tick(); // the toggle event is queued as a task
      failing = true;
      await refresh(); // a failed read renders the empty tracked set
      const blankedWhileFailing = !document.querySelector('#forgejo-section .forgejo-repo-heading');
      failing = false;
      await refresh(); // the recovering read renders the rows and their state
      const pr = document.querySelector('#forgejo-section .forgejo-panel[data-repo="a/x"][data-kind="pr"]');
      return {
        blankedWhileFailing,
        tabRestored: !!pr && !pr.hasAttribute('hidden'),
        cellRestored: cell().open === true,
      };
    } finally {
      window.fetch = realFetch;
    }
  });
  results.push(assert(
    failRecover.blankedWhileFailing,
    'A failed read renders the empty tracked view rather than stale rows'
  ));
  results.push(assert(
    failRecover.tabRestored && failRecover.cellRestored,
    'After a failed poll the next read restores the rows with their tab and open state'
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

// testOutcomesPage covers the /outcomes page
// (§forgejo/observability/the-outcomes-view): the header link that
// makes the route reachable by eye, the SPA fallback serving the page
// for the route, one tall row per forest with its ideation and
// tracker links, and the three states that are not rows — the honest
// empty, the unreadable view, and a disabled engine stating itself
// in its own status words.
async function testOutcomesPage(page, results) {
  console.log('  Outcomes page');

  // The header link is how the route is found by eye.
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
  const linkHref = await page.locator('#outcomes-link').getAttribute('href');
  results.push(assert(linkHref === '/outcomes', `Header links to /outcomes (got: "${linkHref}")`));

  // The route is served: the fallback answers the path with the
  // page, which then renders the outcomes view instead of the
  // trigger dashboard.
  const routeResp = await page.goto(`${baseUrl}/outcomes`);
  results.push(assert(routeResp.status() === 200, 'GET /outcomes returns 200'));
  results.push(assert(
    routeResp.headers()['content-type']?.includes('text/html'),
    'The route answers with the page'
  ));
  results.push(assert(
    await page.locator('#outcomes-view').isVisible(),
    'The outcomes view is visible on /outcomes'
  ));
  const dashHidden = await page.evaluate(() =>
    getComputedStyle(document.getElementById('dashboard-view')).display === 'none');
  results.push(assert(dashHidden, 'The trigger dashboard is hidden on /outcomes'));
  const backHref = await page.locator('#outcomes-link').getAttribute('href');
  results.push(assert(
    backHref === '/',
    `The header's one link is the way back on /outcomes (got: "${backHref}")`
  ));
  const statsHidden = await page.evaluate(() =>
    getComputedStyle(document.querySelector('.header .stats')).display === 'none');
  results.push(assert(statsHidden, 'The trigger stats are hidden on /outcomes rather than reading as zeros'));

  // The compare-then-render discipline the dashboard follows: an
  // unchanged payload renders no new rows, so a keyboard operator's
  // focus on a row's link survives the poll
  // (§forgejo/observability/the-dashboard-view).
  const survivesPoll = await page.evaluate(async () => {
    await refreshOutcomes();
    const before = document.querySelector('#outcomes-list > *');
    await refreshOutcomes();
    return before !== null && document.querySelector('#outcomes-list > *') === before;
  });
  results.push(assert(
    survivesPoll,
    'An unchanged payload renders no new rows'
  ));

  // One tall row per forest, each carrying its ideation and tracker
  // links, exercised through the page's own renderer on a synthetic
  // payload: the mock organisation holds no outcomes repository, so
  // the live instance cannot produce a forest here
  // (§forgejo/observability/the-outcomes-view).
  const synthetic = await page.evaluate(() => {
    renderOutcomes({
      engine_status: 'enabled',
      forests: [
        {
          repository: 'o/out',
          root: { number: 5, repository: 'o/out', title: 'Ideation root', state: 'open', html_url: 'https://forge.example.com/o/out/issues/5' },
          tracker: { number: 6, repository: 'o/out', title: 'Outcome tracker', state: 'open', html_url: 'https://forge.example.com/o/out/issues/6' },
        },
        {
          repository: 'a/x',
          root: { number: 1, repository: 'a/x', title: 'Root without its gate', state: 'open', html_url: 'https://forge.example.com/a/x/issues/1' },
          tracker: null,
        },
      ],
    });
    const rows = [...document.querySelectorAll('#outcomes-list .outcomes-row')];
    return {
      rowCount: rows.length,
      firstRowLinks: [...rows[0]?.querySelectorAll('a') ?? []].map(a => a.getAttribute('href')),
      minHeight: rows.map(r => parseFloat(getComputedStyle(r).minHeight) || 0),
      texts: rows.map(r => r.textContent),
    };
  });
  results.push(assert(synthetic.rowCount === 2, `One row per forest (got: ${synthetic.rowCount})`));
  results.push(assert(
    synthetic.firstRowLinks.includes('https://forge.example.com/o/out/issues/5')
      && synthetic.firstRowLinks.includes('https://forge.example.com/o/out/issues/6'),
    `A forest row links its ideation root and its tracker (got: ${synthetic.firstRowLinks.join(', ')})`
  ));
  results.push(assert(
    synthetic.minHeight.length === 2 && synthetic.minHeight.every(h => h >= 320),
    `Rows are tall enough for the chart promised underneath (min-heights: ${synthetic.minHeight.join(', ')})`
  ));
  results.push(assert(
    synthetic.texts[0]?.includes('o/out') && synthetic.texts[0]?.includes('open'),
    `A row names its repository and the issue states (got: "${synthetic.texts[0]?.trim()}")`
  ));
  results.push(assert(
    /no tracker/i.test(synthetic.texts[1] ?? ''),
    `A tracker's absence is stated, not dropped (got: "${synthetic.texts[1]?.trim()}")`
  ));

  // A forest the payload says is waiting on a person draws its
  // warnings in the row: one ⚠️ entry per warning, naming its kind,
  // the object holding the forest up and the age of the condition,
  // each linking to the object the payload names. Both kinds render
  // distinctly enough to tell apart, and a forest with no warnings
  // carries no marker at all
  // (§forgejo/observability/the-warnings-a-forest-carries).
  const warned = await page.evaluate(() => {
    renderOutcomes({
      engine_status: 'enabled',
      forests: [
        {
          repository: 'o/out',
          root: { number: 5, repository: 'o/out', title: 'Ideation root', state: 'open', html_url: 'https://forge.example.com/o/out/issues/5' },
          tracker: { number: 6, repository: 'o/out', title: 'Outcome tracker', state: 'open', html_url: 'https://forge.example.com/o/out/issues/6' },
          warnings: [
            { kind: 'stale_agentic_merge', repository: 'o/deliv', number: 12, title: 'Delivery waiting unmerged', html_url: 'https://forge.example.com/o/deliv/pulls/12', age_seconds: 7500 },
            { kind: 'human_task_blocking', repository: 'o/out', number: 6, title: 'Claimed by a person', html_url: 'https://forge.example.com/o/out/issues/6', age_seconds: 120 },
          ],
        },
        {
          repository: 'q/quiet',
          root: { number: 1, repository: 'q/quiet', title: 'Healthy root', state: 'open', html_url: 'https://forge.example.com/q/quiet/issues/1' },
          tracker: { number: 2, repository: 'q/quiet', title: 'Healthy tracker', state: 'open', html_url: 'https://forge.example.com/q/quiet/issues/2' },
          warnings: [],
        },
      ],
    });
    const rows = [...document.querySelectorAll('#outcomes-list .outcomes-row')];
    const entries = [...(rows[0]?.querySelectorAll('.outcomes-warning') ?? [])];
    return {
      entryTexts: entries.map(e => e.textContent),
      entryHrefs: [...(rows[0]?.querySelectorAll('.outcomes-warning a') ?? [])].map(a => a.getAttribute('href')),
      quietEntries: rows[1]?.querySelectorAll('.outcomes-warning').length ?? -1,
      quietText: rows[1]?.textContent ?? '',
    };
  });
  results.push(assert(
    warned.entryTexts.length === 2,
    `One warning entry per warning of the forest (got: ${warned.entryTexts.length})`
  ));
  results.push(assert(
    warned.entryTexts.every(t => t.includes('\u26a0')),
    'Every warning entry carries the \u26a0 marker'
  ));
  results.push(assert(
    warned.entryTexts.some(t => t.includes('stale_agentic_merge') && t.includes('o/deliv#12') && t.includes('Delivery waiting unmerged'))
      && warned.entryTexts.some(t => t.includes('human_task_blocking') && t.includes('o/out#6') && t.includes('Claimed by a person')),
    `The two kinds render distinctly in the payload's own words, with their references (got: ${warned.entryTexts.join(' | ')})`
  ));
  results.push(assert(
    warned.entryTexts.some(t => t.includes('2h 5m')) && warned.entryTexts.some(t => t.includes('2m 0s')),
    `A warning entry converts the payload's seconds to a duration (7500s renders 2h 5m, 120s renders 2m 0s; got: ${warned.entryTexts.join(' | ')})`
  ));
  results.push(assert(
    warned.entryHrefs.includes('https://forge.example.com/o/deliv/pulls/12')
      && warned.entryHrefs.includes('https://forge.example.com/o/out/issues/6'),
    `Warning entries link the objects the payload names (got: ${warned.entryHrefs.join(', ') || 'none'})`
  ));
  results.push(assert(
    warned.quietEntries === 0 && !warned.quietText.includes('\u26a0'),
    `A forest with no warnings shows no marker (entries: ${warned.quietEntries})`
  ));

  // A warning whose condition the payload does not date states
  // nothing about its age rather than rendering a default as a fact:
  // the entry carries no age clause at all when age_seconds is
  // absent, and likewise no kind word when the payload names no
  // kind (§forgejo/observability/the-warnings-a-forest-carries).
  const undated = await page.evaluate(() => {
    renderOutcomes({
      engine_status: 'enabled',
      forests: [
        {
          repository: 'o/out',
          root: { number: 10, repository: 'o/out', title: 'Root', state: 'open', html_url: 'https://forge.example.com/o/out/issues/10' },
          tracker: { number: 11, repository: 'o/out', title: 'Tracker', state: 'open', html_url: 'https://forge.example.com/o/out/issues/11' },
          warnings: [
            { repository: 'o/out', number: 12, title: 'No kind, no date', html_url: 'https://forge.example.com/o/out/issues/12' },
          ],
        },
      ],
    });
    const entries = [...document.querySelectorAll('#outcomes-list .outcomes-warning')];
    return { count: entries.length, text: entries[0]?.textContent ?? '' };
  });
  results.push(assert(
    undated.count === 1 && !undated.text.includes('0s') && !undated.text.includes('\u00b7'),
    `A warning with no kind and no age states neither as a default (got: "${undated.text}")`
  ));

  // safeUrl is the guard between a forge-supplied html_url and an
  // href: anything a link cannot carries renders as text, never as a
  // clickable destination. A warning's reference enters the page the
  // same way a forest row's does — through a repository the view did
  // not previously name — so it pays the same guard
  // (§forgejo/observability/the-warnings-a-forest-carries).
  const hostile = await page.evaluate(() => {
    renderOutcomes({
      engine_status: 'enabled',
      forests: [
        {
          repository: 'o/out',
          root: { number: 7, repository: 'o/out', title: 'Executes on click', state: 'open', html_url: 'javascript:alert(1)' },
          tracker: { number: 8, repository: 'o/out', title: 'No destination at all', state: 'open', html_url: '' },
          warnings: [
            { kind: 'human_task_blocking', repository: 'o/out', number: 9, title: '<img src=x onerror=alert(1)>', html_url: 'javascript:alert(2)', age_seconds: 60 },
          ],
        },
      ],
    });
    return {
      hrefs: [...document.querySelectorAll('#outcomes-list a')].map(a => a.getAttribute('href')),
      text: document.getElementById('outcomes-list').textContent,
      markup: document.querySelectorAll('#outcomes-list img').length,
    };
  });
  results.push(assert(
    hostile.hrefs.length === 0,
    `A URL a link cannot carry never becomes an href (hrefs: ${hostile.hrefs.join(', ') || 'none'})`
  ));
  results.push(assert(
    (hostile.text ?? '').includes('Executes on click') && (hostile.text ?? '').includes('No destination at all'),
    `An unlinkable issue still renders as text (got: "${hostile.text?.trim().slice(0, 80)}")`
  ));
  results.push(assert(
    hostile.markup === 0 && (hostile.text ?? '').includes('<img src=x onerror=alert(1)>'),
    `A warning's authored title renders as text, never markup (img elements: ${hostile.markup})`
  ));

  // The honest empty and the unreadable view are different words:
  // an enabled engine that found nothing says there are no forests
  // (§forgejo/observability/the-outcomes-view).
  const empty = await page.evaluate(() => {
    renderOutcomes({ engine_status: 'enabled', forests: [] });
    return document.getElementById('outcomes-list').textContent;
  });
  results.push(assert(
    /no current forests/i.test(empty ?? ''),
    `An honest empty says so (got: "${empty?.trim().slice(0, 80)}")`
  ));

  // A view the engine answers 500 for must not read as "no forests".
  const unreadable = await page.evaluate(() => {
    renderOutcomes(null);
    return document.getElementById('outcomes-list').textContent;
  });
  results.push(assert(
    /could not be read/i.test(unreadable ?? '') && !/no current forests/i.test(unreadable ?? ''),
    `A view that cannot be vouched for says so plainly (got: "${unreadable?.trim().slice(0, 80)}")`
  ));

  // The disabled-engine instances state themselves in their own
  // status words on the page, not as an empty dashboard.
  if (faultUrl) {
    await page.goto(`${faultUrl}/outcomes`);
    await page.waitForTimeout(1500);
    const faultText = await page.locator('#outcomes-list').textContent();
    results.push(assert(
      /credentials missing/i.test(faultText ?? ''),
      `Fault instance states its category on /outcomes (got: "${faultText?.trim().slice(0, 80)}")`
    ));
  }
  if (unconfiguredUrl) {
    await page.goto(`${unconfiguredUrl}/outcomes`);
    await page.waitForTimeout(1500);
    const noneText = await page.locator('#outcomes-list').textContent();
    results.push(assert(
      /not configured/i.test(noneText ?? ''),
      `Unconfigured instance states itself on /outcomes (got: "${noneText?.trim().slice(0, 80)}")`
    ));
  }

  // Return to the main instance for anything that follows.
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
}

// testOutcomesChart covers the forest picture on the /outcomes page
// (§forgejo/observability/the-outcomes-view): each row draws the
// Mermaid source the payload carries underneath its links, with the
// region's recency stamp printed verbatim below it; a click of the
// drawing opens a full-screen overlay of the same drawing, which
// Escape or a click of its backdrop closes; and a source that will
// not parse falls back to the raw source as text rather than nothing.
// The runtime is the page's own vendored copy — the suite fails if
// the page reaches beyond the server to draw itself.
async function testOutcomesChart(page, results) {
  console.log('  Outcomes forest chart');

  await page.goto(`${baseUrl}/outcomes`);
  await page.waitForLoadState('networkidle').catch(() => {});
  // The poll's 5-second timer would otherwise answer the live
  // instance's empty forest list over a synthetic render mid-suite.
  await page.evaluate(() => { if (refreshInterval) clearInterval(refreshInterval); });

  // The page draws itself from a runtime it serves itself: the one
  // script it loads is same-origin, and that path answers with real
  // JavaScript rather than the SPA's index.html
  // (§forgejo/observability/the-outcomes-view).
  const runtime = await page.evaluate(async () => {
    const res = await fetch('/mermaid.min.js');
    return {
      cdn: [...document.scripts].filter(s => s.src).map(s => new URL(s.src).origin).filter(o => o !== location.origin),
      status: res.status,
      type: res.headers.get('content-type') ?? '',
      bytes: (await res.text()).length,
      global: typeof mermaid,
    };
  });
  results.push(assert(
    runtime.cdn.length === 0,
    `The page loads no runtime from a CDN (external scripts: ${runtime.cdn.join(', ') || 'none'})`
  ));
  results.push(assert(
    runtime.status === 200 && /javascript/.test(runtime.type) && runtime.bytes > 1000000 && runtime.global === 'object',
    `The vendored runtime is served same-origin as JavaScript (${runtime.status}, ${runtime.type}, ${runtime.bytes} bytes, global: ${runtime.global})`
  ));

  // One row drawing its picture, one stamping a source that will not
  // parse, one stating nothing: the three states the picture can
  // arrive in, exercised through the page's own renderer because the
  // mock organisation holds no outcomes repository and so cannot
  // produce a forest here. The drawn forest is what every later
  // discipline below is exercised against, so each payload names these
  // forests rather than retyping them.
  const chartSource = 'flowchart TD\n  A["Ideation root #5"] --> B["Tracker #6"]';
  const drawnForest = {
    repository: 'o/out',
    root: { number: 5, repository: 'o/out', title: 'Ideation root', state: 'open', html_url: 'https://forge.example.com/o/out/issues/5' },
    tracker: { number: 6, repository: 'o/out', title: 'Outcome tracker', state: 'open', html_url: 'https://forge.example.com/o/out/issues/6' },
    chart: chartSource,
    chart_revisited: '2026-11-16 09:12 AEDT',
  };
  const raggedForest = {
    repository: 'r/ragged',
    root: { number: 1, repository: 'r/ragged', title: 'Ragged root', state: 'open', html_url: 'https://forge.example.com/r/ragged/issues/1' },
    tracker: { number: 2, repository: 'r/ragged', title: 'Ragged tracker', state: 'open', html_url: 'https://forge.example.com/r/ragged/issues/2' },
    chart: 'flowchart TD\n  A --< this will not parse',
    chart_revisited: '2026-11-02 18:40 CET',
  };
  const emptyForest = {
    repository: 'e/empty',
    root: { number: 9, repository: 'e/empty', title: 'Root with no picture', state: 'open', html_url: 'https://forge.example.com/e/empty/issues/9' },
    tracker: { number: 10, repository: 'e/empty', title: 'Tracker with no block', state: 'open', html_url: 'https://forge.example.com/e/empty/issues/10' },
    chart: '',
    chart_revisited: '',
  };
  const oneForest = { engine_status: 'enabled', forests: [drawnForest] };
  const threeForests = { engine_status: 'enabled', forests: [drawnForest, raggedForest, emptyForest] };
  const oneRagged = { engine_status: 'enabled', forests: [raggedForest] };
  const painted = await page.evaluate((payload) => {
    renderOutcomes(payload);
    return [...document.querySelectorAll('#outcomes-list .outcomes-row')].length;
  }, threeForests);
  results.push(assert(painted === 3, `Three forest rows rendered (got: ${painted})`));

  await page.waitForFunction(() => {
    const cells = document.querySelectorAll('#outcomes-list .outcomes-chart');
    return cells.length === 2 && [...cells].every(c => c.querySelector('svg') || c.classList.contains('fallback'));
  }, null, { timeout: 30000 }).catch(() => {});

  const drawn = await page.evaluate((source) => {
    const rows = [...document.querySelectorAll('#outcomes-list .outcomes-row')];
    const cells = [...document.querySelectorAll('#outcomes-list .outcomes-chart')];
    const preOf = (row) => row.querySelector('.outcomes-chart-source');
    return {
      cellCount: cells.length,
      firstHasSvg: !!rows[0]?.querySelector('.outcomes-chart-svg svg'),
      firstSvgText: rows[0]?.querySelector('.outcomes-chart-svg svg')?.textContent ?? '',
      firstPreShown: !!preOf(rows[0]) && getComputedStyle(preOf(rows[0])).display !== 'none',
      firstSourceHeld: preOf(rows[0])?.textContent === source,
      fallbackHasSvg: !!rows[1]?.querySelector('.outcomes-chart-svg svg'),
      fallbackShown: !!preOf(rows[1]) && getComputedStyle(preOf(rows[1])).display !== 'none',
      fallbackText: preOf(rows[1])?.textContent ?? '',
      thirdHasCell: !!rows[2]?.querySelector('.outcomes-chart'),
      // A refused render must leave no error drawing of Mermaid's own
      // anywhere on the page.
      strayErrors: document.querySelectorAll('svg.error').length
        + [...document.querySelectorAll('body > div')].filter(d => /forest-chart-\d+$/.test(d.id)).length,
      stamps: rows.map(r => r.querySelector('.outcomes-stamp')?.textContent ?? ''),
      // The picture sits underneath the links and the stamp
      // underneath the picture; a cell's fallback mark is rendering
      // state, not structure, so it is dropped here.
      firstOrder: [...rows[0]?.children ?? []].map(c => c.className.split(' ')[0]),
    };
  }, chartSource);
  results.push(assert(drawn.cellCount === 2, `A row with a picture builds one chart cell (cells: ${drawn.cellCount} of 3 rows)`));
  results.push(assert(
    drawn.firstHasSvg && drawn.firstSourceHeld && !drawn.firstPreShown,
    `A parsable source draws as a graph (svg: ${drawn.firstHasSvg}, verbatim source: ${drawn.firstSourceHeld})`
  ));
  results.push(assert(
    /Tracker #6/.test(drawn.firstSvgText),
    `The drawing carries the source's own labels (svg text: "${drawn.firstSvgText.trim().slice(0, 60)}")`
  ));
  results.push(assert(
    !drawn.fallbackHasSvg && drawn.fallbackShown,
    `An unparsable source falls back to text rather than nothing (svg: ${drawn.fallbackHasSvg}, text shown: ${drawn.fallbackShown})`
  ));
  results.push(assert(
    drawn.fallbackText.includes('this will not parse'),
    `The fallback states the source verbatim (got: "${drawn.fallbackText.trim().slice(0, 60)}")`
  ));
  results.push(assert(
    !drawn.thirdHasCell,
    'A row carrying no picture builds no chart cell'
  ));
  results.push(assert(
    drawn.strayErrors === 0,
    `A refused drawing leaves no error bomb on the page (strays: ${drawn.strayErrors})`
  ));
  results.push(assert(
    drawn.stamps[0] === '2026-11-16 09:12 AEDT' && drawn.stamps[1] === '2026-11-02 18:40 CET' && drawn.stamps[2] === '',
    `The recency stamp prints verbatim beneath the picture (got: ${JSON.stringify(drawn.stamps)})`
  ));
  results.push(assert(
    drawn.firstOrder.join('>') === 'outcomes-repo>trigger-meta>outcomes-chart>outcomes-stamp',
    `The row is repository, links, picture, stamp (got: ${drawn.firstOrder.join(' > ')})`
  ));

  // The source is text a stranger could have written, so it reaches
  // the page as escaped text and never as markup.
  const escaped = await page.evaluate(() => chartHtml('<img src=x onerror="alert(1)">'));
  const injected = await page.evaluate(() => {
    renderOutcomes({
      engine_status: 'enabled',
      forests: [{
        repository: 'x/inject',
        root: { number: 1, repository: 'x/inject', title: 'Root', state: 'open', html_url: 'https://forge.example.com/x/inject/issues/1' },
        tracker: null,
        chart: '<img src=x onerror="alert(1)">',
        chart_revisited: '<b>just now</b>',
      }],
    });
    return {
      imgs: document.querySelectorAll('#outcomes-list img').length,
      bolds: document.querySelectorAll('#outcomes-list .outcomes-stamp b').length,
      text: document.querySelector('#outcomes-list .outcomes-chart-source')?.textContent ?? '',
    };
  });
  results.push(assert(
    escaped.includes('&lt;img') && !escaped.includes('<img'),
    `A source carrying markup is escaped (got: "${escaped.slice(0, 60)}")`
  ));
  results.push(assert(
    injected.imgs === 0 && injected.bolds === 0 && injected.text.includes('<img'),
    `A hostile source and stamp render as text, never as markup (imgs: ${injected.imgs}, bolds: ${injected.bolds})`
  ));

  // The click-out: a click of the drawing opens a full-screen copy of
  // it, Escape closes it, and a click of the backdrop closes it too.
  await page.evaluate((payload) => renderOutcomes(payload), oneForest);
  await page.waitForSelector('#outcomes-list .outcomes-chart svg', { timeout: 30000 }).catch(() => {});

  const overlay = page.locator('#chart-overlay');
  results.push(assert(
    !(await overlay.isVisible()),
    'No overlay is open before the click'
  ));
  await page.locator('#outcomes-list .outcomes-chart svg').first().click();
  await overlay.waitFor({ state: 'visible', timeout: 5000 }).catch(() => {});
  results.push(assert(await overlay.isVisible(), 'Clicking the drawing opens the full-screen overlay'));
  const enlarged = await page.evaluate(() => ({
    svg: !!document.querySelector('#chart-overlay-body svg'),
    covering: (() => {
      const box = document.getElementById('chart-overlay').getBoundingClientRect();
      return box.width >= window.innerWidth && box.height >= window.innerHeight;
    })(),
  }));
  results.push(assert(
    enlarged.svg && enlarged.covering,
    `The overlay shows the same drawing full screen (svg: ${enlarged.svg}, covering the viewport: ${enlarged.covering})`
  ));

  await page.keyboard.press('Escape');
  await page.waitForTimeout(200);
  results.push(assert(!(await overlay.isVisible()), 'Escape closes the overlay'));

  await page.locator('#outcomes-list .outcomes-chart').first().click();
  await overlay.waitFor({ state: 'visible', timeout: 5000 }).catch(() => {});
  await page.mouse.click(4, 4);  await page.waitForTimeout(200);
  results.push(assert(!(await overlay.isVisible()), 'A click outside the drawing closes the overlay'));

  // A drawing whose source will not parse still clicks out, to the
  // same text the row states.
  await page.evaluate((payload) => renderOutcomes(payload), oneRagged);
  await page.waitForSelector('#outcomes-list .outcomes-chart.fallback .outcomes-chart-source', { timeout: 30000 }).catch(() => {});
  await page.locator('#outcomes-list .outcomes-chart.fallback').first().click();
  await overlay.waitFor({ state: 'visible', timeout: 5000 }).catch(() => {});
  const enlargedText = await page.evaluate(() => ({
    text: document.querySelector('#chart-overlay-body')?.textContent ?? '',
    svg: !!document.querySelector('#chart-overlay-body svg'),
  }));
  results.push(assert(
    !enlargedText.svg && enlargedText.text.includes('this will not parse'),
    `The overlay of a fallen-back picture states its text (svg: ${enlargedText.svg})`
  ));
  await page.keyboard.press('Escape');
  await page.waitForTimeout(200);

  // The poll's discipline: every render replaces the list, and the
  // two branches that render without rows — the honest empty and the
  // payload the engine could not vouch for — must close an open
  // overlay rather than leave the reader inside a picture the poll
  // has replaced (§forgejo/observability/the-outcomes-view).
  const closed = [];
  for (const replaceWith of [{ engine_status: 'enabled', forests: [] }, null]) {
    await page.evaluate((p) => renderOutcomes(p), oneForest);
    await page.waitForSelector('#outcomes-list .outcomes-chart svg', { timeout: 30000 }).catch(() => {});
    await page.locator('#outcomes-list .outcomes-chart').first().click();
    await overlay.waitFor({ state: 'visible', timeout: 5000 }).catch(() => {});
    await page.evaluate((p) => renderOutcomes(p), replaceWith);
    closed.push(!(await overlay.isVisible()));
  }
  results.push(assert(
    closed.every(Boolean),
    `A re-render closes the overlay whichever branch replaces the list (empty: ${closed[0]}, unreadable: ${closed[1]})`
  ));

  // A page whose runtime never loaded keeps every picture's text:
  // the fallback the brief demands, not a row of empty boxes.
  await page.evaluate((p) => {
    window.__mermaid = window.mermaid;
    window.mermaid = undefined;
    renderOutcomes(p);
  }, oneForest);
  await page.waitForFunction(() => {
    const cell = document.querySelector('#outcomes-list .outcomes-chart');
    return cell && (cell.classList.contains('fallback') || cell.querySelector('svg'));
  }, null, { timeout: 10000 }).catch(() => {});
  const noRuntime = await page.evaluate(() => {
    const cell = document.querySelector('#outcomes-list .outcomes-chart');
    const pre = cell?.querySelector('.outcomes-chart-source');
    return {
      fallback: !!cell && cell.classList.contains('fallback'),
      shown: !!pre && getComputedStyle(pre).display !== 'none',
    };
  });
  results.push(assert(
    noRuntime.fallback && noRuntime.shown,
    `A page whose runtime never loaded keeps each picture's text (fallback: ${noRuntime.fallback}, shown: ${noRuntime.shown})`
  ));
  await page.evaluate(() => { window.mermaid = window.__mermaid; });

  // A click of a cell that is still drawing opens the overlay once
  // the draw settles, never an empty one.
  await page.evaluate((p) => renderOutcomes(p), oneForest);
  await page.locator('#outcomes-list .outcomes-chart').first().click({ timeout: 5000 }).catch(() => {});
  await overlay.waitFor({ state: 'visible', timeout: 5000 }).catch(() => {});
  const early = await page.evaluate(() => ({
    svg: !!document.querySelector('#chart-overlay-body svg'),
    text: (document.querySelector('#chart-overlay-body')?.textContent ?? '').length > 0,
  }));
  results.push(assert(
    early.svg || early.text,
    `A click during a draw opens an overlay that fills in, never an empty one (svg: ${early.svg}, text: ${early.text})`
  ));
  await page.keyboard.press('Escape');
  await page.waitForTimeout(200);

  // The Escape half of the pair needs an open half a keyboard
  // operator can reach: the focused picture answers Enter.
  await page.locator('#outcomes-list .outcomes-chart').first().focus().catch(() => {});
  await page.keyboard.press('Enter');
  await overlay.waitFor({ state: 'visible', timeout: 5000 }).catch(() => {});
  results.push(assert(
    await overlay.isVisible(),
    'Enter on the focused picture opens the overlay'
  ));
  await page.keyboard.press('Escape');
  await page.waitForTimeout(200);
  results.push(assert(
    !(await overlay.isVisible()),
    'Escape closes what the keyboard opened'
  ));

  // The `strict` security level is the sandbox the spec promises for
  // source a stranger wrote, and the promise must survive a source
  // that tries to take it back down: a parsable source carrying an
  // init directive that sets securityLevel and a click to a
  // javascript: URL still draws — and draws inert. Without this
  // assertion a runtime bump or an options edit could retire the
  // guard with the suite still green.
  const downgrade = '%%{init: {"securityLevel": "loose"} }%%\nflowchart TD\n  A["Root #5"] --> B["Tracker #6"]\n  click A "javascript:window.__pwned = 1"';
  await page.evaluate((p) => {
    window.__pwned = 0;
    renderOutcomes(p);
  }, {
    engine_status: 'enabled',
    forests: [{
      repository: 'd/downgrade',
      root: { number: 5, repository: 'd/downgrade', title: 'Root', state: 'open', html_url: 'https://forge.example.com/d/downgrade/issues/5' },
      tracker: null,
      chart: downgrade,
      chart_revisited: '',
    }],
  });
  await page.waitForSelector('#outcomes-list .outcomes-chart svg', { timeout: 30000 }).catch(() => {});
  await page.locator('#outcomes-list .outcomes-chart svg .node').first().click({ force: true }).catch(() => {});
  await page.waitForTimeout(200);
  const inert = await page.evaluate(() => ({
    pwned: window.__pwned === 1,
    svg: !!document.querySelector('#outcomes-list .outcomes-chart svg'),
    fallback: !!document.querySelector('#outcomes-list .outcomes-chart.fallback'),
  }));
  results.push(assert(
    !inert.pwned && inert.svg && !inert.fallback,
    `A source that tries to downgrade strict draws, inert (script ran: ${inert.pwned}, drawn: ${inert.svg}, fell back: ${inert.fallback})`
  ));
  await page.keyboard.press('Escape');
  await page.waitForTimeout(200);

  // Return to the main instance for anything that follows.
  await page.goto(baseUrl);
  await page.waitForLoadState('networkidle').catch(() => {});
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
  await testOutcomesPage(page, results);
  await testOutcomesChart(page, results);
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
