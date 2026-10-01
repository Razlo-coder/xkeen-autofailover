package xkeen

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"xkeen-panel/internal/models"
)

func TestProviderIndependentProxyLinks(t *testing.T) {
	vmess := base64.StdEncoding.EncodeToString([]byte(`{"add":"192.0.2.2","port":"443","id":"00000000-0000-4000-8000-000000000002","aid":"0","net":"ws","path":"/vpn","host":"example.com","tls":"tls","sni":"example.com","ps":"Node"}`))
	ss := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:test-password"))
	cases := []struct{ protocol, uri string }{
		{"vless", "vless://00000000-0000-4000-8000-000000000002@192.0.2.2:443?type=tcp"},
		{"trojan", "trojan://test-password@192.0.2.2:443?security=tls&sni=example.com&type=ws&path=%2Fvpn"},
		{"vmess", "vmess://" + vmess},
		{"shadowsocks", "ss://" + ss + "@192.0.2.2:443#Node"},
		{"shadowsocks", "ss://" + base64.StdEncoding.EncodeToString([]byte("aes-128-gcm:test-password@192.0.2.2:443")) + "#Node"},
	}
	for _, tc := range cases {
		t.Run(tc.protocol+tc.uri[:8], func(t *testing.T) {
			template := map[string]interface{}{"tag": "existing-route", "protocol": "vless", "streamSettings": map[string]interface{}{"sockopt": map[string]interface{}{"mark": 255}}}
			ob, err := OutboundForServer(template, &models.Server{Protocol: tc.protocol, RawURI: tc.uri})
			if err != nil {
				t.Fatal(err)
			}
			addr, port, _, ok := readProxyEndpoint(ob)
			if !ok || addr != "192.0.2.2" || port != 443 || ob["protocol"] != tc.protocol || ob["tag"] != "existing-route" {
				t.Fatalf("wrong endpoint or routing tag: %v", ob)
			}
			if !reflect.DeepEqual(mapOf(ob["streamSettings"])["sockopt"], mapOf(template["streamSettings"])["sockopt"]) {
				t.Fatal("lost policy socket options")
			}
			if bin := os.Getenv("TEST_XRAY_BIN"); bin != "" {
				data, _ := json.Marshal(map[string]interface{}{"outbounds": []interface{}{ob}})
				path := filepath.Join(t.TempDir(), "config.json")
				os.WriteFile(path, data, 0600)
				if output, err := exec.Command(bin, "run", "-test", "-config", path).CombinedOutput(); err != nil {
					t.Fatalf("Xray rejected generated %s config: %s", tc.protocol, output)
				}
			}
		})
	}
}

func TestCountryOverrideSurvivesIPRotation(t *testing.T) {
	old := []models.Server{{Name: "Custom node", RawURI: "old-ip", CountryOverride: "DE"}}
	fresh := []models.Server{{Name: "Custom node", RawURI: "new-ip"}}
	carryOverrides(old, fresh)
	if fresh[0].CountryOverride != "DE" {
		t.Fatal("country override lost after endpoint rotation")
	}
}
