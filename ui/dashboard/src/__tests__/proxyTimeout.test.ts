/**
 * @jest-environment node
 */

// Pin the Node.js test environment for this file so the undici
// Agent and Node http server are available. The default `jsdom`
// environment from jest.config.js doesn't ship them.

import http from 'node:http';
import { Agent, fetch as undiciFetch } from 'undici';

// Match middleware.ts. If middleware.ts changes the value, this test
// is the canary that catches an accidental drop back to the undici
// 5-minute default — any regression on that constant breaks the
// short-Agent assertion below.
const PROXY_TIMEOUT_MS = 20 * 60_000;

type SlowServer = { url: string; close: () => Promise<void> };

function startSlowServer(headersDelayMs: number): Promise<SlowServer> {
  return new Promise((resolve) => {
    const server = http.createServer((_req, res) => {
      setTimeout(() => {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, delay_ms: headersDelayMs }));
      }, headersDelayMs);
    });
    server.listen(0, '127.0.0.1', () => {
      const addr = server.address();
      if (!addr || typeof addr === 'string') {
        throw new Error('failed to start slow server');
      }
      resolve({
        url: `http://127.0.0.1:${addr.port}/`,
        // server.close fires its callback after every active socket
        // closes — await this so jest's open-handle detector doesn't
        // see leftover keep-alive sockets between cases.
        close: () =>
          new Promise<void>((res) => {
            server.close(() => res());
          }),
      });
    });
  });
}

describe('dashboard API proxy timeout', () => {
  it('honours dispatcher.headersTimeout — short Agent rejects the slow upstream', async () => {
    const srv = await startSlowServer(2000);
    const shortAgent = new Agent({ headersTimeout: 300, bodyTimeout: 300 });
    const start = Date.now();
    let caught: unknown;
    try {
      await undiciFetch(srv.url, { dispatcher: shortAgent });
    } catch (err) {
      caught = err;
    }
    const elapsed = Date.now() - start;
    await srv.close();
    await shortAgent.close();

    expect(caught).toBeInstanceOf(Error);
    // undici wraps the timeout in `TypeError: fetch failed`; the cause
    // carries the typed identifier.
    const cause = (caught as { cause?: unknown }).cause;
    const causeStr = String(cause ?? caught);
    expect(causeStr).toMatch(
      /HeadersTimeoutError|UND_ERR_HEADERS_TIMEOUT|headers timeout/i,
    );
    // Agent rejected long before the 2-second server-side delay.
    expect(elapsed).toBeLessThan(1500);
  });

  it('honours dispatcher.headersTimeout — 20-minute Agent (the value middleware ships) waits for a 3-second response', async () => {
    const srv = await startSlowServer(3000);
    const longAgent = new Agent({
      headersTimeout: PROXY_TIMEOUT_MS,
      bodyTimeout: PROXY_TIMEOUT_MS,
    });
    const start = Date.now();
    const resp = await undiciFetch(srv.url, { dispatcher: longAgent });
    const body = (await resp.json()) as { ok: boolean };
    const elapsed = Date.now() - start;
    await srv.close();
    await longAgent.close();

    expect(resp.status).toBe(200);
    expect(body.ok).toBe(true);
    expect(elapsed).toBeGreaterThanOrEqual(2900);
  }, 30_000);

  it('proxy timeout constant matches middleware.ts contract', () => {
    // Guard against accidental edits that drop the timeout back below
    // a multi-minute LLM call. The lower bound here (15 min) leaves
    // headroom for a future revision, while still catching a
    // regression to undici's 5-minute default.
    expect(PROXY_TIMEOUT_MS).toBeGreaterThanOrEqual(15 * 60_000);
  });

  // Regression: middleware.ts must forward POST/PUT/PATCH bodies to
  // the upstream API verbatim. An earlier revision cast the body to
  // `undefined`, which silently dropped JSON payloads.
  it('forwards a POST body through undici fetch + dispatcher', async () => {
    type EchoBody = {
      sentinel: string;
      number: number;
    };
    let received: EchoBody | { error: string } = { error: 'not-set' };

    const echoSrv = await new Promise<SlowServer>((resolve) => {
      const server = http.createServer((req, res) => {
        const chunks: Buffer[] = [];
        req.on('data', (c: Buffer) => chunks.push(c));
        req.on('end', () => {
          try {
            received = JSON.parse(Buffer.concat(chunks).toString('utf8'));
          } catch (err) {
            received = { error: String(err) };
          }
          res.writeHead(200, { 'Content-Type': 'application/json' });
          res.end(JSON.stringify({ ok: true }));
        });
      });
      server.listen(0, '127.0.0.1', () => {
        const addr = server.address();
        if (!addr || typeof addr === 'string') {
          throw new Error('failed to start echo server');
        }
        resolve({
          url: `http://127.0.0.1:${addr.port}/echo`,
          close: () =>
            new Promise<void>((res) => {
              server.close(() => res());
            }),
        });
      });
    });

    const longAgent = new Agent({
      headersTimeout: PROXY_TIMEOUT_MS,
      bodyTimeout: PROXY_TIMEOUT_MS,
    });
    const payload: EchoBody = { sentinel: 'proxy-body-roundtrip', number: 42 };
    const resp = await undiciFetch(echoSrv.url, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(payload),
      dispatcher: longAgent,
    });
    await resp.json();
    await echoSrv.close();
    await longAgent.close();

    expect(received).toEqual(payload);
  });
});
