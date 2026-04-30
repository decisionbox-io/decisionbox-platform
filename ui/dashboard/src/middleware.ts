import { NextRequest, NextResponse } from 'next/server';
import { Agent, fetch as undiciFetch } from 'undici';

// Runtime API proxy — reads API_URL env var at request time (not build
// time). Proxies all /api/* requests to the backend API server-side
// so the browser never talks to the API directly.
//
// Configuration via env var:
//   API_URL=http://decisionbox-api:8080  (K8s cluster-internal)
//   API_URL=http://localhost:8080         (local dev, default)
//
// Long-running requests:
//
// Node's global fetch is undici, which defaults headersTimeout and
// bodyTimeout to 5 minutes. That cap kills long synchronous endpoints
// (pack-generation synth on Opus + a 270k-char prompt routinely
// runs 5–8 minutes). The dispatcher below raises both caps to 20
// minutes — a safety belt while the synchronous endpoints are still
// in use; an async-job migration of pack-gen will eliminate the need
// for this entirely. Browser-side fetch has no comparable timeout, so
// raising the proxy is sufficient end-to-end.
const proxyTimeoutMs = 20 * 60_000;
const longRunningProxy = new Agent({
  headersTimeout: proxyTimeoutMs,
  bodyTimeout: proxyTimeoutMs,
});

// Force Node.js runtime so the undici Agent is usable. Edge runtime
// uses a different fetch implementation that doesn't honour the
// dispatcher option.
export const runtime = 'nodejs';

export async function middleware(request: NextRequest) {
  const { pathname, search } = request.nextUrl;

  // Only proxy /api/* requests (not /health or other dashboard routes)
  if (!pathname.startsWith('/api/')) {
    return NextResponse.next();
  }

  const apiUrl = process.env.API_URL || 'http://localhost:8080';
  const targetUrl = `${apiUrl}${pathname}${search}`;

  // Forward the request to the backend API. Use undici directly with
  // the long-timeout dispatcher so headersTimeout / bodyTimeout
  // accommodate multi-minute LLM calls.
  const headers = new Headers(request.headers);
  // Remove host header (will be set by fetch to the target)
  headers.delete('host');

  // undici's BodyInit type does not unify with the global Web
  // ReadableStream type the way Node's global fetch does, so the
  // request body crosses the boundary as `unknown`. Same shape, just
  // missing the structural matchup TS needs.
  const response = await undiciFetch(targetUrl, {
    method: request.method,
    headers: Object.fromEntries(headers),
    body: request.body as unknown as undefined,
    duplex: 'half',
    dispatcher: longRunningProxy,
  });

  // Forward the response back to the client
  const responseHeaders = new Headers(
    Object.fromEntries(response.headers.entries()),
  );
  // Remove transfer-encoding to avoid issues with Next.js
  responseHeaders.delete('transfer-encoding');

  return new NextResponse(response.body as unknown as ReadableStream | null, {
    status: response.status,
    statusText: response.statusText,
    headers: responseHeaders,
  });
}

export const config = {
  // Only run middleware on /api/* paths
  matcher: '/api/:path*',
};
