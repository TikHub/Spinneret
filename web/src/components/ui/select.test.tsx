import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from './select';

describe('Select', () => {
  it('marks the value so the trigger can clamp long text to one line', () => {
    render(
      <Select>
        <SelectTrigger size="sm" className="w-48" aria-label="Endpoint group">
          <SelectValue placeholder="Select a site to filter by endpoint group" />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="all">All groups</SelectItem>
        </SelectContent>
      </Select>,
    );

    const trigger = screen.getByRole('combobox', { name: 'Endpoint group' });
    // The trigger styles the value through this attribute (Radix drops className
    // on SelectValue); without it a long placeholder overflows the trigger.
    const value = trigger.querySelector('[data-slot="select-value"]');
    expect(value).not.toBeNull();
    expect(value).toHaveTextContent('Select a site to filter by endpoint group');
    expect(trigger.className).toContain('*:data-[slot=select-value]:line-clamp-1');
  });
});
