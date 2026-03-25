import NextAuth from 'next-auth';

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
          // The id_token audience is the client_id, which doesn't match
          // the API's expected audience — so we use access_token instead.
          audience: process.env.AUTH_AUDIENCE,
        },
      },
    },
  ],
  callbacks: {
    async jwt({ token, account }) {
      // On initial sign-in, store the access_token for API calls
      if (account) {
        token.accessToken = account.access_token;
      }
      return token;
    },
    async session({ session, token }) {
      // Make accessToken available in the session for API proxy
      session.accessToken = token.accessToken as string;
      return session;
    },
  },
  pages: {
    signIn: '/login',
  },
});
