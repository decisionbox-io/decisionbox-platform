'use client';

import { useState } from 'react';
import { ActionIcon, Group, Text, TextInput, Tooltip } from '@mantine/core';
import { IconThumbUp, IconThumbUpFilled, IconThumbDown, IconThumbDownFilled } from '@tabler/icons-react';
import { api, Feedback } from '@/lib/api';

interface FeedbackButtonsProps {
  projectId?: string;
  discoveryId: string;
  targetType: 'insight' | 'recommendation' | 'exploration_step';
  targetId: string;
  feedback?: Feedback | null;
  onUpdate?: (feedback: Feedback | null) => void;
}

export default function FeedbackButtons({ projectId, discoveryId, targetType, targetId, feedback, onUpdate }: FeedbackButtonsProps) {
  const [current, setCurrent] = useState<Feedback | null>(feedback || null);
  const [showComment, setShowComment] = useState(false);
  const [comment, setComment] = useState('');
  const [loading, setLoading] = useState(false);

  const handleVote = async (rating: 'like' | 'dislike') => {
    // If clicking the same rating, remove the vote
    if (current?.rating === rating) {
      if (!current.id) return;
      setLoading(true);
      try {
        await api.deleteFeedback(current.id);
        setCurrent(null);
        setShowComment(false);
        onUpdate?.(null);
      } catch { /* ignore */ }
      setLoading(false);
      return;
    }

    // If disliking, show comment input first
    if (rating === 'dislike' && !showComment) {
      setShowComment(true);
      return;
    }

    setLoading(true);
    try {
      const result = await api.submitFeedback(discoveryId, {
        project_id: projectId,
        target_type: targetType,
        target_id: targetId,
        rating,
        comment: rating === 'dislike' ? comment : undefined,
      });
      setCurrent(result);
      setShowComment(false);
      setComment('');
      onUpdate?.(result);
    } catch { /* ignore */ }
    setLoading(false);
  };

  return (
    <Group gap={4} wrap="nowrap" onClick={(e) => e.preventDefault()}>
      <Tooltip label="Useful" withArrow position="top">
        <ActionIcon variant="subtle" size="sm" color={current?.rating === 'like' ? 'green' : 'gray'}
          onClick={() => handleVote('like')} loading={loading && !showComment}>
          {current?.rating === 'like' ? <IconThumbUpFilled size={14} /> : <IconThumbUp size={14} />}
        </ActionIcon>
      </Tooltip>
      <Tooltip label="Not useful" withArrow position="top">
        <ActionIcon variant="subtle" size="sm" color={current?.rating === 'dislike' ? 'red' : 'gray'}
          onClick={() => handleVote('dislike')} loading={loading && showComment}>
          {current?.rating === 'dislike' ? <IconThumbDownFilled size={14} /> : <IconThumbDown size={14} />}
        </ActionIcon>
      </Tooltip>
      {showComment && !current?.rating && (
        <Group gap={4} wrap="nowrap">
          <TextInput size="xs" placeholder="What was wrong? (optional)" value={comment}
            onChange={(e) => setComment(e.currentTarget.value)}
            onKeyDown={(e) => { if (e.key === 'Enter') handleVote('dislike'); }}
            style={{ width: 200 }} />
          <Text size="xs" c="blue" style={{ cursor: 'pointer', whiteSpace: 'nowrap' }}
            onClick={() => handleVote('dislike')}>Submit</Text>
          <Text size="xs" c="dimmed" style={{ cursor: 'pointer' }}
            onClick={() => setShowComment(false)}>Cancel</Text>
        </Group>
      )}
    </Group>
  );
}
