import { render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import i18n from '@/i18n';

import { DurationInput } from './DurationInput';

describe('DurationInput', () => {
  afterEach(async () => {
    await i18n.changeLanguage('en');
  });

  it('previews the duration in the viewer language', async () => {
    const { rerender } = render(<DurationInput value="90m" onChange={() => undefined} />);
    expect(screen.getByText('1h 30m')).toBeInTheDocument();

    await i18n.changeLanguage('zh-CN');
    rerender(<DurationInput value="1d2h3m" onChange={() => undefined} />);
    expect(await screen.findByText('1天 2小时 3分钟')).toBeInTheDocument();
  });
});
