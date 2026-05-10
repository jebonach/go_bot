package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	Token                      string
	ArchiveChatID              int64
	OwnerChatID                int64
	BusinessMode               bool
	RetentionAudio             time.Duration
	RetentionPhoto             time.Duration
	RetentionText              time.Duration
	RetentionOtherMedia        time.Duration
	CleanupInterval            time.Duration
	DeleteExpiredFromArchive   bool
	NotifyOnDelete             bool
	ResendArchivedCopyOnDelete bool
	SQLitePath                 string
	LogLevel                   string
	ChatID                     int64
}

func LoadConfig() (*Config, error) {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("BOT_TOKEN is required")
	}

	archiveChatID, err := parseRequiredInt64("ARCHIVE_CHAT_ID")
	if err != nil {
		return nil, err
	}

	ownerChatID, err := parseRequiredInt64("OWNER_CHAT_ID")
	if err != nil {
		return nil, err
	}

	businessMode, err := parseBoolWithDefault("BUSINESS_MODE", true)
	if err != nil {
		return nil, err
	}
	if !businessMode {
		return nil, fmt.Errorf("BUSINESS_MODE must be true")
	}

	retentionAudioHours, err := parsePositiveIntWithDefault("RETENTION_AUDIO_HOURS", 168)
	if err != nil {
		return nil, err
	}

	retentionPhotoHours, err := parsePositiveIntWithDefault("RETENTION_PHOTO_HOURS", 720)
	if err != nil {
		return nil, err
	}

	retentionTextHours, err := parsePositiveIntWithDefault("RETENTION_TEXT_HOURS", 720)
	if err != nil {
		return nil, err
	}

	retentionOtherMediaHours, err := parsePositiveIntWithDefault("RETENTION_OTHER_MEDIA_HOURS", 720)
	if err != nil {
		return nil, err
	}

	cleanupIntervalMinutes, err := parsePositiveIntWithDefault("CLEANUP_INTERVAL_MINUTES", 360)
	if err != nil {
		return nil, err
	}

	deleteExpiredFromArchive, err := parseBoolWithDefault("DELETE_EXPIRED_FROM_ARCHIVE", true)
	if err != nil {
		return nil, err
	}

	notifyOnDelete, err := parseBoolWithDefault("NOTIFY_ON_DELETE", true)
	if err != nil {
		return nil, err
	}

	resendArchivedCopyOnDelete, err := parseBoolWithDefault("RESEND_ARCHIVED_COPY_ON_DELETE", true)
	if err != nil {
		return nil, err
	}

	sqlitePath := strings.TrimSpace(os.Getenv("SQLITE_PATH"))
	if sqlitePath == "" {
		sqlitePath = "./data/bot.db"
	}
	sqlitePath = filepath.Clean(sqlitePath)

	logLevel := strings.TrimSpace(os.Getenv("LOG_LEVEL"))
	if logLevel == "" {
		logLevel = "info"
	}

	chatID := ownerChatID
	if chatIDRaw := strings.TrimSpace(os.Getenv("CHAT_ID")); chatIDRaw != "" {
		legacyChatID, err := strconv.ParseInt(chatIDRaw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid CHAT_ID: %w", err)
		}
		chatID = legacyChatID
	}

	return &Config{
		Token:                      token,
		ArchiveChatID:              archiveChatID,
		OwnerChatID:                ownerChatID,
		BusinessMode:               businessMode,
		RetentionAudio:             time.Duration(retentionAudioHours) * time.Hour,
		RetentionPhoto:             time.Duration(retentionPhotoHours) * time.Hour,
		RetentionText:              time.Duration(retentionTextHours) * time.Hour,
		RetentionOtherMedia:        time.Duration(retentionOtherMediaHours) * time.Hour,
		CleanupInterval:            time.Duration(cleanupIntervalMinutes) * time.Minute,
		DeleteExpiredFromArchive:   deleteExpiredFromArchive,
		NotifyOnDelete:             notifyOnDelete,
		ResendArchivedCopyOnDelete: resendArchivedCopyOnDelete,
		SQLitePath:                 sqlitePath,
		LogLevel:                   strings.ToLower(logLevel),
		ChatID:                     chatID,
	}, nil
}

func parseRequiredInt64(key string) (int64, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, fmt.Errorf("%s is required", key)
	}

	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	return value, nil
}

func parsePositiveIntWithDefault(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", key, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0", key)
	}
	return value, nil
}

func parseBoolWithDefault(key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}

	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w", key, err)
	}
	return value, nil
}
