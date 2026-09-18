import { render, screen } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { beforeAll, describe, expect, it } from 'vitest';

import { IdText } from '@/components/CopyButton';
import { JsonView } from '@/components/JsonView';
import { TooltipProvider } from '@/components/ui/tooltip';
import enCommon from '@/i18n/locales/en/common.json';

import { DetailGrid, type DetailItem } from './DetailGrid';

/** A real 40-character identifier: one unbreakable "word" with no spaces. */
const LONG_ID = 'idt_01a0b0ff38247591ab1f2e5350fa5bb7';
const LONG_URI = '/api/v3/catalog/items?category=shoes&page=42&sort=price_desc&cursor=eyJvZmZzZXQiOjEyMDB9';

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

function renderGrid(items: readonly DetailItem[]) {
  return render(
    <I18nextProvider i18n={i18next}>
      <TooltipProvider>
        <DetailGrid items={items} />
      </TooltipProvider>
    </I18nextProvider>,
  );
}

/** The <div> wrapping one dt/dd pair. */
function row(label: string): HTMLElement {
  const term = screen.getByText(label);
  const wrapper = term.parentElement;
  if (!wrapper) throw new Error(`no row for ${label}`);
  return wrapper;
}

function valueCell(label: string): HTMLElement {
  const cell = row(label).querySelector('dd');
  if (!cell) throw new Error(`no value for ${label}`);
  return cell;
}

describe('DetailGrid', () => {
  it('pairs every label with a value cell that may shrink and wrap', () => {
    renderGrid([
      { key: 'id', label: 'Report ID', value: <IdText value={LONG_ID} /> },
      { key: 'uri', label: 'Request', wide: true, value: LONG_URI },
    ]);

    for (const label of ['Report ID', 'Request']) {
      const cell = valueCell(label);
      // `min-w-0` lets the grid track shrink; `wrap-anywhere` lets an
      // unbreakable value wrap inside it instead of painting over the next row.
      expect(cell).toHaveClass('min-w-0', 'wrap-anywhere');
    }
    // Only non-wide rows get the fixed label column; a wide row keeps one column.
    expect(row('Report ID').className).toContain('sm:grid-cols-[minmax(0,9rem)_minmax(0,1fr)]');
    expect(row('Request').className).not.toContain('sm:grid-cols-');
  });

  it('renders a long identifier in full, with a copy button and no truncation', () => {
    renderGrid([{ key: 'id', label: 'Report ID', value: <IdText value={LONG_ID} /> }]);

    const text = screen.getByText(LONG_ID);
    expect(text).toBeInTheDocument();
    // Truncation is opt-in (tables); detail panels show and wrap the whole value.
    expect(text).not.toHaveClass('truncate');
    expect(text).toHaveClass('wrap-anywhere');
    expect(text.closest('span[title]')).toHaveAttribute('title', LONG_ID);
    expect(screen.getByRole('button', { name: 'Copy' })).toBeInTheDocument();
  });

  it('truncates only when the caller asks for it', () => {
    renderGrid([{ key: 'id', label: 'Report ID', value: <IdText value={LONG_ID} truncate={12} /> }]);

    const text = screen.getByText(`${LONG_ID.slice(0, 12)}…`);
    expect(text).toHaveClass('truncate');
    expect(screen.queryByText(LONG_ID)).not.toBeInTheDocument();
  });

  it('renders a long URI without a nowrap or truncating value cell', () => {
    renderGrid([{ key: 'uri', label: 'Request', wide: true, value: LONG_URI }]);

    const cell = valueCell('Request');
    expect(cell).toHaveTextContent(LONG_URI);
    expect(cell.className).not.toContain('truncate');
    expect(cell.className).not.toContain('whitespace-nowrap');
  });

  it('renders an empty value as a dash with no copy button', () => {
    renderGrid([{ key: 'lease', label: 'Lease ID', value: <IdText value="" /> }]);

    expect(valueCell('Lease ID')).toHaveTextContent('—');
    expect(screen.queryByRole('button', { name: 'Copy' })).not.toBeInTheDocument();
  });

  it('renders a JSON value in a block that scrolls within its own max height', () => {
    renderGrid([
      {
        key: 'details',
        label: 'Details',
        wide: true,
        value: <JsonView value={{ reason: 'rate-limited', retryAfter: 30 }} maxHeight="12rem" />,
      },
    ]);

    const cell = valueCell('Details');
    expect(cell).toHaveTextContent('"reason"');
    const scroller = cell.querySelector<HTMLElement>('div > div');
    expect(scroller).not.toBeNull();
    expect(scroller?.style.maxHeight).toBe('12rem');
    expect(scroller?.className).toContain('overflow-auto');
    // The copy button sits outside the scroller so it stays reachable.
    const copy = screen.getByRole('button', { name: 'Copy' });
    expect(scroller?.contains(copy)).toBe(false);
  });
});
