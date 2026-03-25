import NextAuth from 'next-auth';

// Auth.js configuration for OIDC authentication.
// Supports any OIDC-compliant IdP (Auth0, Okta, Entra ID, Google, Keycloak, etc.)
// via environment variables — no code changes needed per provider.
export const { handlers, signIn, signOut, auth } = NextAuth({
  providers: [
    {
      id: 'oidc',
      name: 'SSO',
      type: 'oidc',
      issuer: process.env.AUTH_ISSUER_URL,
      clientId: process.env.AUTH_CLIENT_ID,
      clientSecret: process.env.AUTH_CLIENT_SECRET,
    },
  ],
  callbacks: {
    async jwt({ token, account }) {
      // On initial sign-in, store the IdP id_token for API calls
      if (account) {
        token.idToken = account.id_token;
      }
      return token;
    },
    async session({ session, token }) {
      // Make idToken available in the session for API proxy
      session.idToken = token.idToken as string;
      return session;
    },
  },
  pages: {
    signIn: '/login',
  },
});
