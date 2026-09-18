import { ShieldCheckIcon } from 'lucide-react';
import { type ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { useAuth } from '@/app/auth/AuthContext';
import { useTheme, type ThemePreference } from '@/app/theme/ThemeProvider';
import { PageHeader } from '@/components/PageHeader';
import { PageIntro } from '@/components/PageIntro';
import { TimeAgo } from '@/components/TimeAgo';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { FormField } from '@/components/ui/form';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { LANGUAGE_NAMES, SUPPORTED_LANGUAGES, toSupportedLanguage } from '@/i18n';

import { ChangePasswordCard } from '../components/ChangePasswordCard';

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[9rem_1fr] items-center gap-3 py-2 text-sm">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate">{children}</dd>
    </div>
  );
}

/** Profile: account details, language and theme preferences, password change. */
export default function ProfilePage() {
  const { t, i18n } = useTranslation();
  const { user, isPlatformAdmin, tenants } = useAuth();
  const { theme, setTheme } = useTheme();
  const language = toSupportedLanguage(i18n.language) ?? 'en';

  return (
    <>
      <PageHeader title={t('profile.title')} description={t('profile.description')} />
      <PageIntro page="profile" />
      <div className="grid gap-4 xl:grid-cols-2">
        <div className="grid content-start gap-4">
          <Card>
            <CardHeader>
              <CardTitle>{t('profile.account')}</CardTitle>
            </CardHeader>
            <CardContent>
              <dl className="divide-y">
                <Row label={t('profile.username')}>
                  <span className="font-mono">{user?.username}</span>
                  {isPlatformAdmin && (
                    <Badge variant="secondary" className="ml-2">
                      <ShieldCheckIcon />
                      {t('profile.platformAdmin')}
                    </Badge>
                  )}
                </Row>
                <Row label={t('profile.displayName')}>{user?.displayName || '—'}</Row>
                <Row label={t('profile.email')}>{user?.email || '—'}</Row>
                <Row label={t('profile.lastLogin')}>
                  <TimeAgo value={user?.lastLoginAt} fallback={t('time.never')} />
                </Row>
                <Row label={t('profile.createdAt')}>
                  <TimeAgo value={user?.createdAt} />
                </Row>
                <Row label={t('profile.tenants')}>
                  {tenants.length === 0 ? (
                    <span className="text-muted-foreground">{t('profile.noTenants')}</span>
                  ) : (
                    <span className="flex flex-wrap gap-1">
                      {tenants.map((access) => (
                        <Badge key={access.tenant?.id} variant="outline">
                          {access.tenant?.displayName || access.tenant?.name}
                          {access.bindings.length > 0 && (
                            <span className="text-muted-foreground">
                              {' · '}
                              {[...new Set(access.bindings.map((b) => b.role))].join(', ')}
                            </span>
                          )}
                        </Badge>
                      ))}
                    </span>
                  )}
                </Row>
              </dl>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>{t('profile.preferences')}</CardTitle>
            </CardHeader>
            <CardContent className="grid gap-4 sm:grid-cols-2">
              <FormField label={t('profile.language')} description={t('profile.languageHint')}>
                <Select value={language} onValueChange={(lng) => void i18n.changeLanguage(lng)}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SUPPORTED_LANGUAGES.map((lng) => (
                      <SelectItem key={lng} value={lng}>
                        {LANGUAGE_NAMES[lng]}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FormField>
              <FormField label={t('profile.theme')}>
                <Select value={theme} onValueChange={(v) => setTheme(v as ThemePreference)}>
                  <SelectTrigger className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {(['light', 'dark', 'system'] as const).map((value) => (
                      <SelectItem key={value} value={value}>
                        {t(`theme.${value}`)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </FormField>
            </CardContent>
          </Card>
        </div>
        <div className="content-start">
          <ChangePasswordCard />
        </div>
      </div>
    </>
  );
}
