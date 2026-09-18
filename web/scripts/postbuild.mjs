// Vite empties dist/ on every build; recreate dist/.keep so `//go:embed all:dist`
// in web/embed.go always has at least one file and the placeholder stays tracked.
import { mkdirSync, writeFileSync, existsSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const distDir = join(resolve(dirname(fileURLToPath(import.meta.url)), '..'), 'dist');
mkdirSync(distDir, { recursive: true });
writeFileSync(join(distDir, '.keep'), '');
if (!existsSync(join(distDir, 'index.html'))) {
  console.error('postbuild: dist/index.html is missing');
  process.exit(1);
}
