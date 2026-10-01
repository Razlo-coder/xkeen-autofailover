package xkeen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// ValidateXray invokes the core directly, so an older XKeen dispatcher cannot
// swallow the validator's exit status. Production config files are only read.
func ValidateXray(rt Runtime) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, rt.CoreBin, "run", "-test", "-confdir", rt.XrayConfDir)
	assets := filepath.Join(filepath.Dir(rt.XrayConfDir), "dat")
	if body, err := os.ReadFile(rt.InitScript); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok && key == "directory_xray_asset" {
				value = strings.Trim(value, "\"'\r ")
				if filepath.IsAbs(value) && !strings.Contains(value, "$") {
					assets = value
				}
			}
		}
	}
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "XRAY_") && !strings.HasPrefix(strings.ToUpper(key), "V2RAY_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "XRAY_LOCATION_ASSET="+assets)
	output, err := cmd.CombinedOutput()
	return string(output), err
}
