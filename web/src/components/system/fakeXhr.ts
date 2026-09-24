/**
 * Minimal scriptable XMLHttpRequest stand-in for upload tests (test-only; never imported by
 * application code). Install with `vi.stubGlobal('XMLHttpRequest', FakeXhr)`.
 */
export class FakeXhr {
  static last: FakeXhr | null = null;
  method = '';
  url = '';
  headers: Record<string, string> = {};
  withCredentials = false;
  status = 0;
  statusText = '';
  responseText = '';
  responseHeaders: Record<string, string> = {};
  body: unknown;
  upload: {
    onprogress: ((e: { lengthComputable: boolean; loaded: number; total: number }) => void) | null;
    onload: (() => void) | null;
  } = { onprogress: null, onload: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  ontimeout: (() => void) | null = null;
  onabort: (() => void) | null = null;
  aborted = false;

  constructor() {
    FakeXhr.last = this;
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader(k: string, v: string) {
    this.headers[k] = v;
  }
  getResponseHeader(k: string) {
    return this.responseHeaders[k.toLowerCase()] ?? null;
  }
  send(body: unknown) {
    this.body = body;
  }
  abort() {
    this.aborted = true;
    this.onabort?.();
  }
  /** Simulates the whole body being sent (progress events + upload load). */
  sendAll(total: number) {
    this.upload.onprogress?.({ lengthComputable: true, loaded: total, total });
    this.upload.onload?.();
  }
  respond(status: number, text: string, contentType = 'application/json') {
    this.status = status;
    this.responseText = text;
    this.responseHeaders['content-type'] = contentType;
    this.onload?.();
  }
}
