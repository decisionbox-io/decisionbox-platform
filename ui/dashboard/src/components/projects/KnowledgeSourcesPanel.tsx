'use client';

/**
 * Default knowledge-sources panel renderer. Replaced at build time
 * when a downstream overlay ships a richer implementation. Callers
 * dynamically import this path at runtime; shipping a default keeps
 * the bundler resolution static so the production build never trips
 * a "Module not found" error.
 */

import { Alert, Card, Stack, Text, Title } from '@mantine/core';
import { IconAlertCircle, IconUpload } from '@tabler/icons-react';
import { useTranslations } from 'next-intl';

interface Props {
  projectId: string;
  variant: 'page' | 'wizard';
  intro?: string;
  onReadyChange?: (ready: boolean) => void;
}

export default function KnowledgeSourcesPanel(_props: Props) {
  const t = useTranslations('projectsMisc');
  return (
    <Card withBorder p="lg">
      <Stack>
        <div>
          <IconUpload size={18} style={{ verticalAlign: 'middle' }} />{' '}
          <Title order={5} component="span">{t('knowledgeSources')}</Title>
        </div>
        <Text size="sm" c="dimmed">
          {t('knowledgeSourcesUploadHelp')}
        </Text>
        <Alert color="blue" icon={<IconAlertCircle size={16} />} title={t('knowledgeSourcesNotConfiguredTitle')}>
          {t('knowledgeSourcesNotConfiguredBody')}
        </Alert>
      </Stack>
    </Card>
  );
}
