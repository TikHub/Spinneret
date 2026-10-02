import { type RowData, type Table } from '@tanstack/react-table';
import { Columns3Icon } from 'lucide-react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/button';
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';

import { type DataTableFeatures } from './features';

export interface ColumnVisibilityMenuProps<TData extends RowData> {
  table: Table<DataTableFeatures, TData>;
}

/** Dropdown with a checkbox per hideable column. */
export function ColumnVisibilityMenu<TData extends RowData>({ table }: ColumnVisibilityMenuProps<TData>) {
  const { t } = useTranslation();
  const columns = table.getAllLeafColumns().filter((column) => column.getCanHide());
  if (columns.length === 0) return null;

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="outline" size="sm" aria-label={t('table.toggleColumns')}>
          <Columns3Icon />
          <span className="hidden sm:inline">{t('table.columns')}</span>
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="max-h-80 min-w-44">
        <DropdownMenuLabel>{t('table.toggleColumns')}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        {columns.map((column) => (
          <DropdownMenuCheckboxItem
            key={column.id}
            checked={column.getIsVisible()}
            onCheckedChange={(value) => column.toggleVisibility(Boolean(value))}
            onSelect={(event) => event.preventDefault()}
          >
            {column.columnDef.meta?.label ?? column.id}
          </DropdownMenuCheckboxItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
