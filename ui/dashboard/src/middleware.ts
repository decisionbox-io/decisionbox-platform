import { NextRequest, NextResponse } from 'next/server';
import { auth } from '@/lib/auth';

const authEnabled = process.env.AUTH_ENABLED === 'true';

// Runtime API proxy + auth enforcement.
// When AUTH_ENABLED=true: checks session, redirects to login, injects JWT.
// When AUTH_ENABLED=false: proxy only (current behavior), no auth checks.
export async function middleware(request: NextRequest) {
  const { pathname, search } = request.nextUrl;

  // Auth.js routes — always pass through
  if (pathname.startsWith('/api/auth/')) {
    return NextResponse.next();
  }

  // Login page — always accessible
  if (pathname === '/login') {
    return NextResponse.next();
  }

  // If auth enabled, check session for non-API routes
  if (authEnabled && !pathname.startsWith('/api/')) {
    const session = await auth();
    if (!session) {
      return NextResponse.redirect(new URL('/login', request.url));
    }
    return NextResponse.next();
  }

  // Only proxy /api/* requests (not /health or other dashboard routes)
  if (!pathname.startsWith('/api/')) {
    return NextResponse.next();
  }

  const apiUrl = process.env.API_URL || 'http://localhost:8080';
  const targetUrl = `${apiUrl}${pathname}${search}`;

  // Forward the request to the backend API
  const headers = new Headers(request.headers);
  // Remove host header (will be set by fetch to the target)
  headers.delete('host');

  // If auth enabled, inject JWT from session
  if (authEnabled) {
    const session = await auth();
    if (!session) {
      return NextResponse.json({ error: 'unauthorized' }, { status: 401 });
    }
    if (session.idToken) {
      headers.set('Authorization', `Bearer ${session.idToken}`);
    }
  }

  const response = await fetch(targetUrl, {
    method: request.method,
    headers,
    body: request.body,
    // @ts-expect-error duplex is needed for streaming request bodies
    duplex: 'half',
  });

  // Forward the response back to the client
  const responseHeaders = new Headers(response.headers);
  // Remove transfer-encoding to avoid issues with Next.js
  responseHeaders.delete('transfer-encoding');

  return new NextResponse(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: responseHeaders,
  });
}

export const config = {
  // Run on API routes, login, and all app pages
  matcher: ['/((?!_next/static|_next/image|favicon\\.ico|favicon\\.svg).*)'],
};
