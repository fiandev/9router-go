// Package service installs the gateway as an autostart service so it comes
// back after a reboot: a systemd user unit on systemd Linux, a launchd agent
// on macOS, an OpenRC/runit service where those inits run, a cron @reboot
// entry everywhere else, and a scheduled task on Windows.
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"9router/proxy/internal/proc"
)

// Backend names the init/autostart system an install targets.
type Backend string

const (
	BackendNone        Backend = "none"
	BackendSystemdUser Backend = "systemd-user"
	BackendSystemdSys  Backend = "systemd-system"
	BackendLaunchd     Backend = "launchd"
	BackendOpenRC      Backend = "openrc"
	BackendRunit       Backend = "runit"
	BackendCron        Backend = "cron"
	BackendWindowsTask Backend = "windows-task"
	BackendOverrideEnv         = "9ROUTER_SERVICE_BACKEND"
)

// ErrNotInstalled is returned when no autostart entry exists to remove.
var ErrNotInstalled = errors.New("9router-go autostart is not installed")

// Info describes an installed (or just-installed) autostart entry.
type Info struct {
	Backend Backend
	Path    string
	Exec    string
	Enabled bool
	Active  bool
}

// Install writes the autostart entry for this machine's init system and
// enables it. extraArgs are baked into the supervised command line (the same
// --flag=value pairs the background daemon forwards to its child).
func Install(extraArgs []string) (Info, error) {
	exe, err := proc.SelfExecutable()
	if err != nil {
		return Info{}, fmt.Errorf("service.Install: locate binary: %w", err)
	}
	switch Detect() {
	case BackendSystemdUser:
		return installSystemd(exe, extraArgs, false)
	case BackendSystemdSys:
		return installSystemd(exe, extraArgs, true)
	case BackendLaunchd:
		return installLaunchd(exe, extraArgs)
	case BackendOpenRC:
		return installOpenRC(exe, extraArgs)
	case BackendRunit:
		return installRunit(exe, extraArgs)
	case BackendCron:
		return installCron(exe, extraArgs)
	case BackendWindowsTask:
		return installWindowsTask(exe, extraArgs)
	default:
		return Info{}, fmt.Errorf("service.Install: no supported autostart backend found on %s", runtime.GOOS)
	}
}

// Uninstall removes every autostart entry this package could have created,
// whichever backend owns it. It reports the entries it removed.
func Uninstall() ([]Info, error) {
	removed := []Info{}
	if info, ok := uninstallSystemd(false); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallSystemd(true); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallLaunchd(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallOpenRC(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallRunit(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallCron(); ok {
		removed = append(removed, info)
	}
	if info, ok := uninstallWindowsTask(); ok {
		removed = append(removed, info)
	}
	if len(removed) == 0 {
		return nil, ErrNotInstalled
	}
	return removed, nil
}

// Status reports the autostart entry in effect, or false when none exists.
func Status() (Info, bool) {
	for _, probe := range []func() (Info, bool){
		statusSystemd,
		statusLaunchd,
		statusOpenRC,
		statusRunit,
		statusCron,
		statusWindowsTask,
	} {
		if info, ok := probe(); ok {
			return info, true
		}
	}
	return Info{}, false
}

// Detect picks the backend Install will use on this machine. The
// 9ROUTER_SERVICE_BACKEND env var forces one (systemd, launchd, openrc,
// runit, cron) for machines with several inits or for testing.
func Detect() Backend {
	if runtime.GOOS == "windows" {
		if lookPath("schtasks.exe") != "" {
			return BackendWindowsTask
		}
		return BackendNone
	}
	if runtime.GOOS == "darwin" {
		return BackendLaunchd
	}
	if forced := strings.TrimSpace(os.Getenv(BackendOverrideEnv)); forced != "" {
		switch Backend(forced) {
		case BackendSystemdUser, BackendSystemdSys, BackendLaunchd, BackendOpenRC, BackendRunit, BackendCron:
			return Backend(forced)
		case "systemd", "system":
			if os.Geteuid() == 0 {
				return BackendSystemdSys
			}
			return BackendSystemdUser
		}
	}
	if runtime.GOOS != "linux" {
		if hasCron() {
			return BackendCron
		}
		return BackendNone
	}
	switch {
	case hasSystemd():
		if os.Geteuid() == 0 {
			return BackendSystemdSys
		}
		return BackendSystemdUser
	case hasOpenRC():
		return BackendOpenRC
	case hasRunit():
		return BackendRunit
	case hasCron():
		return BackendCron
	default:
		return BackendNone
	}
}

// hasSystemd reports whether this Linux host is actually supervised by
// systemd. The /run/systemd/system directory is the authority: a stray
// systemctl shim elsewhere must not select this backend.
func hasSystemd() bool {
	if lookPath("systemctl") == "" {
		return false
	}
	if st, err := os.Stat("/run/systemd/system"); err == nil && st.IsDir() {
		return true
	}
	return false
}

func hasOpenRC() bool {
	return lookPath("openrc-run") != "" || lookPath("rc-service") != ""
}

func hasRunit() bool {
	if lookPath("sv") != "" {
		return true
	}
	if st, err := os.Stat("/run/runit"); err == nil && st.IsDir() {
		return true
	}
	return false
}

func hasCron() bool {
	return lookPath("crontab") != ""
}

func lookPath(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

// run runs a helper command, returning its combined output for diagnostics.
// It is a var so tests can observe and stub supervisor calls without touching
// the real init system.
var run = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("service: %s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// commandLine joins the executable and args for display and for unit files
// that take a plain command line.
func commandLine(exe string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(exe))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if !strings.ContainsAny(arg, " \t\n\"'\\") {
		return arg
	}
	return strconv.Quote(arg)
}
