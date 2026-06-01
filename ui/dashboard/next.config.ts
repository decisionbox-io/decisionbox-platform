import type { NextConfig } from "next";
import { version as pkgVersion } from "./package.json";

// Build metadata for the System page. The Docker build sets
// DASHBOARD_VERSION / DASHBOARD_BUILD_DATE; outside Docker (local
// `npm run build`) the version falls back to package.json. These are
// inlined into the client bundle as NEXT_PUBLIC_* at build time.
const nextConfig: NextConfig = {
  output: "standalone",
  env: {
    NEXT_PUBLIC_DASHBOARD_VERSION: process.env.DASHBOARD_VERSION || pkgVersion,
    NEXT_PUBLIC_DASHBOARD_BUILD_DATE: process.env.DASHBOARD_BUILD_DATE || "",
  },
};

export default nextConfig;
