package service

import (
	"context"
	"time"

	"removed-messages/internal/storage"
)

const pendingDeleteBatchSize = 100

func (s *BusinessMessageService) RunPendingDeleteLoop(ctx context.Context) {
	if s == nil {
		return
	}

	tick := s.pendingDeleteSweepTick
	if tick <= 0 {
		tick = 2 * time.Second
	}

	s.logger.Infof("pending delete reconcile loop started interval=%s", tick)
	defer s.logger.Infof("pending delete reconcile loop stopped")

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcilePendingDeletes(ctx, time.Now().UTC())
		}
	}
}

func (s *BusinessMessageService) reconcilePendingDeleteIfExists(ctx context.Context, key deleteKey, trigger string) {
	pending, err := s.repo.GetPendingDelete(ctx, key.businessConnectionID, key.sourceChatID, key.sourceMessageID)
	if err != nil {
		s.logger.Warnf("pending delete lookup failed source_chat_id=%d source_message_id=%d trigger=%s: %v", key.sourceChatID, key.sourceMessageID, trigger, err)
		return
	}
	if pending == nil {
		return
	}

	result, err := s.processDelete(ctx, key)
	if err != nil {
		s.logger.Warnf("pending delete reconcile failed source_chat_id=%d source_message_id=%d trigger=%s: %v", key.sourceChatID, key.sourceMessageID, trigger, err)
		s.rescheduleStoredPendingDelete(ctx, *pending, time.Now().UTC(), false, err.Error())
		return
	}

	switch result {
	case deleteProcessProcessed, deleteProcessAlreadyProcessed:
		s.removePendingDelete(ctx, key)
		s.logger.Infof("pending delete resolved source_chat_id=%d source_message_id=%d trigger=%s result=%s", key.sourceChatID, key.sourceMessageID, trigger, result)
	case deleteProcessMissingMessage:
		s.rescheduleStoredPendingDelete(ctx, *pending, time.Now().UTC(), false, "metadata_not_found")
		s.logger.Debugf("pending delete still missing source_chat_id=%d source_message_id=%d trigger=%s", key.sourceChatID, key.sourceMessageID, trigger)
	}
}

func (s *BusinessMessageService) reconcilePendingDeletes(ctx context.Context, now time.Time) {
	pendingDeletes, err := s.repo.ListDuePendingDeletes(ctx, now, pendingDeleteBatchSize)
	if err != nil {
		s.logger.Warnf("pending delete due query failed: %v", err)
		return
	}

	for _, pending := range pendingDeletes {
		key := deleteKey{
			businessConnectionID: pending.BusinessConnectionID,
			sourceChatID:         pending.SourceChatID,
			sourceMessageID:      pending.SourceMessageID,
		}

		result, err := s.processDelete(ctx, key)
		if err != nil {
			s.logger.Warnf("pending delete retry failed source_chat_id=%d source_message_id=%d: %v", key.sourceChatID, key.sourceMessageID, err)
			s.rescheduleStoredPendingDelete(ctx, pending, now, true, err.Error())
			continue
		}

		switch result {
		case deleteProcessProcessed, deleteProcessAlreadyProcessed:
			s.removePendingDelete(ctx, key)
			s.logger.Infof("pending delete resolved source_chat_id=%d source_message_id=%d result=%s", key.sourceChatID, key.sourceMessageID, result)
		case deleteProcessMissingMessage:
			expired := s.rescheduleStoredPendingDelete(ctx, pending, now, false, "metadata_not_found")
			if expired {
				s.logger.Infof("pending delete dropped as stale source_chat_id=%d source_message_id=%d", key.sourceChatID, key.sourceMessageID)
			}
		}
	}
}

func (s *BusinessMessageService) enqueuePendingDelete(ctx context.Context, key deleteKey, reason string) {
	now := time.Now().UTC()
	retryDelay := s.pendingDeleteRetryDelay
	if retryDelay <= 0 {
		retryDelay = 2 * time.Second
	}

	if err := s.repo.UpsertPendingDelete(ctx, &storage.PendingDelete{
		BusinessConnectionID: key.businessConnectionID,
		SourceChatID:         key.sourceChatID,
		SourceMessageID:      key.sourceMessageID,
		FirstSeenAt:          now,
		NextAttemptAt:        now.Add(retryDelay),
		AttemptCount:         0,
		Status:               "pending",
		LastError:            reason,
		UpdatedAt:            now,
	}); err != nil {
		s.logger.Warnf("enqueue pending delete failed source_chat_id=%d source_message_id=%d reason=%s: %v", key.sourceChatID, key.sourceMessageID, reason, err)
		return
	}

	s.logger.Infof("delete event deferred source_chat_id=%d source_message_id=%d reason=%s", key.sourceChatID, key.sourceMessageID, reason)
}

func (s *BusinessMessageService) removePendingDelete(ctx context.Context, key deleteKey) {
	if err := s.repo.DeletePendingDelete(ctx, key.businessConnectionID, key.sourceChatID, key.sourceMessageID); err != nil {
		s.logger.Warnf("remove pending delete failed source_chat_id=%d source_message_id=%d: %v", key.sourceChatID, key.sourceMessageID, err)
	}
}

func (s *BusinessMessageService) rescheduleStoredPendingDelete(ctx context.Context, pending storage.PendingDelete, now time.Time, dueToError bool, lastError string) bool {
	maxAge := s.pendingDeleteMaxAge
	if maxAge <= 0 {
		maxAge = 90 * time.Second
	}

	key := deleteKey{
		businessConnectionID: pending.BusinessConnectionID,
		sourceChatID:         pending.SourceChatID,
		sourceMessageID:      pending.SourceMessageID,
	}

	if now.Sub(pending.FirstSeenAt) > maxAge {
		s.removePendingDelete(ctx, key)
		return true
	}

	retryDelay := s.pendingDeleteRetryDelay
	if retryDelay <= 0 {
		retryDelay = 2 * time.Second
	}

	attemptCount := pending.AttemptCount + 1
	nextDelay := retryDelay
	if dueToError {
		nextDelay = nextDelay * time.Duration(attemptCount+1)
		if nextDelay > 20*time.Second {
			nextDelay = 20 * time.Second
		}
	}

	if err := s.repo.ReschedulePendingDelete(
		ctx,
		pending.BusinessConnectionID,
		pending.SourceChatID,
		pending.SourceMessageID,
		now.Add(nextDelay),
		attemptCount,
		lastError,
		now,
	); err != nil {
		s.logger.Warnf("reschedule pending delete failed source_chat_id=%d source_message_id=%d: %v", pending.SourceChatID, pending.SourceMessageID, err)
	}

	return false
}
