'use client';

import { createContext, useContext, useState, useCallback, ReactNode } from 'react';
import { SeedContext } from '@/lib/api';

interface ChatDrawerState {
  open: boolean;
  projectId: string | null;
  seedContext?: SeedContext;
  initialQuestion?: string;
  // seedNonce bumps on every openWithSeed so the drawer's ChatPanel remounts a
  // fresh, seeded conversation; openGeneric leaves it untouched so the current
  // conversation resumes.
  seedNonce: number;
}

interface ChatDrawerContextValue extends ChatDrawerState {
  // openWithSeed starts a NEW conversation anchored to an insight / rec, with an
  // optional first question (a clicked suggested question).
  openWithSeed: (projectId: string, seed: SeedContext, initialQuestion?: string) => void;
  // openGeneric opens the drawer for a project-scoped chat with no seed; it
  // resumes the current conversation if the drawer is already mounted.
  openGeneric: (projectId: string) => void;
  close: () => void;
}

const ChatDrawerContext = createContext<ChatDrawerContextValue | null>(null);

export function ChatDrawerProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<ChatDrawerState>({
    open: false,
    projectId: null,
    seedNonce: 0,
  });

  const openWithSeed = useCallback((projectId: string, seed: SeedContext, initialQuestion?: string) => {
    setState(prev => ({
      open: true,
      projectId,
      seedContext: seed,
      initialQuestion,
      seedNonce: prev.seedNonce + 1,
    }));
  }, []);

  const openGeneric = useCallback((projectId: string) => {
    setState(prev => {
      // Switching to a different project starts a fresh generic chat; re-opening
      // for the same project resumes the current conversation.
      if (prev.projectId === projectId) {
        return { ...prev, open: true, seedContext: undefined, initialQuestion: undefined };
      }
      return {
        open: true,
        projectId,
        seedContext: undefined,
        initialQuestion: undefined,
        seedNonce: prev.seedNonce + 1,
      };
    });
  }, []);

  const close = useCallback(() => setState(prev => ({ ...prev, open: false })), []);

  return (
    <ChatDrawerContext.Provider value={{ ...state, openWithSeed, openGeneric, close }}>
      {children}
    </ChatDrawerContext.Provider>
  );
}

// useChatDrawer returns the drawer controls. Returns null when no provider is
// mounted (e.g. a route outside the app layout) so triggers can no-op safely.
export function useChatDrawer(): ChatDrawerContextValue | null {
  return useContext(ChatDrawerContext);
}
