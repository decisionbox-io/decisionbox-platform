'use client';

import { signIn } from 'next-auth/react';
import { Button, Center, Stack, Title, Text, Paper } from '@mantine/core';
import { IconLock } from '@tabler/icons-react';

export default function LoginPage() {
  return (
    <Center style={{ minHeight: '100vh' }}>
      <Paper shadow="sm" p="xl" radius="md" withBorder style={{ width: 400 }}>
        <Stack align="center" gap="lg">
          <IconLock size={48} stroke={1.5} />
          <Title order={2}>DecisionBox</Title>
          <Text c="dimmed" ta="center">
            Sign in to access the data discovery platform.
          </Text>
          <Button
            fullWidth
            size="lg"
            onClick={() => signIn('oidc', { callbackUrl: '/' })}
          >
            Sign in with SSO
          </Button>
        </Stack>
      </Paper>
    </Center>
  );
}
