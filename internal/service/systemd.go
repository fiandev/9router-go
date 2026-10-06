package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"9router/proxy/internal/constants"
)

// unitName is the service name under both systemd scopes.
const unitName = "9router-go.service"

func userUnitPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "systemd", "user", unitName)
}

func systemUnitPath() string {
	return filepath.Join(string(filepath.Separator)+"etc", "systemd", "system", unitName)
}

// renderSystemdUnit renders the unit file. DATA_DIR is baked in only when it
// is exported in the installing shell: at boot the supervisor's environment
// has no shell exports, while .env beside the binary is still found by the
// server itself, so only the shell-only value needs preserving.
func renderSystemdUnit(exe string, args []string, system bool) string {
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=9router-go AI API proxy gateway\n")
	b.WriteString("After=network-online.target\n")
	b.WriteString("Wants=network-online.target\n\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + commandLine(exe, args) + "\n")
	if dir := filepath.Dir(exe); dir != "" {
		b.WriteString("WorkingDirectory=" + dir + "\n")
	}
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		b.WriteString("Environment=" + quoteArg("DATA_DIR="+dataDir) + "\n")
	}
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=3\n\n")
	b.WriteString("[Install]\n")
	if system {
		b.WriteString("WantedBy=multi-user.target\n")
	} else {
		b.WriteString("WantedBy=default.target\n")
	}
	return b.String()
}

func installSystemd(exe string, args []string, system bool) (Info, error) {
	path := systemUnitPath()
	scope := systemdScope(system)
	if !system {
		path = userUnitPath()
		if path == "" {
			return Info{}, fmt.Errorf("service.Install: cannot resolve home directory for user unit")
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), constants.FilePermDir); err != nil {
		return Info{}, fmt.Errorf("service.Install: create unit dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(renderSystemdUnit(exe, args, system)), constants.FilePermFile); err != nil {
		return Info{}, fmt.Errorf("service.Install: write %s: %w", path, err)
	}
	ctl := func(opArgs ...string) error {
		full := append(append([]string{}, scope...), opArgs...)
		_, err := run("systemctl", full...)
		return err
	}
	if err := ctl("daemon-reload"); err != nil {
		return Info{Backend: scopeBackend(system), Path: path, Exec: commandLine(exe, args)}, err
	}
	if err := ctl("enable", unitName); err != nil {
		return Info{Backend: scopeBackend(system), Path: path, Exec: commandLine(exe, args)}, err
	}
	info := Info{Backend: scopeBackend(system), Path: path, Exec: commandLine(exe, args), Enabled: true}
	// Starting now is best-effort: when the gateway already runs from
	// `9router-go start` the port is taken and the unit still starts on the
	// next boot, which is the point of the install. The active state is
	// polled rather than trusted from the start job: systemctl reports
	// success when the job is queued, even if the process then exits.
	if err := ctl("start", unitName); err != nil {
		info.Active = systemctlActive(scope)
		return info, nil
	}
	info.Active = waitSystemdActive(scope)
	if !system {
		tryEnableLinger()
	}
	return info, nil
}

// waitSystemdActive polls until the unit is active or the budget runs out.
func waitSystemdActive(scope []string) bool {
	deadline := time.Now().Add(3 * time.Second)
	for {
		if systemctlActive(scope) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func scopeBackend(system bool) Backend {
	if system {
		return BackendSystemdSys
	}
	return BackendSystemdUser
}

// systemdScope is the systemctl prefix for the target manager: --user for the
// per-user instance, nothing for the system one. Every install/uninstall/status
// call must go through it — omitting --user sends the command to the system
// bus, which prompts for root and leaves the user unit untouched.
func systemdScope(system bool) []string {
	if system {
		return nil
	}
	return []string{"--user"}
}

func uninstallSystemd(system bool) (Info, bool) {
	path := systemUnitPath()
	scope := systemdScope(system)
	backend := BackendSystemdSys
	if !system {
		path = userUnitPath()
		backend = BackendSystemdUser
	}
	if path == "" {
		return Info{}, false
	}
	if _, err := os.Stat(path); err != nil {
		return Info{}, false
	}
	full := append(append([]string{}, scope...), "disable", "--now", unitName)
	_, _ = run("systemctl", full...)
	// A failed removal (e.g. a system unit touched without root) must not
	// report success: Status still shows the entry, so the caller sees the
	// truth instead of a removal that never happened.
	if err := os.Remove(path); err != nil {
		return Info{}, false
	}
	_, _ = run("systemctl", append(scope, "daemon-reload")...)
	// A Restart=always unit can leave a queued restart job behind the reload;
	// stopping and resetting it keeps no ghost behind.
	_, _ = run("systemctl", append(scope, "stop", unitName)...)
	_, _ = run("systemctl", append(scope, "reset-failed", unitName)...)
	return Info{Backend: backend, Path: path}, true
}

func statusSystemd() (Info, bool) {
	if path := userUnitPath(); path != "" {
		if _, err := os.Stat(path); err == nil {
			scope := systemdScope(false)
			return Info{
				Backend: BackendSystemdUser,
				Path:    path,
				Enabled: systemctlEnabled(scope),
				Active:  systemctlActive(scope),
			}, true
		}
	}
	if _, err := os.Stat(systemUnitPath()); err == nil {
		scope := systemdScope(true)
		return Info{
			Backend: BackendSystemdSys,
			Path:    systemUnitPath(),
			Enabled: systemctlEnabled(scope),
			Active:  systemctlActive(scope),
		}, true
	}
	return Info{}, false
}

func systemctlEnabled(scope []string) bool {
	out, err := run("systemctl", append(append([]string{}, scope...), "is-enabled", "--quiet", unitName)...)
	_ = out
	return err == nil
}

func systemctlActive(scope []string) bool {
	out, err := run("systemctl", append(append([]string{}, scope...), "is-active", "--quiet", unitName)...)
	_ = out
	return err == nil
}

// tryEnableLinger keeps user units alive after logout. It only applies to the
// user scope and is best-effort: failures surface as a hint from the caller.
func tryEnableLinger() {
	if user := strings.TrimSpace(os.Getenv("SUDO_USER")); user != "" {
		_, _ = run("loginctl", "enable-linger", user)
		return
	}
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		_, _ = run("loginctl", "enable-linger", user)
	}
}
