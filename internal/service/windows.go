package service

import (
	"fmt"
	"runtime"
	"strings"
)

// windowsTaskName is the scheduled-task name this package owns.
const windowsTaskName = "9router-go"

func installWindowsTask(exe string, args []string) (Info, error) {
	if runtime.GOOS != "windows" {
		return Info{}, fmt.Errorf("service.Install: windows-task backend only runs on windows")
	}
	taskCmd := commandLine(exe, args)
	if _, err := run("schtasks.exe", "/create", "/tn", windowsTaskName,
		"/tr", taskCmd, "/sc", "onstart", "/ru", "SYSTEM", "/rl", "highest", "/f"); err != nil {
		return Info{Backend: BackendWindowsTask, Exec: taskCmd}, err
	}
	if _, err := run("schtasks.exe", "/run", "/tn", windowsTaskName); err != nil {
		return Info{Backend: BackendWindowsTask, Path: windowsTaskName, Exec: taskCmd, Enabled: true}, nil
	}
	return Info{Backend: BackendWindowsTask, Path: windowsTaskName, Exec: taskCmd, Enabled: true, Active: true}, nil
}

func uninstallWindowsTask() (Info, bool) {
	if runtime.GOOS != "windows" {
		return Info{}, false
	}
	out, err := run("schtasks.exe", "/query", "/tn", windowsTaskName)
	if err != nil || !strings.Contains(out, windowsTaskName) {
		return Info{}, false
	}
	_, _ = run("schtasks.exe", "/end", "/tn", windowsTaskName)
	_, _ = run("schtasks.exe", "/delete", "/tn", windowsTaskName, "/f")
	return Info{Backend: BackendWindowsTask, Path: windowsTaskName}, true
}

func statusWindowsTask() (Info, bool) {
	if runtime.GOOS != "windows" {
		return Info{}, false
	}
	out, err := run("schtasks.exe", "/query", "/tn", windowsTaskName)
	if err != nil || !strings.Contains(out, windowsTaskName) {
		return Info{}, false
	}
	return Info{Backend: BackendWindowsTask, Path: windowsTaskName, Enabled: true, Active: strings.Contains(out, "Running")}, true
}
