import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Suspense } from 'react';
import { beforeEach, describe, expect, it } from 'vitest';

import '@/i18n';

import { PageIntro } from './PageIntro';

/** Storage key contract: one entry per page id. */
const key = (page: string) => `spinneret.intro.${page}`;

/** Renders the card for a page id (without router links) and waits for its i18n namespace. */
async function renderIntro(page = 'proxies'): Promise<HTMLElement> {
  render(
    <Suspense fallback={<div>loading</div>}>
      <PageIntro page={page} />
    </Suspense>,
  );
  return screen.findByRole('button');
}

/** The body element the toggle controls. */
function body(toggle: HTMLElement): HTMLElement | null {
  return document.getElementById(toggle.getAttribute('aria-controls') ?? '');
}

describe('PageIntro', () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it('is expanded by default and exposes the toggle to assistive technology', async () => {
    const toggle = await renderIntro();
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(body(toggle)).not.toBeNull();
    expect(body(toggle)).not.toHaveAttribute('hidden');
  });

  it('collapses on click and persists the choice', async () => {
    const user = userEvent.setup();
    const toggle = await renderIntro();

    await user.click(toggle);

    expect(toggle).toHaveAttribute('aria-expanded', 'false');
    expect(body(toggle)).toHaveAttribute('hidden');
    expect(window.localStorage.getItem(key('proxies'))).toBe('0');
  });

  it('restores the collapsed state on the next mount and expands again', async () => {
    const user = userEvent.setup();
    window.localStorage.setItem(key('proxies'), '0');
    const toggle = await renderIntro();
    expect(toggle).toHaveAttribute('aria-expanded', 'false');

    await user.click(toggle);

    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(window.localStorage.getItem(key('proxies'))).toBe('1');
  });

  it('keeps the state of other pages separate', async () => {
    const user = userEvent.setup();
    window.localStorage.setItem(key('secrets'), '0');
    const toggle = await renderIntro('proxies');
    expect(toggle).toHaveAttribute('aria-expanded', 'true');

    await user.click(toggle);

    expect(window.localStorage.getItem(key('secrets'))).toBe('0');
    expect(window.localStorage.getItem(key('proxies'))).toBe('0');
  });

  it('renders the summary, the bullets and the "when you need this" line', async () => {
    await renderIntro();
    expect(screen.getByText(/outbound proxy pool of the namespace/i)).toBeInTheDocument();
    expect(screen.getAllByRole('listitem')).toHaveLength(3);
    expect(screen.getByText(/exit nodes start returning captchas/i)).toBeInTheDocument();
  });
});
