import { NextResponse } from 'next/server';

// Logout route — redirects to IdP logout endpoint to clear the IdP session.
// Without this, the IdP retains its session cookie and automatically
// re-authenticates the user on the next sign-in attempt.
//
// Configure via AUTH_LOGOUT_URL — the full IdP logout endpoint:
//   Auth0:    https://your-tenant.auth0.com/v2/logout
//   Okta:     https://your-org.okta.com/oauth2/v1/logout
//   Entra ID: https://login.microsoftonline.com/{tenant}/oauth2/v2.0/logout
//   Keycloak: https://keycloak.example.com/realms/{realm}/protocol/openid-connect/logout
//
// The route appends both standard OIDC (post_logout_redirect_uri) and
// Auth0-specific (returnTo, client_id) parameters for maximum compatibility.
export async function GET() {
  const logoutUrl = process.env.AUTH_LOGOUT_URL;
  const clientId = process.env.AUTH_CLIENT_ID || '';
  const baseUrl = process.env.NEXTAUTH_URL || 'http://localhost:3000';
  const returnTo = `${baseUrl}/login`;

  if (logoutUrl) {
    const url = new URL(logoutUrl);
    // OIDC RP-Initiated Logout standard (Okta, Entra ID, Keycloak)
    url.searchParams.set('post_logout_redirect_uri', returnTo);
    url.searchParams.set('client_id', clientId);
    // Auth0 uses 'returnTo' instead of 'post_logout_redirect_uri'
    url.searchParams.set('returnTo', returnTo);
    return NextResponse.redirect(url.toString());
  }

  // No IdP logout URL configured — redirect to local login page
  return NextResponse.redirect(returnTo);
}
