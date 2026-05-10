package service

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"
)

// trackMediaGroup buffers an album item so the flush loop can produce a single
// "album complete" log entry once the album stops growing.
//
// Per-item archiving is already done synchronously in HandleBusinessMessage; the
// buffer is purely for grouping awareness, not for staging archive sends.
func (s *BusinessMessageService) trackMediaGroup(message *models.Message) {
	if s == nil || message == nil || message.MediaGroupID == "" {
		return
	}

	s.mediaGroupMu.Lock()
	defer s.mediaGroupMu.Unlock()

	bucket, ok := s.mediaGroupBuffer[message.MediaGroupID]
	now := time.Now().UTC()
	if !ok {
		bucket = &mediaGroupBucket{
			BusinessConnectionID: message.BusinessConnectionID,
			SourceChatID:         message.Chat.ID,
			OwnerChatID:          s.cfg.OwnerChatID,
			MediaGroupID:         message.MediaGroupID,
			FirstSeenAt:          now,
		}
		s.mediaGroupBuffer[message.MediaGroupID] = bucket
	}
	bucket.LastSeenAt = now
	bucket.SourceMessageIDs = append(bucket.SourceMessageIDs, message.ID)
}

// RunMediaGroupFlushLoop periodically logs and removes stale album buckets so the
// in-memory map does not grow forever. Buckets become stale when no new item has
// arrived within mediaGroupFlushDelay.
func (s *BusinessMessageService) RunMediaGroupFlushLoop(ctx context.Context) {
	if s == nil {
		return
	}

	tick := s.mediaGroupSweepTick
	if tick <= 0 {
		tick = 500 * time.Millisecond
	}

	s.logger.Infof("media group flush loop started interval=%s", tick)
	defer s.logger.Infof("media group flush loop stopped")

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.flushStaleMediaGroups(time.Now().UTC())
		}
	}
}

func (s *BusinessMessageService) flushStaleMediaGroups(now time.Time) {
	delay := s.mediaGroupFlushDelay
	if delay <= 0 {
		delay = 1500 * time.Millisecond
	}

	s.mediaGroupMu.Lock()
	defer s.mediaGroupMu.Unlock()

	for id, bucket := range s.mediaGroupBuffer {
		if bucket == nil {
			delete(s.mediaGroupBuffer, id)
			continue
		}
		if now.Sub(bucket.LastSeenAt) < delay {
			continue
		}
		s.logger.Infof("media group buffered=%d business_connection_id=%s source_chat_id=%d media_group_id=%s", len(bucket.SourceMessageIDs), bucket.BusinessConnectionID, bucket.SourceChatID, id)
		delete(s.mediaGroupBuffer, id)
	}
}
