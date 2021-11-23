package main

import "time"

// Sensible configs used by server that shouldn't change but could be customized.
const (
	AppID = "lazycons.in/sesrcvr"

	sqlite3BusyTimeout = "30000"
	sqliteCacheSize    = "-10000" // 10MiB

	certFetchTimeout    = 7 * time.Second
	processingTimeout   = 14 * time.Second
	requestReadTimeout  = 4 * time.Second
	requestWriteTimeout = 15*time.Second - 500*time.Millisecond // Set according to SNS request timeout(adjusting for rt)
	shutdownTimeout     = 20 * time.Second                      // wait for active connections to finish(>requestWriteTimeout).

	httpOutgoingDelayMin = 100 * time.Millisecond
	httpOutgoingDelayMax = 1 * time.Second
	httpOutgoingMaxRetry = 500

	msgQSize             = 256
	maxMessageProcessors = msgQSize>>3 | 1

	defaultGcPeriod    = 24 * time.Hour
	defaultGcThreshold = 2000

	incrementalGCDelay  = requestWriteTimeout * 2
	incrementalGCAmount = 100

	downloadDirName  = "mailcache"
	ctrlSockFileName = "srvctrl.sock"
	dbFileName       = "config.sqlite3.DO_NOT_DELETE"
	runLockFileName  = "run.lock"
)
