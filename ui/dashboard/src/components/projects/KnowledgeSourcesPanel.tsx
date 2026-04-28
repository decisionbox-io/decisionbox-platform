'use client';

/**
 * Community-build placeholder for the enterprise knowledge-sources
 * panel. The real implementation lives in
 * `decisionbox-enterprise/ui/src/components/projects/KnowledgeSourcesPanel.tsx`
 * and replaces this file at Docker-build time via the enterprise
 * overlay rsync (community first, enterprise on top).
 *
 * Why a stub instead of a missing module: the pack-gen wizard
 * dynamically imports this path at runtime
 * (`import('@/components/projects/KnowledgeSourcesPanel')`). When the
 * file is missing entirely Turbopack resolves the import statically
 * and fails the production build with a "Module not found" error,
 * even though the runtime import is wrapped in try/catch. Shipping a
 * no-op default keeps community Turbopack builds green; enterprise
 * builds get the real component because the overlay overwrites this
 * file before `next build` runs.
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
        <Alert color="blue" icon={<IconAlertCircle size={16} />} title="Knowledge sources plugin not installed">
          This deployment ships the community build; the knowledge-sources plugin lives in the enterprise overlay.
          Pack generation will run, but without the source-text context the result will rely on the warehouse
          schema alone.
        </Alert>
      </Stack>
    </Card>
  );
}
