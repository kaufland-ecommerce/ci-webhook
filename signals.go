//go:build !windows

package main

import (
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func setupSignals(notifyReload func()) {
	slog.Info("setting up os signal watcher")
	signals := make(chan os.Signal, 1)

	signal.Notify(
		signals,
		syscall.SIGUSR1,
		syscall.SIGHUP,
		syscall.SIGTERM,
		os.Interrupt,
	)

	go func() {
		slog.Info("os signal watcher ready")
		for {
			sig := <-signals
			switch sig {
			case syscall.SIGUSR1, syscall.SIGHUP:
				slog.Warn("caught signal", "signal", sig)
				notifyReload()
			case os.Interrupt, syscall.SIGTERM:
				log.Printf("caught %s signal; exiting\n", sig)
				slog.Warn("caught signal", "signal", sig)
				// todo: do proper shutdown, by notifying main loop, and remove this
				if pidFile != nil {
					err := pidFile.Remove()
					if err != nil {
						log.Print(err)
					}
				}
				os.Exit(0)
			default:
				log.Printf("caught unhandled signal %+v\n", sig)
			}
		}
	}()
}
