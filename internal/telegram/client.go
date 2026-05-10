package telegram

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"removed-messages/internal/logging"
)

const (
	defaultMaxRetries = 4
	defaultBaseDelay  = 250 * time.Millisecond
	defaultMaxDelay   = 30 * time.Second
)

var retryAfterPattern = regexp.MustCompile(`retry after (\d+)`)

type Client struct {
	bot         *tgbot.Bot
	logger      *logging.Logger
	maxRetries  int
	baseDelay   time.Duration
	maxDelay    time.Duration
	throttleGap time.Duration
}

func NewClient(botInstance *tgbot.Bot, logger *logging.Logger) *Client {
	return &Client{
		bot:         botInstance,
		logger:      logger,
		maxRetries:  defaultMaxRetries,
		baseDelay:   defaultBaseDelay,
		maxDelay:    defaultMaxDelay,
		throttleGap: 0,
	}
}

func (c *Client) CopyMessage(ctx context.Context, fromChatID int64, messageID int, toChatID int64) (int, error) {
	return c.callWithRetry(ctx, "copy_message", func() (int, error) {
		result, err := c.bot.CopyMessage(ctx, &tgbot.CopyMessageParams{
			ChatID:     toChatID,
			FromChatID: fromChatID,
			MessageID:  messageID,
		})
		if err != nil {
			return 0, fmt.Errorf("copy message %d from chat %d to chat %d: %w", messageID, fromChatID, toChatID, err)
		}
		return result.ID, nil
	})
}

func (c *Client) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	_, err := c.callWithRetry(ctx, "delete_message", func() (int, error) {
		if _, err := c.bot.DeleteMessage(ctx, &tgbot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: messageID,
		}); err != nil {
			return 0, fmt.Errorf("delete message %d from chat %d: %w", messageID, chatID, err)
		}
		return 0, nil
	})
	return err
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) (int, error) {
	return c.callWithRetry(ctx, "send_message", func() (int, error) {
		message, err := c.bot.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID: chatID,
			Text:   text,
		})
		if err != nil {
			return 0, fmt.Errorf("send message to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendBusinessMessage(ctx context.Context, businessConnectionID string, chatID int64, text string) (int, error) {
	businessConnectionID = strings.TrimSpace(businessConnectionID)
	if businessConnectionID == "" {
		return 0, fmt.Errorf("business connection id is empty")
	}

	return c.callWithRetry(ctx, "send_business_message", func() (int, error) {
		message, err := c.bot.SendMessage(ctx, &tgbot.SendMessageParams{
			BusinessConnectionID: businessConnectionID,
			ChatID:               chatID,
			Text:                 text,
		})
		if err != nil {
			return 0, fmt.Errorf("send business message to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendPhotoByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	return c.callWithRetry(ctx, "send_photo", func() (int, error) {
		message, err := c.bot.SendPhoto(ctx, &tgbot.SendPhotoParams{
			ChatID:  chatID,
			Photo:   &models.InputFileString{Data: fileID},
			Caption: caption,
		})
		if err != nil {
			return 0, fmt.Errorf("send photo to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendVoiceByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	return c.callWithRetry(ctx, "send_voice", func() (int, error) {
		message, err := c.bot.SendVoice(ctx, &tgbot.SendVoiceParams{
			ChatID:  chatID,
			Voice:   &models.InputFileString{Data: fileID},
			Caption: caption,
		})
		if err != nil {
			return 0, fmt.Errorf("send voice to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendAudioByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	return c.callWithRetry(ctx, "send_audio", func() (int, error) {
		message, err := c.bot.SendAudio(ctx, &tgbot.SendAudioParams{
			ChatID:  chatID,
			Audio:   &models.InputFileString{Data: fileID},
			Caption: caption,
		})
		if err != nil {
			return 0, fmt.Errorf("send audio to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendDocumentByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	return c.callWithRetry(ctx, "send_document", func() (int, error) {
		message, err := c.bot.SendDocument(ctx, &tgbot.SendDocumentParams{
			ChatID:   chatID,
			Document: &models.InputFileString{Data: fileID},
			Caption:  caption,
		})
		if err != nil {
			return 0, fmt.Errorf("send document to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendVideoByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	return c.callWithRetry(ctx, "send_video", func() (int, error) {
		message, err := c.bot.SendVideo(ctx, &tgbot.SendVideoParams{
			ChatID:  chatID,
			Video:   &models.InputFileString{Data: fileID},
			Caption: caption,
		})
		if err != nil {
			return 0, fmt.Errorf("send video to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendAnimationByFileID(ctx context.Context, chatID int64, fileID string, caption string) (int, error) {
	return c.callWithRetry(ctx, "send_animation", func() (int, error) {
		message, err := c.bot.SendAnimation(ctx, &tgbot.SendAnimationParams{
			ChatID:    chatID,
			Animation: &models.InputFileString{Data: fileID},
			Caption:   caption,
		})
		if err != nil {
			return 0, fmt.Errorf("send animation to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendStickerByFileID(ctx context.Context, chatID int64, fileID string) (int, error) {
	return c.callWithRetry(ctx, "send_sticker", func() (int, error) {
		message, err := c.bot.SendSticker(ctx, &tgbot.SendStickerParams{
			ChatID:  chatID,
			Sticker: &models.InputFileString{Data: fileID},
		})
		if err != nil {
			return 0, fmt.Errorf("send sticker to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendVideoNoteByFileID(ctx context.Context, chatID int64, fileID string) (int, error) {
	return c.callWithRetry(ctx, "send_video_note", func() (int, error) {
		message, err := c.bot.SendVideoNote(ctx, &tgbot.SendVideoNoteParams{
			ChatID:    chatID,
			VideoNote: &models.InputFileString{Data: fileID},
		})
		if err != nil {
			return 0, fmt.Errorf("send video note to chat %d: %w", chatID, err)
		}
		return message.ID, nil
	})
}

func (c *Client) SendOwnerNotification(ctx context.Context, ownerChatID int64, text string) error {
	_, err := c.callWithRetry(ctx, "send_owner_notification", func() (int, error) {
		if _, err := c.bot.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:    ownerChatID,
			ParseMode: "HTML",
			Text:      text,
		}); err != nil {
			return 0, fmt.Errorf("send owner notification: %w", err)
		}
		return 0, nil
	})
	return err
}

func (c *Client) IsMissingMessageError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, tgbot.ErrorNotFound) {
		return true
	}

	if !errors.Is(err, tgbot.ErrorBadRequest) {
		return false
	}

	lowerErr := strings.ToLower(err.Error())
	return strings.Contains(lowerErr, "message to delete not found") ||
		strings.Contains(lowerErr, "message to copy not found") ||
		strings.Contains(lowerErr, "message can't be deleted") ||
		strings.Contains(lowerErr, "message identifier is not specified")
}

// callWithRetry retries Telegram API calls with exponential backoff for transient
// errors and honors the retry_after hint Telegram returns on flood control (429).
// Permanent client errors (400 BadRequest, 403 Forbidden, missing-message) are NOT
// retried.
func (c *Client) callWithRetry(ctx context.Context, op string, fn func() (int, error)) (int, error) {
	var (
		lastErr  error
		attempts = c.maxRetries
	)
	if attempts <= 0 {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}

		id, err := fn()
		if err == nil {
			if c.throttleGap > 0 {
				time.Sleep(c.throttleGap)
			}
			return id, nil
		}

		lastErr = err

		if !c.isRetryable(err) {
			return 0, err
		}

		wait := c.computeWait(err, attempt)
		if c.logger != nil {
			c.logger.Warnf("telegram %s retryable error attempt=%d wait=%s: %v", op, attempt+1, wait, err)
		}

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(wait):
		}
	}

	return 0, fmt.Errorf("%s exhausted retries: %w", op, lastErr)
}

func (c *Client) isRetryable(err error) bool {
	if err == nil {
		return false
	}
	// Permanent client errors that should never be retried.
	if errors.Is(err, tgbot.ErrorNotFound) || errors.Is(err, tgbot.ErrorBadRequest) {
		return false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "forbidden") || strings.Contains(lower, "unauthorized") {
		return false
	}
	if strings.Contains(lower, "too many requests") || strings.Contains(lower, "retry after") {
		return true
	}
	// Network/server errors are transient. Treat unknown errors as transient.
	return true
}

func (c *Client) computeWait(err error, attempt int) time.Duration {
	if seconds, ok := parseRetryAfter(err); ok {
		wait := time.Duration(seconds) * time.Second
		if wait <= 0 {
			wait = c.baseDelay
		}
		if wait > c.maxDelay {
			wait = c.maxDelay
		}
		return wait
	}

	wait := c.baseDelay << attempt
	if wait <= 0 || wait > c.maxDelay {
		wait = c.maxDelay
	}
	return wait
}

func parseRetryAfter(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	matches := retryAfterPattern.FindStringSubmatch(strings.ToLower(err.Error()))
	if len(matches) < 2 {
		return 0, false
	}
	value, parseErr := strconv.Atoi(matches[1])
	if parseErr != nil || value < 0 {
		return 0, false
	}
	return value, true
}
