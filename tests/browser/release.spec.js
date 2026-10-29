const { test, expect } = require('@playwright/test');

test('offline setup, query paths, final streaming text, approval, cancellation, and reload', async ({ page }) => {
  const errors = [];
  page.on('pageerror', (error) => errors.push(error.message));
  // Reproduce a background tab: a short answer finishes before any paint callback.
  await page.addInitScript(() => { window.requestAnimationFrame = () => 1; });
  await page.goto('/?token=browser-test');
  await expect(page.locator('#workspace')).toContainText('project & space');
  await expect(page.locator('.welcome')).toBeVisible();

  await page.locator('#open-settings').click();
  await expect(page.locator('#settings-view')).toBeVisible();
  await expect(page.locator('#models-hint')).toContainText('Ollama not reachable');
  await page.locator('#set-or-key').fill('fixture-key');
  await page.locator('#set-or-save').click();
  await expect(page.locator('#set-or-hint')).toContainText('A key is configured');
  await page.locator('#set-provider').selectOption('openrouter');
  await expect(page.locator('#set-model option[value="fixture"]')).toHaveCount(1);
  await page.locator('#set-model').selectOption('fixture');
  await page.locator('#close-settings').click();
  await expect(page.locator('#model')).toHaveValue('fixture');

  await page.locator('[data-tab="files"]').click();
  await page.locator('#file-list .item').filter({ hasText: 'nested & files' }).click();
  await page.locator('#file-list .item').filter({ hasText: 'sample.txt' }).click();
  await expect(page.locator('#viewer')).toContainText('preview works');
  await page.locator('#viewer-close').click();

  await page.locator('#input').fill('hello');
  await page.locator('#send').click();
  await expect(page.locator('#transcript .assistant')).toHaveText('small answer');
  await expect(page.locator('#send')).toBeEnabled();
  await expect(page.locator('#statustext')).toHaveText('ready');
  await expect(page.locator('.welcome')).toBeHidden();

  await page.locator('#input').fill('write sample');
  await page.locator('#send').click();
  await expect(page.locator('#approval-dock .approval')).toContainText('write');
  await expect(page.locator('#transcript .approval')).toHaveCount(0);
  await page.locator('.approval button.allow').click();
  await expect(page.locator('#transcript .assistant')).toHaveCount(2);
  await expect(page.locator('#send')).toBeEnabled();
  await expect(page.locator('#statustext')).toHaveText('ready');
  await expect(page.locator('.approval')).toHaveCount(0);

  const tool = page.locator('.tool-head').last();
  await tool.focus();
  await tool.press('Enter');
  await expect(tool).toHaveAttribute('aria-expanded', 'true');
  await expect(page.locator('.tool-body').last()).toBeVisible();
  await tool.press('Space');
  await expect(tool).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('.tool-body').last()).toBeHidden();

  await page.locator('#input').fill('fail command');
  await page.locator('#send').click();
  await page.locator('.approval button.allow:not(:disabled)').click();
  await expect(page.locator('.tool').last()).toHaveClass(/fail/);
  await expect(page.locator('.tool-ms').last()).toContainText('failed');
  await expect(page.locator('#transcript .assistant')).toHaveCount(3);
  await expect(page.locator('#send')).toBeEnabled();

  await page.locator('#input').fill('wait');
  await page.locator('#send').click();
  await expect(page.locator('#stop')).toBeVisible();
  await page.locator('#stop').click();
  await expect(page.locator('#send')).toBeEnabled();
  await expect(page.locator('#transcript')).toContainText('cancelled');

  await page.reload();
  await expect(page.locator('#transcript .assistant')).toHaveCount(3);
  await expect(page.locator('#transcript .assistant').first()).toHaveText('small answer');
  await expect(page.locator('#left')).toContainText('browser test thread');
  await expect(page.locator('#send')).toBeEnabled();
  await page.locator('.tool-head').last().press('Enter');
  await expect(page.locator('.tool-body').last()).toBeVisible();
  await expect(page.locator('.tool').last()).toHaveClass(/fail/);
  await expect(page.locator('.tool-ms').last()).toHaveText('failed');
  expect(errors).toEqual([]);
});

test('compact windows keep the composer usable and collapsed panes out of keyboard navigation', async ({ page }) => {
  await page.setViewportSize({ width: 1180, height: 820 });
  await page.goto('/?token=browser-test');
  await expect(page.locator('#workspace')).toContainText('project & space');

  for (const width of [1180, 620, 390]) {
    await page.setViewportSize({ width, height: 720 });
    await expect(page.locator('#send')).toBeInViewport();
    await expect(page.locator('#input')).toBeInViewport();
    await expect(page.locator('#approval')).toBeInViewport();
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
  await expect(page.locator('#left')).toHaveJSProperty('inert', true);
  await expect(page.locator('#right')).toHaveJSProperty('inert', true);
  await page.locator('#toggle-left-mini').click();
  await expect(page.locator('#left')).toHaveJSProperty('inert', false);
  await page.locator('#open-settings').click();
  await page.locator('#toggle-left').click();
  await expect(page.locator('#settings-view')).toBeVisible();
  await expect(page.locator('#set-or-save')).toBeInViewport();
  await expect(page.locator('#toggle-left-mini')).toBeFocused();
  await page.locator('#close-settings').click();
  await expect(page.getByRole('textbox', { name: 'Message Cody' })).toBeFocused();
  await page.reload();
  await expect(page.locator('#left')).toHaveJSProperty('inert', true);
  await expect(page.locator('#send')).toBeInViewport();
});

test('compact approval dock retains details, survives reload, and supports denial, retry, and cancellation', async ({ page }) => {
  await page.goto('/?token=browser-test');
  await page.locator('#open-settings').click();
  await page.locator('#set-or-key').fill('fixture-key');
  await page.locator('#set-or-save').click();
  await expect(page.locator('#set-or-hint')).toContainText('A key is configured');
  await page.locator('#set-provider').selectOption('openrouter');
  await page.locator('#set-model').selectOption('fixture');
  await page.locator('#close-settings').click();
  await page.locator('#input').fill('write sample');
  await page.locator('#send').click();
  const dock = page.locator('#approval-dock');
  const card = dock.locator('.approval');
  await expect(card).toHaveCount(1);
  for (const width of [1180, 620, 390]) {
    await page.setViewportSize({ width, height: 720 });
    await expect(card.getByRole('button', { name: 'Approve', exact: true })).toBeInViewport();
    await expect(card.getByRole('button', { name: 'Deny', exact: true })).toBeInViewport();
    await expect(page.locator('#input')).toBeInViewport();
    const box = await card.boundingBox(), composer = await page.locator('.composer').boundingBox();
    expect(box.height).toBeLessThanOrEqual(48);
    expect(box.y + box.height).toBeLessThanOrEqual(composer.y);
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  }
  const review = card.getByRole('button', { name: 'Review write request' });
  await review.press('Enter');
  await expect(review).toHaveAttribute('aria-expanded', 'true');
  await expect(card.locator('pre')).toContainText('"content": "approved write"');
  await page.reload();
  await expect(card).toHaveCount(1);
  await expect(page.locator('#transcript .approval')).toHaveCount(0);

  await page.route('**/api/approve', route => route.fulfill({ status: 503, body: 'temporary test failure' }), { times: 1 });
  await card.getByRole('button', { name: 'Deny', exact: true }).click();
  await expect(card.getByRole('alert')).toContainText('Could not send your decision.');
  await expect(card.getByRole('button', { name: 'Deny', exact: true })).toBeEnabled();
  await card.getByRole('button', { name: 'Deny', exact: true }).click();
  await expect(card).toHaveCount(0);
  await expect(page.locator('#send')).toBeEnabled();
  await page.locator('.tool-head').last().click();
  await expect(page.locator('.tool-body').last()).toContainText('user denied');

  await page.locator('#input').fill('write sample');
  await page.locator('#send').click();
  await expect(card).toHaveCount(1);
  await page.locator('#stop').click();
  await expect(card).toHaveCount(0);
  await expect(page.locator('#send')).toBeEnabled();
});


test('web and browser opt-ins stay independent and search keys never return to the page', async ({ page }) => {
  await page.goto('/?token=browser-test');
  await page.locator('#open-settings').click();
  const web = page.locator('#set-web-enabled'), browser = page.locator('#set-browser-enabled');
  await expect(web).not.toBeChecked();
  await expect(browser).not.toBeChecked();
  await web.check();
  await expect(browser).not.toBeChecked();
  await page.locator('#set-search-key').fill('test-brave-secret');
  await page.locator('#set-search-save').click();
  await expect(page.locator('#set-web-hint')).toContainText('Search key configured');
  await expect(page.locator('#set-search-key')).toHaveValue('');
  await page.reload();
  await page.locator('#open-settings').click();
  await expect(web).toBeChecked();
  await expect(browser).not.toBeChecked();
  const response = await page.request.get('/api/settings', { headers: { 'X-Cody-Token': 'browser-test' } });
  expect(await response.text()).not.toContain('test-brave-secret');
  await Promise.all([
    page.waitForResponse(r => r.url().endsWith('/api/settings') && r.request().method() === 'POST'),
    browser.check(),
  ]);
  await Promise.all([
    page.waitForResponse(r => r.url().endsWith('/api/settings') && r.request().method() === 'POST'),
    web.uncheck(),
  ]);
  await page.reload();
  await page.locator('#open-settings').click();
  await expect(web).not.toBeChecked();
  await expect(browser).toBeChecked();
  await browser.uncheck();
  await page.locator('#set-search-clear').click();
  await expect(page.locator('#set-web-hint')).toContainText('Add a search key');
});

test('plan gate shows the plan before anything changes and blocks unplanned changes', async ({ page }) => {
  await page.goto('/?token=browser-test');
  await page.locator('#open-settings').click();
  const gate = page.locator('#set-plan');
  await Promise.all([
    page.waitForResponse(r => r.url().endsWith('/api/settings') && r.request().method() === 'POST'),
    gate.check(),
  ]);
  await page.locator('#close-settings').click();

  await page.locator('#input').fill('plan sample');
  await page.locator('#send').click();
  const card = page.locator('#approval-dock .approval');
  await expect(card).toContainText('plan');
  await expect(card.locator('pre')).toBeVisible();
  await expect(card.locator('pre')).toContainText('1. inspect the sample');
  await expect(card.locator('pre')).toContainText('2. update the sample');
  await card.locator('button.allow').click();
  await expect(page.locator('#transcript .assistant').last()).toHaveText('small answer');

  // A new request with a change but no plan is refused without asking.
  const answers = await page.locator('#transcript .assistant').count();
  await page.locator('#input').fill('write sample');
  await page.locator('#send').click();
  await expect(page.locator('#transcript .assistant')).toHaveCount(answers + 1);
  await expect(page.locator('#approval-dock .approval')).toHaveCount(0);
  await expect(page.locator('#statustext')).toHaveText('ready');

  await page.locator('#open-settings').click();
  await gate.uncheck();
});
