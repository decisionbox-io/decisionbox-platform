import { NextResponse } from 'next/server';

const startTime = Date.now();

// GET /health — Dashboard health check for K8s liveness/readiness probes.
// Checks that the dashboard is alive and can reach the backend API.
export async function GET() {
  const uptimeSeconds = Math.floor((Date.now() - startTime) / 1000);
  const apiUrl = process.env.API_URL || 'http://localhost:8080';

  let apiStatus = 'unknown';
  try {
    const res = await fetch(`${apiUrl}/api/v1/health`, { signal: AbortSignal.timeout(3000) });
    apiStatus = res.ok ? 'ok' : `error (${res.status})`;
  } catch (e) {
    apiStatus = `unreachable (${(e as Error).message})`;
  }

  const healthy = apiStatus === 'ok';

  return NextResponse.json(
    {
      status: healthy ? 'ok' : 'degraded',
      uptime_seconds: uptimeSeconds,
      services: {
        api: { status: apiStatus },
      },
    },
    { status: healthy ? 200 : 503 },
  );
}
