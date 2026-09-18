import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { describe, expect, it, vi } from 'vitest';

import '@/i18n';

import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

import { DataTable, type DataTableProps } from './DataTable';
import { type DataTableColumn } from './types';

interface Entry {
  id: string;
  name: string;
  detail?: string;
}

const COLUMNS: DataTableColumn<Entry>[] = [
  { accessorKey: 'name', header: 'Name', meta: { label: 'Name' } },
  { accessorKey: 'id', header: 'ID', meta: { label: 'ID' } },
];

const ROWS: Entry[] = [
  { id: 'a', name: 'alpha', detail: 'alpha details' },
  { id: 'b', name: 'beta' },
  { id: 'c', name: 'gamma', detail: 'gamma details' },
];

function renderTable(props: Partial<DataTableProps<Entry>> = {}) {
  return render(
    <DataTable
      columns={COLUMNS}
      data={ROWS}
      getRowId={(row) => row.id}
      enableColumnVisibility={false}
      virtualize={false}
      {...props}
    />,
  );
}

const expandable: Pick<DataTableProps<Entry>, 'getRowCanExpand' | 'renderExpandedRow'> = {
  getRowCanExpand: (row) => row.original.detail !== undefined,
  renderExpandedRow: (row) => <p>{row.detail}</p>,
};

describe('DataTable row expansion', () => {
  it('adds no expander column without renderExpandedRow', () => {
    renderTable();
    expect(screen.queryByRole('button', { name: 'Expand row' })).not.toBeInTheDocument();
    expect(screen.getAllByRole('columnheader')).toHaveLength(2);
  });

  it('expands and collapses expandable rows into a full-width detail row', () => {
    renderTable(expandable);
    const expanders = screen.getAllByRole('button', { name: 'Expand row' });
    // "beta" has no details.
    expect(expanders).toHaveLength(2);
    expect(screen.queryByText('alpha details')).not.toBeInTheDocument();

    const first = expanders[0] as HTMLElement;
    fireEvent.click(first);
    expect(first).toHaveAttribute('aria-expanded', 'true');
    expect(first).toHaveAccessibleName('Collapse row');
    const detail = screen.getByText('alpha details').closest('td') as HTMLTableCellElement;
    expect(detail).toHaveAttribute('colspan', '3');
    const detailRow = detail.closest('tr') as HTMLTableRowElement;
    expect(first).toHaveAttribute('aria-controls', detailRow.id);
    // The detail row directly follows its row.
    const alphaRow = screen.getByText('alpha').closest('tr') as HTMLTableRowElement;
    expect(alphaRow.nextElementSibling).toBe(detailRow);
    expect(screen.queryByText('gamma details')).not.toBeInTheDocument();

    fireEvent.click(first);
    expect(first).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('alpha details')).not.toBeInTheDocument();
  });

  it('does not activate the row when the expander is clicked', () => {
    const onRowClick = vi.fn();
    renderTable({ ...expandable, onRowClick });
    fireEvent.click(screen.getAllByRole('button', { name: 'Expand row' })[0] as HTMLElement);
    expect(onRowClick).not.toHaveBeenCalled();
    expect(screen.getByText('alpha details')).toBeInTheDocument();
    // Clicks inside the detail row do not activate the row either.
    fireEvent.click(screen.getByText('alpha details'));
    expect(onRowClick).not.toHaveBeenCalled();
    fireEvent.click(screen.getByText('gamma'));
    expect(onRowClick).toHaveBeenCalledWith(ROWS[2]);
  });

  it('keeps rows expanded by row id across refetches', () => {
    const { rerender } = renderTable(expandable);
    fireEvent.click(screen.getAllByRole('button', { name: 'Expand row' })[1] as HTMLElement);
    expect(screen.getByText('gamma details')).toBeInTheDocument();

    const refetched = [{ id: 'z', name: 'zeta' }, ...ROWS.map((row) => ({ ...row }))];
    rerender(
      <DataTable
        columns={COLUMNS}
        data={refetched}
        getRowId={(row) => row.id}
        enableColumnVisibility={false}
        virtualize={false}
        {...expandable}
      />,
    );
    expect(screen.getByText('gamma details')).toBeInTheDocument();
    expect(screen.queryByText('alpha details')).not.toBeInTheDocument();
  });

  it('renders detail rows in virtualized tables', () => {
    // jsdom has no layout: give the scroll container a viewport so the virtualizer renders rows.
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(600);
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(800);
    renderTable({ ...expandable, virtualize: true });
    fireEvent.click(screen.getAllByRole('button', { name: 'Expand row' })[0] as HTMLElement);
    const detailRow = screen.getByText('alpha details').closest('tr') as HTMLTableRowElement;
    expect(screen.getByText('alpha').closest('tr')?.nextElementSibling).toBe(detailRow);
    expect(detailRow).toHaveAttribute('data-index', '1');
  });

  it('supports controlled expansion state', () => {
    const onChange = vi.fn();
    function Controlled() {
      const [expanded, setExpanded] = useState<Record<string, boolean>>({ c: true });
      return (
        <DataTable
          columns={COLUMNS}
          data={ROWS}
          getRowId={(row) => row.id}
          enableColumnVisibility={false}
          virtualize={false}
          expanded={expanded}
          onExpandedChange={(updater) => {
            setExpanded((prev) => {
              const next = typeof updater === 'function' ? updater(prev) : updater;
              onChange(next);
              return next as Record<string, boolean>;
            });
          }}
          {...expandable}
        />
      );
    }
    render(<Controlled />);
    expect(screen.getByText('gamma details')).toBeInTheDocument();
    const alphaRow = screen.getByText('alpha').closest('tr') as HTMLTableRowElement;
    fireEvent.click(within(alphaRow).getByRole('button', { name: 'Expand row' }));
    expect(onChange).toHaveBeenLastCalledWith({ a: true, c: true });
    expect(screen.getByText('alpha details')).toBeInTheDocument();
  });
});

describe('DataTable row activation', () => {
  /** Row action menu like the feature pages use: a portal rendered from a cell. */
  const menuColumn: DataTableColumn<Entry> = {
    id: 'actions',
    header: 'Actions',
    enableHiding: false,
    cell: ({ row }) => (
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <button type="button">Actions for {row.original.name}</button>
        </DropdownMenuTrigger>
        <DropdownMenuContent>
          <DropdownMenuItem onSelect={() => onMenuSelect(row.original)}>Preview</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    ),
  };
  const onMenuSelect = vi.fn();

  it('activates a row on a click in a plain cell', () => {
    const onRowClick = vi.fn();
    renderTable({ onRowClick });
    fireEvent.click(screen.getByText('beta'));
    expect(onRowClick).toHaveBeenCalledWith(ROWS[1]);
  });

  it('does not activate the row when a row action menu item is selected', async () => {
    const onRowClick = vi.fn();
    onMenuSelect.mockClear();
    renderTable({ onRowClick, columns: [...COLUMNS, menuColumn] });

    await userEvent.click(screen.getByRole('button', { name: 'Actions for alpha' }));
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Preview' }));

    expect(onMenuSelect).toHaveBeenCalledWith(ROWS[0]);
    // The menu renders in a portal; React still bubbles its events to the row.
    expect(onRowClick).not.toHaveBeenCalled();
  });
});
