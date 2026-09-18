import { act, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import '@/i18n';

import { FilterBar, SearchInput } from './FilterBar';

const DEBOUNCE_MS = 300;

interface Filters {
  q: string;
  owner: string;
  state: string;
}

const INITIAL: Filters = { q: '', owner: '', state: 'active' };

/** Page-like harness: a search box in `search`, one in children and a non-search filter. */
function Harness({ onFilters }: { onFilters: (filters: Filters) => void }) {
  const [filters, setFilters] = useState<Filters>(INITIAL);
  const update = (patch: Partial<Filters>) => {
    setFilters((prev) => {
      const next = { ...prev, ...patch };
      onFilters(next);
      return next;
    });
  };
  const activeCount = [filters.q, filters.owner, filters.state].filter((v) => v !== '').length;
  return (
    <FilterBar
      search={{ value: filters.q, onChange: (q) => update({ q }), placeholder: 'Search name' }}
      activeCount={activeCount}
      onReset={() => update({ q: '', owner: '', state: '' })}
    >
      <SearchInput value={filters.owner} onChange={(owner) => update({ owner })} placeholder="Owner" />
    </FilterBar>
  );
}

describe('FilterBar', () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('applies a typed search after the debounce', () => {
    const onFilters = vi.fn();
    render(<Harness onFilters={onFilters} />);
    fireEvent.change(screen.getByPlaceholderText('Search name'), { target: { value: 'alice' } });
    expect(onFilters).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(DEBOUNCE_MS));
    expect(onFilters).toHaveBeenLastCalledWith({ ...INITIAL, q: 'alice' });
  });

  it('drops search drafts typed within the debounce window when filters are reset', () => {
    const onFilters = vi.fn();
    render(<Harness onFilters={onFilters} />);
    const search = screen.getByPlaceholderText('Search name');
    const owner = screen.getByPlaceholderText('Owner');

    fireEvent.change(search, { target: { value: 'alice' } });
    fireEvent.change(owner, { target: { value: 'bob' } });
    fireEvent.click(screen.getByRole('button', { name: /reset/i }));
    expect(onFilters).toHaveBeenLastCalledWith({ q: '', owner: '', state: '' });
    expect(search).toHaveValue('');
    expect(owner).toHaveValue('');

    onFilters.mockClear();
    act(() => vi.advanceTimersByTime(DEBOUNCE_MS * 2));
    expect(onFilters).not.toHaveBeenCalled();
    expect(search).toHaveValue('');
    expect(owner).toHaveValue('');
  });

  it('keeps accepting input after a reset', () => {
    const onFilters = vi.fn();
    render(<Harness onFilters={onFilters} />);
    const search = screen.getByPlaceholderText('Search name');
    fireEvent.change(search, { target: { value: 'alice' } });
    fireEvent.click(screen.getByRole('button', { name: /reset/i }));
    fireEvent.change(search, { target: { value: 'carol' } });
    act(() => vi.advanceTimersByTime(DEBOUNCE_MS));
    expect(onFilters).toHaveBeenLastCalledWith({ q: 'carol', owner: '', state: '' });
  });
});
