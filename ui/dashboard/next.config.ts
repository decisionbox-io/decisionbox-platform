import type { NextConfig } from "next";
import createNextIntlPlugin from "next-intl/plugin";
import { version as pkgVersion } from "./package.json";

// Build metadata for the System page. The Docker build sets
// DASHBOARD_VERSION / DASHBOARD_BUILD_DATE; outside Docker (local
// `npm run build`) the version falls back to package.json. These are
// inlined into the client bundle as NEXT_PUBLIC_* at build time.
const nextConfig: NextConfig = {
  output: "standalone",
  env: {
    NEXT_PUBLIC_DASHBOARD_VERSION: process.env.DASHBOARD_VERSION || pkgVersion,
    // "unknown" mirrors the Go images' unstamped default so every
    // component reports a build date consistently on the System page.
    NEXT_PUBLIC_DASHBOARD_BUILD_DATE: process.env.DASHBOARD_BUILD_DATE || "unknown",
  },
};

// next-intl: point the plugin at the request-scoped config (locale + messages).
// The enterprise dashboard reuses this community next.config.ts, so wiring it
// here covers both builds.
const withNextIntl = createNextIntlPlugin("./src/i18n/request.ts");

export default withNextIntl(nextConfig);
