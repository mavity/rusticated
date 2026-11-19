//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func configureProcess(*exec.Cmd) error {
	return nil
}

func attachProcess(cmd *exec.Cmd) (func() error, func(), error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, fmt.Errorf("configure job object: %w", err)
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, fmt.Errorf("open child process: %w", err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	_ = windows.CloseHandle(process)
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, nil, fmt.Errorf("assign child to job object: %w", err)
	}
	terminate := func() error {
		return windows.TerminateJobObject(job, 1)
	}
	release := func() {
		_ = windows.CloseHandle(job)
	}
	return terminate, release, nil
}
