//go:build wasip1

package app

import (
	"log"
	"sync"
	"syscall"
)

var platformInfoWatcherOnce sync.Once

func startPlatformInfoWatcher() {
	platformInfoWatcherOnce.Do(func() {
		go func() {
			for {
				wait := syscall.BeginPlatformInfoWait()
				syscall.RefreshPlatformInfo()
				if err := wait.Wait(); err != nil {
					log.Printf("watch system information: %v", err)
					return
				}
			}
		}()
	})
}
