import { create } from '@bufbuild/protobuf';
import { render, screen, within } from '@testing-library/react';
import i18next from 'i18next';
import { I18nextProvider, initReactI18next } from 'react-i18next';
import { beforeAll, describe, expect, it } from 'vitest';

import { ImportFailureSchema, ImportIdentitiesResponseSchema } from '@/gen/spinneret/v1/identity_admin_pb';
import enCommon from '@/i18n/locales/en/common.json';
import enIdentities from '@/i18n/locales/en/identities.json';

import { IMPORT_FAILURES_SHOWN } from '../importData';
import { ImportResultView } from './ImportResultView';

beforeAll(async () => {
  await i18next.use(initReactI18next).init({
    lng: 'en',
    resources: { en: { common: enCommon, identities: enIdentities } },
    defaultNS: 'common',
    fallbackNS: 'common',
    interpolation: { escapeValue: false },
    react: { useSuspense: false },
  });
});

function renderResult(dryRun: boolean, failed = 2) {
  const response = create(ImportIdentitiesResponseSchema, {
    created: 1200,
    updated: 3,
    unchanged: 4,
    failed: Array.from({ length: failed }, (_, i) =>
      create(ImportFailureSchema, { line: failed - i + 1, message: `row ${failed - i + 1} is invalid` }),
    ),
  });
  return render(
    <I18nextProvider i18n={i18next}>
      <ImportResultView result={response} dryRun={dryRun} />
    </I18nextProvider>,
  );
}

describe('ImportResultView', () => {
  it('renders dry run counts and rejected rows sorted by line', () => {
    renderResult(true);
    expect(screen.getByText('Dry run: nothing was stored')).toBeInTheDocument();
    expect(screen.getByText('Would create')).toBeInTheDocument();
    expect(screen.getByText('1,200')).toBeInTheDocument();
    expect(screen.getByText('1,209 rows processed')).toBeInTheDocument();

    const table = screen.getByRole('table');
    const rows = within(table).getAllByRole('row');
    // Header plus two failures, the lowest line first.
    expect(rows).toHaveLength(3);
    expect(within(rows[1]!).getByText('2')).toBeInTheDocument();
    expect(within(rows[1]!).getByText('row 2 is invalid')).toBeInTheDocument();
    expect(within(rows[2]!).getByText('3')).toBeInTheDocument();
  });

  it('labels a real import and hides the failure table without failures', () => {
    renderResult(false, 0);
    expect(screen.getByText('Import finished')).toBeInTheDocument();
    expect(screen.getByText('Created')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('caps the rendered failures and reports the rest', () => {
    renderResult(true, IMPORT_FAILURES_SHOWN + 3);
    expect(within(screen.getByRole('table')).getAllByRole('row')).toHaveLength(IMPORT_FAILURES_SHOWN + 1);
    expect(screen.getByText('3 more rejected rows not shown.')).toBeInTheDocument();
  });
});
