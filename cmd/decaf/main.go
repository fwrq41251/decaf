package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/fwrq41251/decaf/internal/server"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logFile, err := os.OpenFile("decaf.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Fatalf("failed to open log file: %v", err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "[decaf] ", log.LstdFlags|log.Lshortfile)

	s := server.NewServer(logger)
	if err := s.Run(ctx); err != nil {
		logger.Fatalf("server exited with error: %v", err)
	}
}
