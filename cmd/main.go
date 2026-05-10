package main

import (
	"log"
	"removed-messages/internal/bot"
	"removed-messages/internal/config"
	"removed-messages/internal/logging"
)

func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatal(err.Error())
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		log.Fatal(err.Error())
	}

	err = bot.Init(cfg, logger)
	if err != nil {
		log.Fatal(err.Error())
	}
}
