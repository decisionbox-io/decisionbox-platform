import 'next-auth';

declare module 'next-auth' {
  interface Session {
    apiToken?: string;
  }
}

declare module 'next-auth/jwt' {
  interface JWT {
    apiToken?: string;
  }
}
