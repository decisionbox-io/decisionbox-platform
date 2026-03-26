import NextAuth from 'next-auth';

// Which token to send to the API for authentication:
//   "access_token" (default) — JWT access token with custom API audience.
//     Works with: Auth0, Okta, Entra ID, Keycloak, AWS Cognito.
//     Requires AUTH_AUDIENCE to be set.
//   "id_token" — JWT id_token with client_id as audience.
//     Works with: Google (opaque access tokens), any OIDC provider.
//     Set AUTH_AUDIENCE to the client_id on the API side.
const tokenType = process.env.AUTH_TOKEN_TYPE || 'access_token';

// Auth.js configuration for OIDC authentication.
// Supports any OIDC-compliant IdP (Auth0, Okta, Entra ID, Google, Keycloak, etc.)
// via environment variables — no code changes needed per provider.
//
// IdP logout is handled separately via /api/auth/logout route,
// configured with AUTH_LOGOUT_URL env var (e.g., https://tenant.auth0.com/v2/logout).
export const { handlers, signIn, signOut, auth } = NextAuth({
  providers: [
    {
      id: 'oidc',
      name: 'SSO',
      type: 'oidc',
      issuer: process.env.AUTH_ISSUER_URL,
      clientId: process.env.AUTH_CLIENT_ID,
      clientSecret: process.env.AUTH_CLIENT_SECRET,
      authorization: {
        params: {
          // Request an access_token scoped to the API audience.
          // Only set when using access_token flow (Auth0, Okta, Entra ID).
          // Google ignores this parameter and issues opaque access tokens.
          ...(tokenType === 'access_token' && process.env.AUTH_AUDIENCE
            ? { audience: process.env.AUTH_AUDIENCE }
            : {}),
        },
      },
    },
  ],
  callbacks: {
    async jwt({ token, account }) {
      if (account) {
        token.apiToken = tokenType === 'id_token'
          ? account.id_token
          : account.access_token;
      }
      return token;
    },
    async session({ session, token }) {
      session.apiToken = token.apiToken as string;
      return session;
    },
  },
  pages: {
    signIn: '/login',
  },
});
