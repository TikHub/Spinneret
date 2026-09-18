/** Secret paths relative to the namespace (CreateSecretRequest.path). */
export const SECRET_PATH_PATTERN = /^[a-z0-9][a-z0-9_./-]{0,255}$/;

export type SecretPathError = 'required' | 'pattern' | 'segments';

/**
 * Validates a secret path: the pattern plus no empty, "." or ".." segments
 * (so paths stay canonical for scope globs).
 */
export function validateSecretPath(path: string): SecretPathError | undefined {
  if (path === '') return 'required';
  if (!SECRET_PATH_PATTERN.test(path)) return 'pattern';
  if (path.split('/').some((segment) => segment === '' || segment === '.' || segment === '..')) {
    return 'segments';
  }
  return undefined;
}

export function isValidSecretPath(path: string): boolean {
  return validateSecretPath(path) === undefined;
}

/** Splits a path into its folder ("a/b/" or "") and leaf name. */
export function splitSecretPath(path: string): { folder: string; name: string } {
  const slash = path.lastIndexOf('/');
  if (slash < 0) return { folder: '', name: path };
  return { folder: path.slice(0, slash + 1), name: path.slice(slash + 1) };
}

/** A folder of the secret path tree. */
export interface SecretFolder {
  /** Folder prefix including the trailing slash, e.g. "signing/api/". */
  prefix: string;
  /** Last segment, e.g. "api". */
  name: string;
  /** Secrets below this folder (recursively). */
  count: number;
  children: SecretFolder[];
}

interface MutableFolder {
  prefix: string;
  name: string;
  count: number;
  children: Map<string, MutableFolder>;
}

function freeze(folder: MutableFolder): SecretFolder {
  return {
    prefix: folder.prefix,
    name: folder.name,
    count: folder.count,
    children: [...folder.children.values()].sort((a, b) => a.name.localeCompare(b.name)).map(freeze),
  };
}

/** Builds the folder tree of a list of secret paths (leaves are not included). */
export function buildSecretFolders(paths: readonly string[]): SecretFolder[] {
  const root: MutableFolder = { prefix: '', name: '', count: 0, children: new Map() };
  for (const path of paths) {
    const segments = path.split('/').slice(0, -1);
    let node = root;
    let prefix = '';
    for (const segment of segments) {
      prefix += `${segment}/`;
      let child = node.children.get(segment);
      if (!child) {
        child = { prefix, name: segment, count: 0, children: new Map() };
        node.children.set(segment, child);
      }
      child.count++;
      node = child;
    }
  }
  return freeze(root).children;
}

/** Reports whether a path lies below a folder prefix ("" matches everything). */
export function inFolder(path: string, prefix: string): boolean {
  return prefix === '' || path.startsWith(prefix);
}

/** Tags of secrets: 1..64 characters. */
export function isValidSecretTag(tag: string): boolean {
  return tag.length >= 1 && tag.length <= 64;
}
