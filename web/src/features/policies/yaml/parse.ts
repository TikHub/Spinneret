/**
 * YAML subset parser used to load policy YAML into the form and rule builders
 * (the console has no YAML dependency). Supported: block mappings and
 * sequences (including compact "- key: value" items), flow mappings and
 * sequences (also across lines), plain, single- and double-quoted scalars,
 * literal and folded block scalars, comments and a leading "---".
 * Anchors, aliases, tags, complex keys and multiple documents are rejected
 * with a YamlSubsetError so callers can fall back to the YAML editor.
 */

export type YamlValue = null | boolean | number | string | YamlValue[] | { [key: string]: YamlValue };

export class YamlSubsetError extends Error {
  readonly line: number;

  constructor(message: string, line: number) {
    super(line > 0 ? `line ${line}: ${message}` : message);
    this.name = 'YamlSubsetError';
    this.line = line;
  }
}

interface Line {
  /** 1-based line number. */
  no: number;
  indent: number;
  /** Content after the indentation, comment removed, trailing spaces trimmed. */
  text: string;
  /** Raw line (for block scalars). */
  raw: string;
}

/**
 * Reports whether a quote at `index` starts a quoted scalar: at the start of
 * the text, after a flow indicator ("[", "{", ","), or after a "key:" or "-"
 * indicator followed by whitespace. A quote inside a plain scalar (e.g.
 * "description: don't") is an ordinary character.
 */
function startsQuotedScalar(text: string, index: number): boolean {
  let j = index - 1;
  while (j >= 0 && /\s/.test(text[j] ?? '')) j--;
  if (j < 0) return true;
  const prev = text[j] ?? '';
  if (prev === '[' || prev === '{' || prev === ',') return true;
  if (j === index - 1) return false;
  if (prev === ':') return true;
  return prev === '-' && (j === 0 || /\s/.test(text[j - 1] ?? ''));
}

/** Removes a trailing comment (a "#" at the start or after whitespace, outside quotes). */
export function stripComment(text: string): string {
  let quote: '"' | "'" | undefined;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quote === '"') {
      if (ch === '\\') i++;
      else if (ch === '"') quote = undefined;
    } else if (quote === "'") {
      if (ch === "'") {
        if (text[i + 1] === "'") i++;
        else quote = undefined;
      }
    } else if (ch === '"' || ch === "'") {
      if (startsQuotedScalar(text, i)) quote = ch;
    } else if (ch === '#' && (i === 0 || /\s/.test(text[i - 1] ?? ''))) {
      return text.slice(0, i).trimEnd();
    }
  }
  return text.trimEnd();
}

function toLines(source: string): Line[] {
  const rawLines = source.replace(/\r\n?/g, '\n').split('\n');
  return rawLines.map((raw, index) => {
    const indentMatch = /^ */.exec(raw);
    const indent = indentMatch ? indentMatch[0].length : 0;
    if (raw[indent] === '\t') throw new YamlSubsetError('tabs are not allowed for indentation', index + 1);
    return { no: index + 1, indent, text: stripComment(raw.slice(indent)), raw };
  });
}

const NULL_RE = /^(null|Null|NULL|~)$/;
const TRUE_RE = /^(true|True|TRUE)$/;
const FALSE_RE = /^(false|False|FALSE)$/;
const INT_RE = /^[-+]?[0-9]+$/;
const PREFIXED_INT_RE = /^([-+]?)0([xob])([0-9a-fA-F]+)$/;
const PREFIXED_DIGITS: Record<string, RegExp> = { x: /^[0-9a-fA-F]+$/, o: /^[0-7]+$/, b: /^[01]+$/ };
const PREFIXED_RADIX: Record<string, number> = { x: 16, o: 8, b: 2 };
const FLOAT_RE = /^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$/;

/**
 * Resolves a plain scalar to null, boolean, number or string, following the
 * server's YAML library (go-yaml v3): "_" separators are ignored in numbers
 * and 0x/0o/0b prefixes are accepted. Decimal integers whose conversion would
 * change them (leading zeros such as business code "0010", or beyond 2^53)
 * keep their literal text, which is what the server reads for codes.
 */
export function resolvePlain(text: string): YamlValue {
  if (text === '' || NULL_RE.test(text)) return null;
  if (TRUE_RE.test(text)) return true;
  if (FALSE_RE.test(text)) return false;
  if (/^[-+]?\.(inf|Inf|INF)$/.test(text)) return text.startsWith('-') ? -Infinity : Infinity;
  if (/^\.(nan|NaN|NAN)$/.test(text)) return Number.NaN;
  if (!/^[-+0-9.]/.test(text)) return text;
  const plain = text.replace(/_/g, '');
  if (INT_RE.test(plain)) {
    const digits = plain.replace(/^[-+]/, '');
    if (digits.length > 1 && digits.startsWith('0')) return text;
    const value = Number(plain);
    return Number.isSafeInteger(value) ? value : text;
  }
  const prefixed = PREFIXED_INT_RE.exec(plain);
  if (prefixed) {
    const [, sign = '', radix = 'x', digits = ''] = prefixed;
    if (!PREFIXED_DIGITS[radix]?.test(digits)) return text;
    const value = Number.parseInt(digits, PREFIXED_RADIX[radix]);
    if (!Number.isSafeInteger(value)) return text;
    return sign === '-' ? -value : value;
  }
  if (FLOAT_RE.test(plain)) return Number(plain);
  return text;
}

const DOUBLE_ESCAPES: Record<string, string> = {
  '0': '\0',
  a: '\x07',
  b: '\b',
  t: '\t',
  '\t': '\t',
  n: '\n',
  v: '\v',
  f: '\f',
  r: '\r',
  e: '\x1b',
  ' ': ' ',
  '"': '"',
  '/': '/',
  '\\': '\\',
  N: String.fromCharCode(0x85),
  _: String.fromCharCode(0xa0),
};

/** Inline tokenizer for flow collections and quoted scalars on (joined) lines. */
class InlineParser {
  pos = 0;

  constructor(
    private readonly src: string,
    private readonly line: number,
  ) {}

  fail(message: string): never {
    throw new YamlSubsetError(message, this.line);
  }

  skipSpaces(): void {
    while (this.pos < this.src.length && /\s/.test(this.src[this.pos] ?? '')) this.pos++;
  }

  atEnd(): boolean {
    this.skipSpaces();
    return this.pos >= this.src.length;
  }

  /** Parses one value; `flow` stops plain scalars at flow indicators. */
  value(flow: boolean): YamlValue {
    this.skipSpaces();
    const ch = this.src[this.pos];
    if (ch === '{') return this.flowMap();
    if (ch === '[') return this.flowSeq();
    if (ch === '"') return this.doubleQuoted();
    if (ch === "'") return this.singleQuoted();
    if (ch === '&' || ch === '*') this.fail('anchors and aliases are not supported');
    if (ch === '!') this.fail('tags are not supported');
    return resolvePlain(this.plain(flow));
  }

  plain(flow: boolean): string {
    const start = this.pos;
    while (this.pos < this.src.length) {
      const ch = this.src[this.pos] ?? '';
      if (flow && (ch === ',' || ch === ']' || ch === '}')) break;
      if (flow && ch === ':' && this.isSeparatorColon(this.pos)) break;
      this.pos++;
    }
    return this.src.slice(start, this.pos).trim();
  }

  /** A ":" separates key and value when followed by whitespace, a flow indicator or the end. */
  isSeparatorColon(index: number): boolean {
    const next = this.src[index + 1];
    return next === undefined || /[\s,\]}]/.test(next);
  }

  doubleQuoted(): string {
    this.pos++;
    let out = '';
    while (this.pos < this.src.length) {
      const ch = this.src[this.pos++] ?? '';
      if (ch === '"') return out;
      if (ch !== '\\') {
        out += ch;
        continue;
      }
      const esc = this.src[this.pos++] ?? '';
      if (esc in DOUBLE_ESCAPES) out += DOUBLE_ESCAPES[esc];
      else if (esc === 'x' || esc === 'u' || esc === 'U') {
        const len = esc === 'x' ? 2 : esc === 'u' ? 4 : 8;
        const hex = this.src.slice(this.pos, this.pos + len);
        if (!new RegExp(`^[0-9a-fA-F]{${len}}$`).test(hex)) this.fail('invalid escape sequence');
        out += String.fromCodePoint(Number.parseInt(hex, 16));
        this.pos += len;
      } else this.fail(`invalid escape "\\${esc}"`);
    }
    return this.fail('unterminated double-quoted string');
  }

  singleQuoted(): string {
    this.pos++;
    let out = '';
    while (this.pos < this.src.length) {
      const ch = this.src[this.pos++] ?? '';
      if (ch !== "'") {
        out += ch;
      } else if (this.src[this.pos] === "'") {
        out += "'";
        this.pos++;
      } else {
        return out;
      }
    }
    return this.fail('unterminated single-quoted string');
  }

  flowSeq(): YamlValue[] {
    this.pos++;
    const items: YamlValue[] = [];
    for (;;) {
      this.skipSpaces();
      if (this.src[this.pos] === ']') {
        this.pos++;
        return items;
      }
      if (this.pos >= this.src.length) this.fail('unterminated flow sequence');
      items.push(this.value(true));
      this.skipSpaces();
      const ch = this.src[this.pos];
      if (ch === ',') this.pos++;
      else if (ch !== ']') this.fail('expected "," or "]" in flow sequence');
    }
  }

  flowMap(): { [key: string]: YamlValue } {
    this.pos++;
    const out: { [key: string]: YamlValue } = {};
    for (;;) {
      this.skipSpaces();
      if (this.src[this.pos] === '}') {
        this.pos++;
        return out;
      }
      if (this.pos >= this.src.length) this.fail('unterminated flow mapping');
      const keyValue = this.value(true);
      const key = keyToString(keyValue, this.line);
      this.skipSpaces();
      let value: YamlValue = null;
      if (this.src[this.pos] === ':') {
        this.pos++;
        this.skipSpaces();
        const next = this.src[this.pos];
        value = next === ',' || next === '}' ? null : this.value(true);
      }
      if (Object.prototype.hasOwnProperty.call(out, key)) this.fail(`duplicate key "${key}"`);
      setKey(out, key, value);
      this.skipSpaces();
      const ch = this.src[this.pos];
      if (ch === ',') this.pos++;
      else if (ch !== '}') this.fail('expected "," or "}" in flow mapping');
    }
  }
}

/** Defines an own property (never invokes the __proto__ setter). */
function setKey(target: { [key: string]: YamlValue }, key: string, value: YamlValue): void {
  Object.defineProperty(target, key, { value, enumerable: true, writable: true, configurable: true });
}

function keyToString(value: YamlValue, line: number): string {
  if (value === null) return 'null';
  if (typeof value === 'object') throw new YamlSubsetError('complex mapping keys are not supported', line);
  return String(value);
}

/** Bracket depth of flow collections outside quotes (> 0 means the collection continues). */
function flowDepth(text: string): number {
  let depth = 0;
  let quote: string | undefined;
  for (let i = 0; i < text.length; i++) {
    const ch = text[i];
    if (quote) {
      if (quote === '"' && ch === '\\') i++;
      else if (ch === quote) {
        if (quote === "'" && text[i + 1] === "'") i++;
        else quote = undefined;
      }
    } else if ((ch === '"' || ch === "'") && startsQuotedScalar(text, i)) quote = ch;
    else if (ch === '[' || ch === '{') depth++;
    else if (ch === ']' || ch === '}') depth--;
  }
  return quote ? Math.max(depth, 1) : depth;
}

/** Finds the ":" separating a block mapping key from its value, or -1. */
function findKeyColon(text: string): number {
  const first = text[0];
  if (first === '"' || first === "'") {
    const parser = new InlineParser(text, 0);
    try {
      if (first === '"') parser.doubleQuoted();
      else parser.singleQuoted();
    } catch {
      return -1;
    }
    let i = parser.pos;
    while (text[i] === ' ' || text[i] === '\t') i++;
    return text[i] === ':' && isValueSeparator(text[i + 1]) ? i : -1;
  }
  if (first === '[' || first === '{') return -1;
  for (let i = 0; i < text.length; i++) {
    if (text[i] === ':' && isValueSeparator(text[i + 1])) return i;
  }
  return -1;
}

/** A ":" ends a block mapping key when followed by a space, a tab or the end of the line. */
function isValueSeparator(next: string | undefined): boolean {
  return next === undefined || next === ' ' || next === '\t';
}

function isSeqItem(text: string): boolean {
  return text === '-' || text.startsWith('- ');
}

class BlockParser {
  private index = 0;

  constructor(private readonly lines: Line[]) {}

  /** Next line with content, skipping blank and comment-only lines. */
  peek(): Line | undefined {
    while (this.index < this.lines.length && (this.lines[this.index]?.text ?? '') === '') this.index++;
    return this.lines[this.index];
  }

  document(): YamlValue {
    let first = this.peek();
    if (first && first.indent === 0 && (first.text === '---' || first.text.startsWith('--- '))) {
      if (first.text !== '---') throw new YamlSubsetError('content after "---" is not supported', first.no);
      this.index++;
      first = this.peek();
    }
    if (!first) return null;
    if (first.text.startsWith('%')) throw new YamlSubsetError('directives are not supported', first.no);
    const value = this.block(first.indent);
    const rest = this.peek();
    if (rest) {
      if (rest.text === '---' || rest.text === '...') {
        this.index++;
        if (this.peek()) throw new YamlSubsetError('multiple documents are not supported', rest.no);
      } else {
        throw new YamlSubsetError('unexpected content (check the indentation)', rest.no);
      }
    }
    return value;
  }

  block(indent: number): YamlValue {
    const line = this.peek();
    if (!line) return null;
    if (isSeqItem(line.text)) return this.sequence(indent);
    if (line.text.startsWith('? '))
      throw new YamlSubsetError('complex mapping keys are not supported', line.no);
    if (findKeyColon(line.text) >= 0) return this.mapping(indent);
    // A single scalar or flow collection as a whole block.
    this.index++;
    return this.inline(line, line.text, indent);
  }

  mapping(indent: number): { [key: string]: YamlValue } {
    const out: { [key: string]: YamlValue } = {};
    for (let line = this.peek(); line && line.indent === indent; line = this.peek()) {
      if (isSeqItem(line.text) || line.text === '---' || line.text === '...') break;
      const colon = findKeyColon(line.text);
      if (colon < 0) throw new YamlSubsetError('expected "key: value"', line.no);
      const rawKey = line.text.slice(0, colon).trim();
      if (rawKey.startsWith('&') || rawKey.startsWith('*') || rawKey.startsWith('!')) {
        throw new YamlSubsetError('anchors, aliases and tags are not supported', line.no);
      }
      const key = keyToString(new InlineParser(rawKey, line.no).value(false), line.no);
      if (Object.prototype.hasOwnProperty.call(out, key)) {
        throw new YamlSubsetError(`duplicate key "${key}"`, line.no);
      }
      const rest = line.text.slice(colon + 1).trim();
      this.index++;
      setKey(out, key, this.valueAfterIndicator(line, rest, indent, true));
    }
    const next = this.peek();
    if (next && next.indent > indent) throw new YamlSubsetError('unexpected indentation', next.no);
    return out;
  }

  sequence(indent: number): YamlValue[] {
    const out: YamlValue[] = [];
    for (let line = this.peek(); line && line.indent === indent && isSeqItem(line.text); line = this.peek()) {
      const rest = line.text.slice(1).trimStart();
      const offset = line.text.length - rest.length;
      if (rest !== '' && (isSeqItem(rest) || findKeyColon(rest) >= 0)) {
        // Compact nested collection: re-read the item as a line indented at its content column.
        this.lines[this.index] = { ...line, indent: indent + offset, text: rest };
        out.push(this.block(indent + offset));
        continue;
      }
      this.index++;
      out.push(this.valueAfterIndicator(line, rest, indent, false));
    }
    const next = this.peek();
    if (next && next.indent > indent) throw new YamlSubsetError('unexpected indentation', next.no);
    return out;
  }

  /** Value after "key:" or "-": nested block, block scalar, or inline value. */
  valueAfterIndicator(line: Line, rest: string, indent: number, inMapping: boolean): YamlValue {
    if (rest === '') {
      const next = this.peek();
      if (!next) return null;
      if (next.indent > indent) return this.block(next.indent);
      // "key:" followed by "- item" at the same indentation is a sequence value.
      if (inMapping && next.indent === indent && isSeqItem(next.text)) return this.sequence(indent);
      return null;
    }
    if (/^[|>]/.test(rest)) return this.blockScalar(line, rest, indent);
    return this.inline(line, rest, indent);
  }

  /** Parses an inline value, joining continuation lines of flow collections and plain scalars. */
  inline(line: Line, text: string, indent: number): YamlValue {
    let joined = text;
    // Only flow collections and quoted scalars continue on the next lines; brackets or
    // quotes inside a plain scalar ("description: don't [yet]") are ordinary characters.
    const continues = /^["'[{]/.test(text);
    while (continues && flowDepth(joined) > 0) {
      const next = this.peek();
      if (!next) throw new YamlSubsetError('unterminated flow collection', line.no);
      joined = `${joined} ${next.text}`;
      this.index++;
    }
    const parser = new InlineParser(joined, line.no);
    const first = joined[0];
    const value = parser.value(false);
    if (!parser.atEnd()) throw new YamlSubsetError('unexpected characters after value', line.no);
    if (typeof value === 'string' && first !== '"' && first !== "'" && first !== '[' && first !== '{') {
      // Multi-line plain scalar: more-indented continuation lines are folded with spaces.
      let folded = value;
      for (let next = this.peek(); next && next.indent > indent; next = this.peek()) {
        if (findKeyColon(next.text) >= 0 || isSeqItem(next.text)) break;
        folded = `${folded} ${next.text}`;
        this.index++;
      }
      return folded;
    }
    return value;
  }

  blockScalar(line: Line, header: string, indent: number): string {
    const match = /^([|>])([+-]?)(\d?)([+-]?)$/.exec(header);
    if (!match) throw new YamlSubsetError('invalid block scalar header', line.no);
    const folded = match[1] === '>';
    const chomp = match[2] || match[4];
    const body: string[] = [];
    let contentIndent = match[3] ? indent + Number(match[3]) : -1;
    while (this.index < this.lines.length) {
      const next = this.lines[this.index];
      if (!next) break;
      const blank = next.raw.trim() === '';
      if (!blank) {
        if (contentIndent < 0) contentIndent = next.indent;
        if (next.indent < contentIndent || next.indent <= indent) break;
      }
      body.push(blank ? '' : next.raw.slice(contentIndent));
      this.index++;
    }
    let trailing = 0;
    while (body.length > 0 && body[body.length - 1] === '') {
      body.pop();
      trailing++;
    }
    if (body.length === 0) return chomp === '+' ? '\n'.repeat(trailing) : '';
    let text = folded ? foldLines(body) : body.join('\n');
    if (chomp !== '-') text += '\n';
    if (chomp === '+') text += '\n'.repeat(trailing);
    return text;
  }
}

/**
 * Folds the lines of a ">" block scalar: adjacent lines are joined with a
 * space, each empty line between them becomes one line break, and line breaks
 * around more-indented lines are kept.
 */
function foldLines(lines: readonly string[]): string {
  let out = '';
  let empty = 0;
  let content = false;
  let moreIndented = false;
  for (const line of lines) {
    if (line === '') {
      empty++;
      continue;
    }
    if (line.startsWith(' ') || line.startsWith('\t')) {
      moreIndented = true;
      out += '\n'.repeat(content ? empty + 1 : empty);
    } else if (moreIndented) {
      moreIndented = false;
      out += '\n'.repeat(empty + 1);
    } else if (empty === 0) {
      if (content) out += ' ';
    } else {
      out += '\n'.repeat(empty);
    }
    out += line;
    content = true;
    empty = 0;
  }
  return out;
}

/** Parses YAML text into plain values; throws YamlSubsetError on unsupported or invalid input. */
export function parseYaml(source: string): YamlValue {
  return new BlockParser(toLines(source)).document();
}
