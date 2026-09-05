'use client';

import { useEffect, useState } from 'react';
import { useTranslations } from 'next-intl';
import { Alert, Stack, Text, Title } from '@mantine/core';
import { IconAlertCircle } from '@tabler/icons-react';
import Shell from '@/components/layout/AppShell';
import { SystemInventory, withDashboardSelf } from '@/components/system/SystemInventory';
import { api, SystemComponent } from '@/lib/api';

// The running dashboard's own build — baked in at build time (see
// next.config.ts). It is the one component the API cannot report, since
// the dashboard ships as a separate image.
const dashboardSelf: SystemComponent = {
  name: 'Dashboard',
  kind: 'service',
  version: process.env.NEXT_PUBLIC_DASHBOARD_VERSION || 'dev',
  build_date: process.env.NEXT_PUBLIC_DASHBOARD_BUILD_DATE || undefined,
};

export default function SystemPage() {
  const t = useTranslations('systemSearch');
  const [components, setComponents] = useState<SystemComponent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.getSystemInfo()
      .then((info) => setComponents(info.components ?? []))
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  return (
    <Shell breadcrumb={[{ label: t('title') }]}>
      <Stack gap="lg">
        <div>
          <Title order={2}>{t('title')}</Title>
          <Text c="dimmed" size="sm" mt={4}>
            {t('subtitle')}
          </Text>
        </div>

        {error && (
          <Alert icon={<IconAlertCircle size={16} />} title={t('connectionError')} color="red" variant="light">
            {error}
          </Alert>
        )}

        {loading && <Text c="dimmed">{t('loading')}</Text>}

        {!loading && !error && (
          <SystemInventory components={withDashboardSelf(components, dashboardSelf)} />
        )}
      </Stack>
    </Shell>
  );
}
