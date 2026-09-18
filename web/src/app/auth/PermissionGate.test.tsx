import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createRef } from 'react';
import { describe, expect, it, vi } from 'vitest';

import '@/i18n';

import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { TooltipProvider } from '@/components/ui/tooltip';

vi.mock('./AuthContext', () => ({
  useAuth: () => ({
    can: (permission: string) => permission === 'proxy:write',
    canInTenant: () => false,
  }),
}));

const { PermissionButton } = await import('./PermissionGate');

describe('PermissionButton', () => {
  it('forwards its ref to the button when the permission is granted', () => {
    const ref = createRef<HTMLButtonElement>();
    render(
      <PermissionButton ref={ref} permission="proxy:write">
        Edit
      </PermissionButton>,
    );
    expect(ref.current).toBe(screen.getByRole('button', { name: 'Edit' }));
  });

  it('forwards its ref to the disabled button when the permission is missing', () => {
    const ref = createRef<HTMLButtonElement>();
    render(
      <TooltipProvider>
        <PermissionButton ref={ref} permission="secret:reveal">
          Reveal
        </PermissionButton>
      </TooltipProvider>,
    );
    const button = screen.getByRole('button', { name: 'Reveal' });
    expect(ref.current).toBe(button);
    expect(button).toBeDisabled();
  });

  it('works as an asChild trigger', async () => {
    const user = userEvent.setup();
    const consoleError = vi.spyOn(console, 'error');
    render(
      <Popover>
        <PopoverTrigger asChild>
          <PermissionButton permission="proxy:write">Options</PermissionButton>
        </PopoverTrigger>
        <PopoverContent>Proxy options</PopoverContent>
      </Popover>,
    );
    const trigger = screen.getByRole('button', { name: 'Options' });
    expect(trigger).toHaveAttribute('aria-haspopup', 'dialog');
    await user.click(trigger);
    expect(await screen.findByText('Proxy options')).toBeInTheDocument();
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    // Function components that drop refs make React warn; the trigger must not.
    expect(consoleError).not.toHaveBeenCalledWith(
      expect.stringContaining('Function components cannot be given refs'),
      expect.anything(),
      expect.anything(),
    );
  });
});
