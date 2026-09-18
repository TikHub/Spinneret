import { expect, type Locator, type Page } from '@playwright/test';

/** All toasts currently on screen (sonner renders one list item per toast). */
export function toasts(page: Page): Locator {
  return page.locator('li[data-sonner-toast]');
}

/** Waits for a toast whose text matches and dismisses every visible toast. */
export async function expectToast(page: Page, text: string | RegExp): Promise<void> {
  const toast = toasts(page).filter({ hasText: text }).first();
  await expect(toast).toBeVisible();
  await dismissToasts(page);
}

/** Closes open toasts so they cannot cover controls of the next step. */
export async function dismissToasts(page: Page): Promise<void> {
  const closeButtons = page.locator('li[data-sonner-toast] button[data-close-button]');
  for (let i = await closeButtons.count(); i > 0; i -= 1) {
    const button = closeButtons.first();
    if (!(await button.isVisible().catch(() => false))) break;
    await button.click({ timeout: 5_000 }).catch(() => undefined);
  }
  await expect(toasts(page)).toHaveCount(0, { timeout: 10_000 });
}

/** Opens a console page and waits for its level-1 heading. */
export async function gotoPage(page: Page, path: string, heading: string | RegExp): Promise<void> {
  await page.goto(path);
  await expect(page.getByRole('heading', { level: 1, name: heading })).toBeVisible();
}

/** The open modal dialog (Radix renders one at a time). */
export function dialog(page: Page): Locator {
  return page.getByRole('dialog');
}

/** Picks an option of a Radix select identified by its accessible name. */
export async function selectOption(scope: Page | Locator, name: string | RegExp, option: string | RegExp) {
  const page = 'page' in scope ? (scope as Locator).page() : (scope as Page);
  await scope.getByRole('combobox', { name }).click();
  await page.getByRole('option', { name: option }).click();
}

/** Monaco root element of the editor with this accessible name. */
function editorRoot(page: Page, name: string | RegExp): Locator {
  return page
    .getByRole('textbox', { name })
    .locator('xpath=ancestor::div[contains(@class,"monaco-editor")][1]')
    .first();
}

/**
 * Text of the rendered lines of the Monaco editor with this accessible name.
 * Monaco only renders the visible viewport, so this is the visible text, not the
 * whole document.
 */
export async function editorText(page: Page, name: string | RegExp): Promise<string> {
  return (await editorRoot(page, name).locator('.view-lines').innerText()).replace(/\u00a0/g, ' ');
}

/**
 * Pastes text over the current selection of a Monaco editor.
 *
 * A real paste is required: typing or inserting text would run Monaco's
 * auto-indent and bracket completion over it. Meta+V is not delivered to the
 * page by Chromium on macOS, so the paste event is dispatched into the editor's
 * input element directly.
 */
async function pasteIntoEditor(root: Locator, text: string): Promise<void> {
  await root.evaluate((node, value) => {
    const data = new DataTransfer();
    data.setData('text/plain', value);
    const input = node.querySelector('.native-edit-context, textarea') ?? node;
    input.dispatchEvent(
      new ClipboardEvent('paste', { clipboardData: data, bubbles: true, cancelable: true }),
    );
  }, text);
}

/**
 * Replaces the whole content of the Monaco editor with this accessible name.
 * Everything is selected by clicking the first character and shift-clicking the
 * last line. Monaco only renders the visible viewport, so this is meant for
 * documents that fit on screen; use `replaceInEditor` for longer ones.
 */
export async function fillEditor(page: Page, name: string | RegExp, value: string): Promise<void> {
  const root = editorRoot(page, name);
  const lines = root.locator('.view-lines');
  await expect(lines).toBeVisible();
  const box = await lines.boundingBox();
  if (!box) throw new Error(`editor "${String(name)}" has no layout`);
  await lines.click({ position: { x: 1, y: 1 } });
  // The right edge of .view-lines sits under Monaco's overview ruler and is not
  // clickable, so the last line is selected from its start and extended with End.
  await lines.click({ position: { x: 2, y: Math.max(1, box.height - 2) }, modifiers: ['Shift'] });
  await page.keyboard.press('Shift+End');
  await pasteIntoEditor(root, value);
  const lastLine = value.trimEnd().split('\n').at(-1)!.trim();
  await expect.poll(() => editorText(page, name)).toContain(lastLine);
}

/**
 * Replaces one token of a Monaco editor: the line is found by its text, the
 * token is double-clicked (which selects that word) and the replacement pasted.
 */
export async function replaceInEditor(
  page: Page,
  name: string | RegExp,
  line: string | RegExp,
  token: string,
  replacement: string,
): Promise<void> {
  const root = editorRoot(page, name);
  const target = root
    .locator('.view-line')
    .filter({ hasText: line })
    .first()
    .getByText(token, { exact: true });
  await target.dblclick();
  await pasteIntoEditor(root, replacement);
  await expect.poll(() => editorText(page, name)).toContain(replacement);
}
