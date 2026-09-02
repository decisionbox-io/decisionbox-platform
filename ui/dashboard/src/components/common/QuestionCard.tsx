'use client';

import { useState } from 'react';
import {
  Button, Checkbox, Group, Radio, SegmentedControl, Text, Textarea,
} from '@mantine/core';
import { notifications } from '@mantine/notifications';
import { api, DiscoveryQuestion, QuestionAnswerPayload } from '@/lib/api';

const OTHER_ID = '__other';

// answerTypeLabel is a short human tag shown on each card so the analyst knows
// how they'll answer before reading the controls.
function answerTypeLabel(t: DiscoveryQuestion['answer_type']): string {
  switch (t) {
    case 'boolean': return 'Yes / No';
    case 'single_choice': return 'Pick one';
    case 'multi_choice': return 'Pick any';
    default: return 'Open answer';
  }
}

interface QuestionCardProps {
  projectId: string;
  question: DiscoveryQuestion;
  // onResolved fires after a successful answer or dismiss so the parent can drop
  // the card and update its pending count.
  onResolved: (id: string) => void;
  // onLinkClick scrolls to the finding the question is about, when present.
  onLinkClick?: (target: DiscoveryQuestion['linked_target']) => void;
}

export default function QuestionCard({ projectId, question, onResolved, onLinkClick }: QuestionCardProps) {
  const [bool, setBool] = useState<string | null>(null);
  const [single, setSingle] = useState<string | null>(null);
  const [multi, setMulti] = useState<string[]>([]);
  const [text, setText] = useState('');
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);

  const otherSelected =
    (question.answer_type === 'single_choice' && single === OTHER_ID) ||
    (question.answer_type === 'multi_choice' && multi.includes(OTHER_ID));

  // Options as sent by the server already include the "__other" escape.
  const options = question.options ?? [];

  const buildPayload = (): QuestionAnswerPayload | string => {
    switch (question.answer_type) {
      case 'boolean':
        if (bool === null) return 'Choose Yes or No.';
        return { answer_bool: bool === 'yes', answer_note: note.trim() || undefined };
      case 'single_choice':
        if (!single) return 'Pick an option.';
        if (single === OTHER_ID && !note.trim()) return 'Add a note for "Other".';
        return { answer_option_ids: [single], answer_note: note.trim() || undefined };
      case 'multi_choice':
        if (multi.length === 0) return 'Pick at least one option.';
        if (multi.includes(OTHER_ID) && !note.trim()) return 'Add a note for "Other".';
        return { answer_option_ids: multi, answer_note: note.trim() || undefined };
      default:
        if (!text.trim()) return 'Type an answer.';
        return { answer: text.trim() };
    }
  };

  const submit = async () => {
    const payload = buildPayload();
    if (typeof payload === 'string') {
      notifications.show({ message: payload, color: 'yellow' });
      return;
    }
    setBusy(true);
    try {
      await api.answerQuestion(projectId, question.id, payload);
      notifications.show({ message: 'Answer saved — it will inform the next run.', color: 'green' });
      onResolved(question.id);
    } catch (e) {
      notifications.show({ title: 'Could not save answer', message: (e as Error).message, color: 'red' });
      setBusy(false);
    }
  };

  const dismiss = async () => {
    setBusy(true);
    try {
      await api.dismissQuestion(projectId, question.id);
      onResolved(question.id);
    } catch (e) {
      notifications.show({ title: 'Could not dismiss', message: (e as Error).message, color: 'red' });
      setBusy(false);
    }
  };

  return (
    <div style={{
      border: '1px solid var(--db-border)', borderRadius: 'var(--db-radius)',
      padding: 14, marginBottom: 10, background: 'var(--db-bg-surface)',
    }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', gap: 12 }}>
        <div style={{ fontSize: 14, fontWeight: 500, color: 'var(--db-text-primary)' }}>{question.question}</div>
        <span style={{
          fontSize: 11, whiteSpace: 'nowrap', padding: '1px 7px', borderRadius: 'var(--db-radius)',
          background: 'var(--db-bg-muted)', color: 'var(--db-text-secondary)', height: 'fit-content',
        }}>{answerTypeLabel(question.answer_type)}</span>
      </div>
      {question.rationale && (
        <div style={{ fontSize: 12, color: 'var(--db-text-tertiary)', marginTop: 4 }}>
          {question.rationale}
          {onLinkClick && question.linked_target?.id && (
            <>
              {' · '}
              <Text component="span" size="xs" c="blue" style={{ cursor: 'pointer' }}
                onClick={() => onLinkClick(question.linked_target)}>
                view {question.linked_target.type}
              </Text>
            </>
          )}
        </div>
      )}

      <div style={{ marginTop: 12 }}>
        {question.answer_type === 'boolean' && (
          <SegmentedControl
            value={bool ?? ''}
            onChange={setBool}
            data={[{ label: 'Yes', value: 'yes' }, { label: 'No', value: 'no' }]}
            size="xs"
          />
        )}

        {question.answer_type === 'single_choice' && (
          <Radio.Group value={single} onChange={setSingle}>
            <Group gap={6} style={{ flexDirection: 'column', alignItems: 'flex-start' }}>
              {options.map((o) => <Radio key={o.id} value={o.id} label={o.label} size="xs" />)}
            </Group>
          </Radio.Group>
        )}

        {question.answer_type === 'multi_choice' && (
          <Checkbox.Group value={multi} onChange={setMulti}>
            <Group gap={6} style={{ flexDirection: 'column', alignItems: 'flex-start' }}>
              {options.map((o) => <Checkbox key={o.id} value={o.id} label={o.label} size="xs" />)}
            </Group>
          </Checkbox.Group>
        )}

        {(question.answer_type === 'free_text' || otherSelected) && (
          <Textarea
            mt={question.answer_type === 'free_text' ? 0 : 8}
            placeholder={question.answer_type === 'free_text' ? 'Your answer…' : 'Add a note…'}
            value={question.answer_type === 'free_text' ? text : note}
            onChange={(e) => (question.answer_type === 'free_text' ? setText : setNote)(e.currentTarget.value)}
            autosize minRows={2} size="xs"
          />
        )}

        {/* An optional nuance note on a boolean answer ("Yes, but only after 2025-01"). */}
        {question.answer_type === 'boolean' && (
          <Textarea mt={8} placeholder="Optional note…" value={note}
            onChange={(e) => setNote(e.currentTarget.value)} autosize minRows={1} size="xs" />
        )}
      </div>

      <Group gap={8} mt={12}>
        <Button size="xs" onClick={submit} loading={busy}>Submit</Button>
        <Button size="xs" variant="subtle" color="gray" onClick={dismiss} disabled={busy}>Dismiss</Button>
      </Group>
    </div>
  );
}
