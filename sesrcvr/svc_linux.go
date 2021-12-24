//go:build linux
// +build linux

package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-systemd/daemon"
)

func SvcNotifyStatus(status string) error {
	_, err := daemon.SdNotify(false, fmt.Sprintf("STATUS=%s", status))
	return err
}

func SvcReadyNotify(wg *sync.WaitGroup, ctx context.Context) error {
	daemon.SdNotify(false, "READY=1")
	delay, err := daemon.SdWatchdogEnabled(false)
	if err != nil || delay == 0 {
		return err
	}
	tCh := time.Tick(delay / 2)

	wg.Add(1)
	go func() {
		defer wg.Done()
	Loop:
		for {
			select {
			case <-tCh:
				daemon.SdNotify(false, "WATCHDOG=1")
			case <-ctx.Done():
				break Loop
			}
		}
	}()

	return nil
}

func SvcStopNotify() error {
	_, err := daemon.SdNotify(false, daemon.SdNotifyStopping)
	return err
}
