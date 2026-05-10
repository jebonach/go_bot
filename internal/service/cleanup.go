package service

import (
	"context"
	"time"

	"removed-messages/internal/storage"
)

const (
	cleanupBatchSize     = 200
	cleanupDeleteBackoff = 35 * time.Millisecond
)

func (s *BusinessMessageService) RunCleanupLoop(ctx context.Context) {
	if s == nil || s.repo == nil {
		return
	}

	s.logger.Infof("cleanup loop started interval=%s", s.cfg.CleanupInterval)
	defer s.logger.Infof("cleanup loop stopped")

	s.runCleanupOnce(ctx)

	ticker := time.NewTicker(s.cfg.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCleanupOnce(ctx)
		}
	}
}

func (s *BusinessMessageService) runCleanupOnce(ctx context.Context) {
	started := time.Now().UTC()
	removedCount := 0

	s.logger.Infof("cleanup run started")

	for {
		if ctx.Err() != nil {
			break
		}

		records, err := s.repo.ListExpired(ctx, time.Now().UTC(), cleanupBatchSize)
		if err != nil {
			s.logger.Errorf("cleanup query failed: %v", err)
			break
		}
		if len(records) == 0 {
			break
		}

		for i := range records {
			if ctx.Err() != nil {
				break
			}
			if s.cleanupRecord(ctx, &records[i]) {
				removedCount++
			}
		}

		if len(records) < cleanupBatchSize {
			break
		}
	}

	s.logger.Infof("cleanup run finished removed=%d duration=%s", removedCount, time.Since(started))
}

func (s *BusinessMessageService) cleanupRecord(ctx context.Context, record *storage.ArchivedMessage) bool {
	if record == nil {
		return false
	}

	if s.cfg.DeleteExpiredFromArchive {
		if !s.cleanupArchiveCopies(ctx, record) {
			return false
		}
	}

	if err := s.repo.DeleteByID(ctx, record.ID); err != nil {
		s.logger.Errorf("delete expired db row failed id=%d: %v", record.ID, err)
		return false
	}

	return true
}

func (s *BusinessMessageService) cleanupArchiveCopies(ctx context.Context, record *storage.ArchivedMessage) bool {
	copies, err := s.repo.ListArchiveCopiesByMessageID(ctx, record.ID)
	if err != nil {
		s.logger.Errorf("list archive copies during cleanup failed id=%d: %v", record.ID, err)
		return false
	}

	if len(copies) == 0 {
		return s.cleanupLegacyArchiveCopy(ctx, record)
	}

	for i := range copies {
		copy := &copies[i]
		if copy.DeletedFromArchive != nil {
			continue
		}
		if copy.ArchiveChatID == 0 {
			copy.ArchiveChatID = s.cfg.ArchiveChatID
		}
		if !s.deleteArchiveMessageIfPresent(ctx, copy.ArchiveChatID, copy.ArchiveMessageID) {
			return false
		}
		if !s.deleteArchiveMessageIfPresent(ctx, copy.ArchiveChatID, copy.MetadataMessageID) {
			return false
		}
		if copy.ID == 0 {
			continue
		}
		if err := s.repo.MarkArchiveCopyDeleted(ctx, copy.ID, time.Now().UTC()); err != nil {
			s.logger.Errorf("mark archive copy deleted failed id=%d: %v", copy.ID, err)
			return false
		}
	}

	return true
}

func (s *BusinessMessageService) cleanupLegacyArchiveCopy(ctx context.Context, record *storage.ArchivedMessage) bool {
	if record.ArchiveMessageID == nil {
		return true
	}

	archiveChatID := s.cfg.ArchiveChatID
	if record.ArchiveChatID != nil {
		archiveChatID = *record.ArchiveChatID
	}

	return s.deleteArchiveMessageIfPresent(ctx, archiveChatID, record.ArchiveMessageID)
}

func (s *BusinessMessageService) deleteArchiveMessageIfPresent(ctx context.Context, archiveChatID int64, messageID *int) bool {
	if messageID == nil {
		return true
	}

	if err := s.tg.DeleteMessage(ctx, archiveChatID, *messageID); err != nil {
		if s.tg.IsMissingMessageError(err) {
			s.logger.Warnf("archive message already missing during cleanup archive_message_id=%d", *messageID)
			return true
		}
		s.logger.Errorf("delete archive message during cleanup failed archive_message_id=%d: %v", *messageID, err)
		return false
	}

	if cleanupDeleteBackoff > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(cleanupDeleteBackoff):
		}
	}

	return true
}
