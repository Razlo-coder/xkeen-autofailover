package xkeen

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MainCoreInstance distinguishes a panel restart from a core/router restart.
// Probe children never count. PID reuse is guarded by boot ID and start ticks.
func MainCoreInstance(core string) string {
	if core == "" {
		core = CoreXray
	}
	pid, _ := readPIDFile("/opt/var/run/" + core + ".pid")
	return mainCoreInstanceFromProc("/proc", core, pid)
}

func mainCoreInstanceFromProc(root, core string, hint int) string {
	boot, err := os.ReadFile(filepath.Join(root, "sys/kernel/random/boot_id"))
	if err != nil || strings.TrimSpace(string(boot)) == "" {
		// Some router kernels omit boot_id; btime still separates rebooted PIDs.
		if stat, statErr := os.ReadFile(filepath.Join(root, "stat")); statErr == nil {
			for _, line := range strings.Split(string(stat), "\n") {
				if strings.HasPrefix(line, "btime ") {
					boot = []byte(strings.TrimSpace(line))
					break
				}
			}
		}
		if strings.TrimSpace(string(boot)) == "" {
			return ""
		}
	}
	instance := func(pid string) string {
		cmdline, err := os.ReadFile(filepath.Join(root, pid, "cmdline"))
		parts := bytes.Split(cmdline, []byte{0})
		if err != nil || len(parts) == 0 || filepath.Base(string(parts[0])) != core || bytes.Contains(cmdline, []byte("panel-vpn-probe-")) {
			return ""
		}
		stat, err := os.ReadFile(filepath.Join(root, pid, "stat"))
		if err != nil {
			return ""
		}
		// /proc/PID/stat field 22; comm may contain spaces or parentheses.
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			return ""
		}
		fields := strings.Fields(string(stat[end+1:]))
		if len(fields) < 20 || fields[0] == "Z" || fields[0] == "X" || fields[0] == "x" {
			return ""
		}
		if ticks, err := strconv.ParseUint(fields[19], 10, 64); err != nil || ticks == 0 {
			return ""
		}
		return strings.TrimSpace(string(boot)) + ":" + pid + ":" + fields[19]
	}
	if hint > 0 {
		if token := instance(strconv.Itoa(hint)); token != "" {
			return token
		}
	}
	entries, _ := os.ReadDir(root)
	var found string
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		if token := instance(entry.Name()); token != "" {
			if found != "" {
				return "" // Cannot bind a session to an ambiguous main process.
			}
			found = token
		}
	}
	return found
}
