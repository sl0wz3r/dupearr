/**
 * Client-side validation helpers shared by the Profiles, Media Management and Exclusions settings
 * pages. They only catch obvious mistakes early — the server stays authoritative (its 400
 * `[{propertyName, errorMessage}]` responses are mapped onto the same fields).
 */
import type { ValidationFailure } from '@/api/types';

/**
 * Rewrites RE2-only syntax that JavaScript rejects so the JS parser can check the rest:
 * `(?P<name>` → `(?<name>`, flag groups `(?i)` / `(?-s)` are dropped, `(?i:…)` → `(?:…)`.
 * Only valid RE2 flags (i, m, s, U) are rewritten, so `(?x)` still fails.
 */
function toJsSyntax(pattern: string): string {
  return pattern
    .replace(/\(\?P</g, '(?<')
    .replace(/\(\?[imsU]*-?[imsU]*\)/g, (m) => (m === '(?)' || m === '(?-)' ? m : ''))
    .replace(/\(\?[imsU]*-?[imsU]*:/g, (m) => (m === '(?:' ? m : '(?:'));
}

/**
 * Validates a regular expression the Go backend will compile with RE2 (`regexp` package): the JS
 * parser checks the general syntax (after rewriting RE2-only flag/group syntax), then constructs
 * JS accepts but RE2 rejects (look-around, back-references) are reported. Best effort — the server
 * compiles the pattern authoritatively. Returns an error message or null.
 */
export function validateRegex(pattern: string): string | null {
  if (!pattern.trim()) return 'A regular expression is required';
  try {
    new RegExp(toJsSyntax(pattern));
  } catch (e) {
    const detail = e instanceof Error ? e.message.replace(/^Invalid regular expression:\s*/i, '') : '';
    return detail ? `Invalid regular expression: ${detail}` : 'Invalid regular expression';
  }
  if (/\(\?<?[=!]/.test(pattern)) {
    return 'Look-around such as (?=…) or (?<!…) is not supported (Go RE2 syntax)';
  }
  // An odd number of backslashes before a digit is a back-reference (\1); "\\1" is a literal.
  if (/(?:^|[^\\])(?:\\\\)*\\[1-9]/.test(pattern)) {
    return 'Back-references such as \\1 are not supported (Go RE2 syntax)';
  }
  return null;
}

/**
 * Validates a doublestar glob: non-empty and balanced `[…]` / `{…}` (backslash escapes the next
 * character). Returns an error message or null.
 */
export function validateGlob(pattern: string): string | null {
  if (!pattern.trim()) return 'A pattern is required';
  let square = 0;
  let curly = 0;
  for (let i = 0; i < pattern.length; i++) {
    const ch = pattern[i];
    if (ch === '\\') {
      i++; // skip the escaped character
      continue;
    }
    if (square > 0) {
      if (ch === ']') square--;
      continue;
    }
    if (ch === '[') square++;
    else if (ch === '{') curly++;
    else if (ch === '}') {
      if (curly === 0) return 'Unbalanced "}" in pattern';
      curly--;
    } else if (ch === ']') return 'Unbalanced "]" in pattern';
  }
  if (square > 0) return 'Unclosed "[" in pattern';
  if (curly > 0) return 'Unclosed "{" in pattern';
  return null;
}

/** True for absolute POSIX (`/data`), Windows drive (`C:\media`, `D:/media`) and UNC (`\\nas\share`) paths. */
export function isAbsolutePath(path: string): boolean {
  const p = path.trim();
  return p.startsWith('/') || /^[A-Za-z]:[\\/]/.test(p) || p.startsWith('\\\\');
}

/** Lower-cased, forward-slashed, without duplicate or trailing slashes (for prefix comparisons only). */
export function comparablePath(p: string): string {
  let s = p.trim().replace(/\\/g, '/').replace(/\/{2,}/g, '/');
  if (s.length > 1) s = s.replace(/\/+$/, '');
  return s.toLowerCase();
}

/** `/`, `C:\`, `C:/` or `C:` — the root of a filesystem. */
export function isFilesystemRoot(p: string): boolean {
  const s = comparablePath(p);
  return s === '/' || /^[a-z]:$/.test(s);
}

/** True when `child` is `parent` or lies inside it (segment-aware, case-insensitive). */
export function isSameOrInside(parent: string, child: string): boolean {
  const a = comparablePath(parent);
  const b = comparablePath(child);
  if (!a || !b) return false;
  if (a === b) return true;
  return b.startsWith(a.endsWith('/') ? a : `${a}/`);
}

/** Lower-cases the first letter of every path segment ("Criteria[2].Order" → "criteria[2].order"). */
function normalizeSegment(segment: string): string {
  return segment ? segment.charAt(0).toLowerCase() + segment.slice(1) : segment;
}

/**
 * Splits a server property name into path segments: `Criteria[2].Patterns[0].Pattern` and
 * `criteria.2.patterns.0.pattern` both become `['criteria', 2, 'patterns', 0, 'pattern']`.
 * Numeric segments become numbers; names get a lower-case first letter.
 */
export function splitPropertyPath(propertyName: string | null | undefined): (string | number)[] {
  if (!propertyName) return [];
  return propertyName
    .replace(/\[(\d+)\]/g, '.$1')
    .split('.')
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => (/^\d+$/.test(s) ? Number(s) : normalizeSegment(s)));
}

/** Field → messages for flat forms (path mappings, exclusions). Unknown fields land in `''`. */
export function groupFieldErrors(
  failures: readonly ValidationFailure[],
  knownFields: readonly string[],
): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  const known = new Map(knownFields.map((k) => [k.toLowerCase(), k] as const));
  for (const f of failures) {
    const [first] = splitPropertyPath(f.propertyName);
    // camelCase JSON names are expected, but "LocalPath" / "localpath" name the same field.
    const key = typeof first === 'string' ? (known.get(first.toLowerCase()) ?? '') : '';
    (out[key] ??= []).push(f.errorMessage);
  }
  return out;
}
