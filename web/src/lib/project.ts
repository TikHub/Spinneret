/**
 * Canonical links to the project outside this deployment.
 *
 * They live in one module so that a rename of the repository is one edit rather
 * than a search, and so that nothing in the feature code hard-codes a URL.
 */

/** Repository the console was built from. */
export const REPO_URL = 'https://github.com/TikHub/Spinneret';

/** Organisation that maintains and open-sourced the project. */
export const MAINTAINER_URL = 'https://github.com/TikHub';

/** Maintainer name, shown as written — not translated. */
export const MAINTAINER = 'TikHub';

/** Licence the project is published under. */
export const LICENSE = 'Apache-2.0';

export const LICENSE_URL = `${REPO_URL}/blob/main/LICENSE`;
export const CHANGELOG_URL = `${REPO_URL}/blob/main/CHANGELOG.md`;
export const ISSUES_URL = `${REPO_URL}/issues`;
export const SECURITY_URL = `${REPO_URL}/blob/main/SECURITY.md`;

/**
 * The manual is published in the repository in two languages. `language` is an
 * i18next language tag; anything that is not Chinese — including a missing tag,
 * which is what an i18n instance that has not finished initialising reports —
 * gets the English set.
 */
export function docsUrl(language: string | undefined): string {
  const dir = language?.toLowerCase().startsWith('zh') ? 'zh' : 'en';
  return `${REPO_URL}/tree/main/documents/${dir}`;
}
