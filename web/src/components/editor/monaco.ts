// Local Monaco setup (no CDN): the editor API with all editor features, YAML
// tokenization and the JSON language service. Imported only by the lazily
// loaded editor implementations so Monaco stays in its own chunk.
import { loader } from '@monaco-editor/react';
import * as monaco from 'monaco-editor/editor/editor.api';
import 'monaco-editor/features/register.all';
import 'monaco-editor/languages/definitions/yaml/register';
import 'monaco-editor/languages/features/json/register';
import EditorWorker from 'monaco-editor/editor/editor.worker?worker';
import JsonWorker from 'monaco-editor/language/json/json.worker?worker';

self.MonacoEnvironment = {
  getWorker(_workerId: string, label: string): Worker {
    if (label === 'json') return new JsonWorker();
    return new EditorWorker();
  },
};

loader.config({ monaco: monaco as unknown as Parameters<typeof loader.config>[0]['monaco'] });

export { monaco };
