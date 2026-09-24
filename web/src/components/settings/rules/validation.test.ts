import { describe, expect, it } from 'vitest';
import { groupFieldErrors, isAbsolutePath, splitPropertyPath, validateGlob, validateRegex } from './validation';

describe('validateRegex (Go RE2 flavour)', () => {
  it.each([
    ['^Star Wars', null],
    ['(?i)remux', null],
    ['(?is)^a.b$', null],
    ['(?i:web)[-. ]?dl', null],
    ['(?P<group>\\w+)-x', null],
    ['\\\\1', null], // escaped backslash followed by "1" is a literal
    ['', 'A regular expression is required'],
    ['(unclosed', /^Invalid regular expression/],
    ['[a-', /^Invalid regular expression/],
    ['(?x)abc', /^Invalid regular expression/], // x is not an RE2 flag
    ['foo(?=bar)', /Look-around/],
    ['(?<!x)y', /Look-around/],
    ['(a)\\1', /Back-references/],
  ] as const)('%j', (pattern, expected) => {
    const got = validateRegex(pattern);
    if (expected === null) expect(got).toBeNull();
    else if (typeof expected === 'string') expect(got).toBe(expected);
    else expect(got).toMatch(expected);
  });
});

describe('validateGlob', () => {
  it.each([
    ['**/*Remux*', null],
    ['/data/{movies,tv}/**', null],
    ['**/[abc]*.mkv', null],
    ['**/\\[x', null], // escaped bracket
    ['', 'A pattern is required'],
    ['**/[abc', 'Unclosed "[" in pattern'],
    ['/a/{b,c', 'Unclosed "{" in pattern'],
    ['/a/b}', 'Unbalanced "}" in pattern'],
    ['/a/b]', 'Unbalanced "]" in pattern'],
  ] as const)('%j', (pattern, expected) => {
    expect(validateGlob(pattern)).toBe(expected);
  });
});

describe('isAbsolutePath', () => {
  it.each([
    ['/data/media', true],
    ['  /data ', true],
    ['C:\\media', true],
    ['d:/media', true],
    ['\\\\nas\\share', true],
    ['data/media', false],
    ['', false],
    ['~/media', false],
  ])('%j → %s', (path, expected) => {
    expect(isAbsolutePath(path)).toBe(expected);
  });
});

describe('property paths', () => {
  it('splits bracket and dotted paths', () => {
    expect(splitPropertyPath('Criteria[2].Patterns[0].Pattern')).toEqual(['criteria', 2, 'patterns', 0, 'pattern']);
    expect(splitPropertyPath('criteria.2.order')).toEqual(['criteria', 2, 'order']);
    expect(splitPropertyPath('')).toEqual([]);
    expect(splitPropertyPath(undefined)).toEqual([]);
  });

  it('groups flat field errors and collects unknown ones under ""', () => {
    expect(
      groupFieldErrors(
        [
          { propertyName: 'LocalPath', errorMessage: 'Does not exist' },
          { propertyName: 'remotePath', errorMessage: 'Required' },
          { propertyName: 'REMOTEPATH', errorMessage: 'Must not be a filesystem root' },
          { propertyName: 'other', errorMessage: 'Nope' },
          { propertyName: '', errorMessage: 'General' },
        ],
        ['remotePath', 'localPath'],
      ),
    ).toEqual({
      localPath: ['Does not exist'],
      remotePath: ['Required', 'Must not be a filesystem root'],
      '': ['Nope', 'General'],
    });
  });
});
