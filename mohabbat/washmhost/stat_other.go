//go:build !linux && !darwin && !windows

package main

import "os"

func populatePlatformAbiStat(os.FileInfo, string, bool, *AbiStat) {}
