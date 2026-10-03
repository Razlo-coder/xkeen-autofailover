package xkeen

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMainCoreInstanceUsesBootAndStartTimeExcludingProbes(t *testing.T) {
	root := t.TempDir()
	bootFile := filepath.Join(root, "sys/kernel/random/boot_id")
	os.MkdirAll(filepath.Dir(bootFile), 0700)
	os.WriteFile(bootFile, []byte("boot-A\n"), 0600)
	writeProcess := func(pid int, cmd, ticks, state string) {
		t.Helper()
		dir := filepath.Join(root, strconv.Itoa(pid))
		os.MkdirAll(dir, 0700)
		os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmd), 0600)
		fields := append([]string{state}, strings.Fields(strings.Repeat("0 ", 18))...)
		fields = append(fields, ticks, "0")
		os.WriteFile(filepath.Join(dir, "stat"), []byte(strconv.Itoa(pid)+" (xray (main)) "+strings.Join(fields, " ")), 0600)
	}
	writeProcess(100, "/opt/sbin/xray\x00run\x00-confdir\x00/opt/etc/xray/configs\x00", "123", "S")
	writeProcess(200, "/opt/sbin/xray\x00run\x00-config\x00/tmp/panel-vpn-probe-123/config.json\x00", "234", "S")
	if got := mainCoreInstanceFromProc(root, "xray", 200); got != "boot-A:100:123" {
		t.Fatalf("probe/stale PID hint affected main identity: %q", got)
	}
	writeProcess(100, "/opt/sbin/xray\x00", "456", "S")
	if got := mainCoreInstanceFromProc(root, "xray", 100); got != "boot-A:100:456" {
		t.Fatal("reused PID did not change core identity")
	}
	os.WriteFile(bootFile, []byte("boot-B"), 0600)
	if got := mainCoreInstanceFromProc(root, "xray", 100); got != "boot-B:100:456" {
		t.Fatal("router reboot did not change core identity")
	}
	writeProcess(100, "/opt/sbin/xray\x00", "456", "Z")
	if got := mainCoreInstanceFromProc(root, "xray", 100); got != "" {
		t.Fatal("zombie/probe counted as a live main core")
	}
	writeProcess(100, "/opt/sbin/xray\x00", "456", "S")
	writeProcess(300, "/opt/sbin/xray\x00", "789", "S")
	if got := mainCoreInstanceFromProc(root, "xray", 0); got != "" {
		t.Fatal("ambiguous main processes guessed")
	}
	os.RemoveAll(filepath.Join(root, "300"))
	os.Remove(bootFile)
	os.WriteFile(filepath.Join(root, "stat"), []byte("cpu 1 2 3 4\nbtime 1710000000\n"), 0600)
	if got := mainCoreInstanceFromProc(root, "xray", 100); got != "btime 1710000000:100:456" {
		t.Fatalf("router without boot_id cannot retain uptime: %q", got)
	}
}
