import { afterEach, describe, expect, it } from 'vitest';
import { safeReturnUrl } from './AuthGate';

afterEach(() => {
  delete window.Dupearr;
});

describe('safeReturnUrl', () => {
  it.each([
    ['/duplicates', '/duplicates'],
    ['/duplicates?page=2&sort=size', '/duplicates?page=2&sort=size'],
    ['/duplicate/12#files', '/duplicate/12#files'],
    ['/settings/general', '/settings/general'],
    ['/', '/'],
  ])('keeps the local path %j', (input, expected) => {
    expect(safeReturnUrl(input)).toBe(expected);
  });

  it.each([null, undefined, '', 'duplicates', 'http://evil.example/', 'https://evil.example', 'javascript:alert(1)'])(
    'falls back to / for %j',
    (input) => {
      expect(safeReturnUrl(input)).toBe('/');
    },
  );

  it.each(['//evil.example', '//evil.example/x', '/\\evil.example', '/\\/evil.example'])(
    'refuses the protocol-relative %j',
    (input) => {
      expect(safeReturnUrl(input)).toBe('/');
    },
  );

  // SEC-023: the URL parser strips ASCII tab/CR/LF and treats "\" like "/", so each of these
  // resolves to another origin when assigned to window.location.
  it.each([
    ['%2F%09%2Fevil.example', '/\t/evil.example'],
    ['%2F%0A%2Fevil.example', '/\n/evil.example'],
    ['%2F%0D%2Fevil.example', '/\r/evil.example'],
    ['%2F%09%5Cevil.example', '/\t\\evil.example'],
    ['%2F%5C%09evil.example', '/\\\tevil.example'],
  ])('refuses %s (decoded %j)', (encoded, decoded) => {
    expect(decodeURIComponent(encoded)).toBe(decoded);
    // Proof that the raw value would leave the origin.
    expect(new URL(decoded, 'http://tower:3873/login').origin).not.toBe('http://tower:3873');
    expect(safeReturnUrl(decoded)).toBe('/');
  });

  it.each(['/duplicates\u0000', '/\u001fx', '/x\u007f', '/dup\\licates'])(
    'refuses control characters and backslashes anywhere (%j)',
    (input) => {
      expect(safeReturnUrl(input)).toBe('/');
    },
  );

  // Dot segments collapse to an empty first segment: "/.//evil.example" has the pathname
  // "//evil.example", which must never be handed on as a (protocol-relative) URL.
  it.each(['/.//evil.example', '/a/..//evil.example', '/%2e//evil.example', '/%2E%2E//evil.example', '/a\\..\\\\evil.example'])(
    'refuses %j that normalizes to a protocol-relative path',
    (input) => {
      expect(safeReturnUrl(input)).toBe('/');
    },
  );

  it.each(['/login', '/login?returnUrl=%2F', '/setup', '/a/../login', '/%2e%2e/setup', '/./login'])(
    'never returns to the login/setup page (%j)',
    (input) => {
      expect(safeReturnUrl(input)).toBe('/');
    },
  );

  it('refuses the login/setup page with the url base prefixed', () => {
    window.Dupearr = { urlBase: '/dupearr' };
    expect(safeReturnUrl('/dupearr/login')).toBe('/');
    expect(safeReturnUrl('/dupearr/setup?x=1')).toBe('/');
    expect(safeReturnUrl('/duplicates')).toBe('/duplicates');
  });

  it('always returns a same-origin, path-absolute value', () => {
    const inputs = [
      '/\t/evil.example',
      '/.//evil.example',
      '/%09/evil.example',
      '/ /evil.example',
      '/duplicates',
      '/x?y=//evil.example#//evil.example',
    ];
    for (const input of inputs) {
      const out = safeReturnUrl(input);
      expect(out.startsWith('/')).toBe(true);
      expect(out.startsWith('//')).toBe(false);
      expect(new URL(out, 'http://tower:3873/login').origin).toBe('http://tower:3873');
    }
  });
});
