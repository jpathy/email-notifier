//go:build !linux
// +build !linux

package main

import (
	"context"
	"sync"
)

func SvcNotifyStatus(_ string) error {
	return nil
}

func SvcReadyNotify(_ sync.WaitGroup, _ context.Context) error {
	return nil
}

func SvcStopNotify() error {
	return nil
}
