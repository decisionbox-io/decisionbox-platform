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

interface Props {
  projectId: string;
  variant: 'page' | 'wizard';
  intro?: string;
  onReadyChange?: (ready: boolean) => void;
}

export default function KnowledgeSourcesPanel(_props: Props) {
  return (
    <Card withBorder p="lg">
      <Stack>
        <div>
          <IconUpload size={18} style={{ verticalAlign: 'middle' }} />{' '}
          <Title order={5} component="span">Knowledge sources</Title>
        </div>
        <Text size="sm" c="dimmed">
          Upload website URLs, DOCX/XLSX/CSV/MD/TXT files, or paste free-text notes describing your business.
        </Text>
        <Alert color="blue" icon={<IconAlertCircle size={16} />} title="Knowledge sources not configured">
          No knowledge-sources provider is registered on this deployment. Pack generation will run, but
          without the source-text context the result will rely on the warehouse schema alone.
        </Alert>
      </Stack>
    </Card>
  );
}
