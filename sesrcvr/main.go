package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	var exitCode int
	mainCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGABRT, syscall.SIGQUIT)
	defer func() {
		cancel()
		os.Exit(exitCode)
	}()
	exitCode = runRootCmd(mainCtx)
}
