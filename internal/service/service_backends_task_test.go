package service

import (
	"errors"
	"slices"
	"testing"
)

func TestWindowsTaskGuardsOffWindows(t *testing.T) {
	stubHost(t, hostStub{goos: "linux", euid: 1000})
	stubRun(t)

	if _, err := installWindowsTask("C:\\9router-go.exe", nil); err == nil {
		t.Error("installWindowsTask() error = nil off windows, want the guard error")
	}
	if _, ok := uninstallWindowsTask(); ok {
		t.Error("uninstallWindowsTask() ok = true off windows, want false")
	}
	if _, ok := statusWindowsTask(); ok {
		t.Error("statusWindowsTask() ok = true off windows, want false")
	}
}

func TestInstallWindowsTaskOnWindows(t *testing.T) {
	stubHost(t, hostStub{goos: "windows"})
	calls := stubRun(t)

	info, err := installWindowsTask(`C:\9router-go.exe`, []string{"--rtk=true"})
	if err != nil {
		t.Fatalf("installWindowsTask() error = %v", err)
	}
	if info.Backend != BackendWindowsTask || !info.Enabled || !info.Active {
		t.Errorf("Info = %+v, want windows-task enabled and active", info)
	}
	if info.Path != windowsTaskName {
		t.Errorf("Info.Path = %q, want %q", info.Path, windowsTaskName)
	}
	var created bool
	for _, c := range *calls {
		if c.name == "schtasks.exe" && slices.Contains(c.args, "/create") {
			created = true
		}
	}
	if !created {
		t.Errorf("calls = %v, want a schtasks /create", *calls)
	}
}

func TestInstallWindowsTaskErrorPaths(t *testing.T) {
	t.Run("create failure is reported and not enabled", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/create") {
				return "access denied", errors.New("boom")
			}
			return "", nil
		})

		info, err := installWindowsTask(`C:\9router-go.exe`, nil)
		if err == nil {
			t.Fatal("installWindowsTask() error = nil when /create fails")
		}
		if info.Enabled {
			t.Errorf("Info.Enabled = true after a failed create: %+v", info)
		}
	})

	t.Run("run failure leaves the task enabled but inactive", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/run") {
				return "", errors.New("boom")
			}
			return "", nil
		})

		info, err := installWindowsTask(`C:\9router-go.exe`, nil)
		if err != nil {
			t.Fatalf("installWindowsTask() error = %v", err)
		}
		if !info.Enabled || info.Active {
			t.Errorf("Info = %+v, want enabled but inactive", info)
		}
	})
}

func TestStatusWindowsTask(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		err     error
		wantOK  bool
		wantAct bool
	}{
		{name: "task present and running", out: windowsTaskName + "  Running", wantOK: true, wantAct: true},
		{name: "task present but ready", out: windowsTaskName + "  Ready", wantOK: true, wantAct: false},
		{name: "query error", err: errors.New("no task"), wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubHost(t, hostStub{goos: "windows"})
			stubRunFunc(t, func(string, ...string) (string, error) { return tt.out, tt.err })

			info, ok := statusWindowsTask()
			if ok != tt.wantOK {
				t.Fatalf("statusWindowsTask() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && info.Active != tt.wantAct {
				t.Errorf("Info.Active = %v, want %v", info.Active, tt.wantAct)
			}
		})
	}
}

func TestUninstallWindowsTask(t *testing.T) {
	t.Run("task present is ended and deleted", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		calls := stubRunFunc(t, func(name string, args ...string) (string, error) {
			if slices.Contains(args, "/query") {
				return windowsTaskName + "  Ready", nil
			}
			return "", nil
		})

		if _, ok := uninstallWindowsTask(); !ok {
			t.Fatal("uninstallWindowsTask() = false, want true")
		}
		var ended, deleted bool
		for _, c := range *calls {
			if slices.Contains(c.args, "/end") {
				ended = true
			}
			if slices.Contains(c.args, "/delete") {
				deleted = true
			}
		}
		if !ended || !deleted {
			t.Errorf("calls = %v, want /end then /delete", *calls)
		}
	})

	t.Run("query error means nothing to remove", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubRunFunc(t, func(string, ...string) (string, error) { return "", errors.New("not found") })
		if _, ok := uninstallWindowsTask(); ok {
			t.Error("uninstallWindowsTask() = true when the task does not exist")
		}
	})

	t.Run("output without the task name means nothing to remove", func(t *testing.T) {
		stubHost(t, hostStub{goos: "windows"})
		stubRunFunc(t, func(string, ...string) (string, error) { return "some other task", nil })
		if _, ok := uninstallWindowsTask(); ok {
			t.Error("uninstallWindowsTask() = true without the task name in the query output")
		}
	})
}

func TestControlWindowsTask(t *testing.T) {
	tests := []struct {
		op    string
		flags []string
	}{
		{"start", []string{"/run"}},
		{"stop", []string{"/end"}},
		{"restart", []string{"/end", "/run"}},
	}
	for _, tt := range tests {
		t.Run(tt.op, func(t *testing.T) {
			calls := stubRun(t)
			if err := controlWindowsTask(tt.op); err != nil {
				t.Fatalf("controlWindowsTask(%s) error = %v", tt.op, err)
			}
			for _, flag := range tt.flags {
				found := false
				for _, c := range *calls {
					if slices.Contains(c.args, flag) {
						found = true
					}
				}
				if !found {
					t.Errorf("calls = %v, want a %s invocation", *calls, flag)
				}
			}
		})
	}

	t.Run("unknown operation is rejected", func(t *testing.T) {
		stubRun(t)
		if err := controlWindowsTask("explode"); err == nil {
			t.Error("controlWindowsTask(explode) error = nil, want error")
		}
	})
}
