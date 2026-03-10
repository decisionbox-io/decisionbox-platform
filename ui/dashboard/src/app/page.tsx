'use client';

import { useEffect, useState } from 'react';
import { Alert, Badge, Button, Card, Group, SimpleGrid, Stack, Text, Title } from '@mantine/core';
import { IconAlertCircle, IconPlus, IconBrain } from '@tabler/icons-react';
import Link from 'next/link';
import Shell from '@/components/layout/AppShell';
import { api, Project } from '@/lib/api';

export default function ProjectsPage() {
  const [projects, setProjects] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api.listProjects()
      .then(setProjects)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  return (
    <Shell>
      <Stack gap="lg">
        <Group justify="space-between">
          <Title order={2}>Projects</Title>
          <Button component={Link} href="/projects/new" leftSection={<IconPlus size={16} />}>
            New Project
          </Button>
        </Group>

        {error && (
          <Alert icon={<IconAlertCircle size={16} />} title="Connection Error" color="red" variant="light">
            {error}
          </Alert>
        )}

        {loading && <Text c="dimmed">Loading projects...</Text>}

        {!loading && !error && projects.length === 0 && (
          <Card withBorder p="xl" ta="center">
            <Stack align="center" gap="md">
              <IconBrain size={48} color="var(--mantine-color-gray-5)" />
              <Title order={3} c="dimmed">No projects yet</Title>
              <Text c="dimmed">Create your first project to start discovering insights.</Text>
              <Button component={Link} href="/projects/new" leftSection={<IconPlus size={16} />}>
                Create Project
              </Button>
            </Stack>
          </Card>
        )}

        <SimpleGrid cols={{ base: 1, sm: 2, lg: 3 }}>
          {projects.map((project) => (
            <Card key={project.id} withBorder shadow="sm" radius="md" component={Link} href={`/projects/${project.id}`}
              style={{ textDecoration: 'none', cursor: 'pointer' }}>
              <Group justify="space-between" mb="xs">
                <Text fw={600}>{project.name}</Text>
                <Badge color={project.status === 'active' ? 'green' : 'gray'} variant="light">
                  {project.status}
                </Badge>
              </Group>

              <Text size="sm" c="dimmed" mb="sm">
                {project.domain} / {project.category}
              </Text>

              {project.description && (
                <Text size="sm" c="dimmed" lineClamp={2} mb="sm">
                  {project.description}
                </Text>
              )}

              <Group gap="xs">
                <Badge variant="outline" size="sm">{project.warehouse.provider}</Badge>
                <Badge variant="outline" size="sm">{project.llm.provider}</Badge>
              </Group>

              {project.last_run_at && (
                <Text size="xs" c="dimmed" mt="sm">
                  Last run: {new Date(project.last_run_at).toLocaleDateString()}
                </Text>
              )}
            </Card>
          ))}
        </SimpleGrid>
      </Stack>
    </Shell>
  );
}
