import { NextResponse } from 'next/server';

// Logout route — redirects to IdP logout endpoint to clear the IdP session.
// Without this, the IdP retains its session cookie and automatically
// re-authenticates the user on the next sign-in attempt.
//
// Configure via: AUTH_LOGOUT_URL (e.g., https://your-tenant.auth0.com/v2/logout)
// The route appends ?client_id=...&returnTo=... query parameters automatically.
export async function GET() {
  const logoutUrl = process.env.AUTH_LOGOUT_URL;
  const clientId = process.env.AUTH_CLIENT_ID || '';
  const baseUrl = process.env.NEXTAUTH_URL || 'http://localhost:3000';
  const returnTo = `${baseUrl}/login`;

  if (logoutUrl) {
    const url = new URL(logoutUrl);
    url.searchParams.set('client_id', clientId);
    url.searchParams.set('returnTo', returnTo);
    return NextResponse.redirect(url.toString());
  }

  // No IdP logout URL configured — redirect to local login page
  return NextResponse.redirect(returnTo);
}
