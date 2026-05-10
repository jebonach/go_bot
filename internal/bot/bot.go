package bot

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"removed-messages/internal/config"
	"removed-messages/internal/logging"
	"removed-messages/internal/service"
	"removed-messages/internal/storage"
	"removed-messages/internal/telegram"
)

func Init(cfg *config.Config, logger *logging.Logger) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	repo, err := storage.NewSQLiteRepository(cfg.SQLitePath)
	if err != nil {
		return fmt.Errorf("init sqlite repository: %w", err)
	}
	defer func() {
		if closeErr := repo.Close(); closeErr != nil {
			logger.Errorf("close sqlite repository: %v", closeErr)
		}
	}()

	if err := repo.Migrate(ctx); err != nil {
		logger.Errorf("db migration failed: %v", err)
		return fmt.Errorf("migrate sqlite repository: %w", err)
	}
	logger.Infof("db migration completed")

	var svc *service.BusinessMessageService

	opts := []tgbot.Option{
		tgbot.WithErrorsHandler(func(err error) {
			logger.Errorf("telegram bot runtime error: %v", err)
		}),
		tgbot.WithNotAsyncHandlers(),
		tgbot.WithAllowedUpdates(tgbot.AllowedUpdates{
			models.AllowedUpdateMessage,
			models.AllowedUpdateBusinessConnection,
			models.AllowedUpdateBusinessMessage,
			models.AllowedUpdateEditedBusinessMessage,
			models.AllowedUpdateDeletedBusinessMessages,
		}),
		tgbot.WithDefaultHandler(func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
			switch {
			case update.BusinessConnection != nil && cfg.BusinessMode:
				svc.HandleBusinessConnection(ctx, update.BusinessConnection)
			case update.BusinessMessage != nil && cfg.BusinessMode:
				if update.BusinessMessage.Chat.ID == cfg.OwnerChatID || update.BusinessMessage.Chat.ID == cfg.ArchiveChatID {
					return
				}
				svc.HandleBusinessMessage(ctx, update.BusinessMessage)
			case update.EditedBusinessMessage != nil && cfg.BusinessMode:
				svc.HandleEditedBusinessMessage(ctx, update.EditedBusinessMessage)
			case update.DeletedBusinessMessages != nil && cfg.BusinessMode:
				svc.HandleDeletedBusinessMessages(ctx, update.DeletedBusinessMessages)
			case update.Message != nil:
				if svc != nil && svc.HandleOwnerCommand(ctx, update.Message) {
					return
				}
				if update.Message.Text == "/start" || update.Message.Text == "/id" {
					if _, err := b.SendMessage(ctx, &tgbot.SendMessageParams{
						ChatID:    update.Message.Chat.ID,
						ParseMode: "HTML",
						Text:      fmt.Sprintf("Your chat ID: <code>%d</code>", update.Message.Chat.ID),
					}); err != nil {
						logger.Errorf("send /id response failed: %v", err)
					}
				}
			}
		}),
	}

	b, err := tgbot.New(cfg.Token, opts...)
	if err != nil {
		return fmt.Errorf("init telegram bot: %w", err)
	}

	tgClient := telegram.NewClient(b, logger)
	svc = service.NewBusinessMessageService(cfg, repo, tgClient, logger)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		svc.RunCleanupLoop(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		svc.RunPendingDeleteLoop(ctx)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		svc.RunMediaGroupFlushLoop(ctx)
	}()

	logger.Infof("bot started business_mode=%t", cfg.BusinessMode)
	b.Start(ctx)
	wg.Wait()

	logger.Infof("bot stopped")
	return nil
}
