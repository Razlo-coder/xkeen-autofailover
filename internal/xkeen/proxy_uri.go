package xkeen

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"xkeen-panel/internal/models"
)

func SupportedProxyProtocol(protocol string) bool {
	switch protocol {
	case "vless", "vmess", "trojan", "shadowsocks":
		return true
	}
	return false
}

func decodeShareBase64(value string) ([]byte, error) {
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if data, err := encoding.DecodeString(value); err == nil {
			return data, nil
		}
	}
	return nil, fmt.Errorf("неверный base64")
}

func uriStream(u *url.URL) *VLESSParams {
	q := u.Query()
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("peer")
	}
	path := q.Get("path")
	if q.Get("serviceName") != "" {
		path = q.Get("serviceName")
	}
	return &VLESSParams{Address: u.Hostname(), Network: q.Get("type"), Security: q.Get("security"), SNI: sni,
		Fingerprint: q.Get("fp"), PublicKey: q.Get("pbk"), ShortID: q.Get("sid"), Host: q.Get("host"),
		Path: path, Mode: q.Get("mode"), ALPN: q.Get("alpn"), Extra: q.Get("extra")}
}

func endpointPort(u *url.URL) (int, error) {
	port := 443
	if u.Port() != "" {
		var err error
		port, err = strconv.Atoi(u.Port())
		if err != nil {
			return 0, fmt.Errorf("неверный порт")
		}
	}
	if u.Hostname() == "" || port < 1 || port > 65535 {
		return 0, fmt.Errorf("неверный адрес или порт")
	}
	return port, nil
}

// buildProxyURI converts provider-independent share links, never guessing when
// a link requires a plugin or obsolete VMess authentication.
func buildProxyURI(s *models.Server, tag string, format outboundFormat) (map[string]interface{}, error) {
	if s == nil || s.RawURI == "" {
		return nil, fmt.Errorf("нет ссылки подключения: обновите подписку")
	}
	u, err := url.Parse(s.RawURI)
	if err != nil {
		return nil, fmt.Errorf("неверная ссылка подключения")
	}
	switch u.Scheme {
	case "vless":
		p, err := ParseVLESS(s.RawURI)
		if err != nil {
			return nil, fmt.Errorf("неверная ссылка VLESS")
		}
		return buildOutboundFromURI(p, tag, format), nil
	case "trojan":
		port, err := endpointPort(u)
		if err != nil || u.User == nil || u.User.Username() == "" {
			return nil, fmt.Errorf("неверная ссылка Trojan")
		}
		p := uriStream(u)
		if p.Security == "" {
			p.Security = "tls"
		}
		password := u.User.Username()
		if suffix, ok := u.User.Password(); ok {
			password += ":" + suffix
		}
		ob := buildOutboundFromURI(p, tag, formatFlat)
		ob["protocol"] = "trojan"
		ob["settings"] = map[string]interface{}{"servers": []interface{}{map[string]interface{}{"address": u.Hostname(), "port": port, "password": password}}}
		return ob, nil
	case "ss":
		if u.Query().Get("plugin") != "" {
			return nil, fmt.Errorf("Shadowsocks с плагином не поддерживается")
		}
		// SIP002: method:password may be plaintext or base64. Legacy links encode
		// the entire method:password@host:port instead.
		if u.User == nil {
			raw := strings.TrimPrefix(strings.Split(s.RawURI, "#")[0], "ss://")
			raw = strings.Split(raw, "?")[0]
			decoded, err := decodeShareBase64(raw)
			if err != nil {
				return nil, fmt.Errorf("неверная ссылка Shadowsocks")
			}
			u, err = url.Parse("ss://" + string(decoded))
			if err != nil {
				return nil, fmt.Errorf("неверная ссылка Shadowsocks")
			}
		}
		port, err := endpointPort(u)
		if err != nil || u.User == nil {
			return nil, fmt.Errorf("неверная ссылка Shadowsocks")
		}
		method, password, ok := u.User.Username(), "", false
		if password, ok = u.User.Password(); !ok {
			decoded, err := decodeShareBase64(method)
			if err != nil {
				return nil, fmt.Errorf("неверные параметры Shadowsocks")
			}
			method, password, ok = strings.Cut(string(decoded), ":")
		}
		if !ok || method == "" || password == "" {
			return nil, fmt.Errorf("неверные параметры Shadowsocks")
		}
		return map[string]interface{}{"tag": tag, "protocol": "shadowsocks", "settings": map[string]interface{}{"servers": []interface{}{map[string]interface{}{"address": u.Hostname(), "port": port, "method": method, "password": password}}}, "streamSettings": map[string]interface{}{"network": "tcp"}}, nil
	case "vmess":
		decoded, err := decodeShareBase64(strings.TrimPrefix(s.RawURI, "vmess://"))
		if err != nil {
			return nil, fmt.Errorf("неверная ссылка VMess")
		}
		var v map[string]interface{}
		if json.Unmarshal(decoded, &v) != nil {
			return nil, fmt.Errorf("неверная ссылка VMess")
		}
		text := func(key string) string {
			value := v[key]
			if value == nil {
				return ""
			}
			return fmt.Sprint(value)
		}
		port, err := strconv.Atoi(text("port"))
		if err != nil || port < 1 || port > 65535 || text("add") == "" || text("id") == "" {
			return nil, fmt.Errorf("неверный адрес, порт или пользователь VMess")
		}
		if aid := text("aid"); aid != "" && aid != "0" {
			return nil, fmt.Errorf("устаревший VMess alterId не поддерживается")
		}
		if header := text("type"); header != "" && header != "none" {
			return nil, fmt.Errorf("VMess с нестандартным заголовком не поддерживается")
		}
		p := &VLESSParams{Address: text("add"), Network: text("net"), SNI: text("sni"), Host: text("host"), Path: text("path"), Fingerprint: text("fp"), ALPN: text("alpn")}
		if text("tls") == "tls" {
			p.Security = "tls"
		}
		security := text("scy")
		if security == "" {
			security = "auto"
		}
		ob := buildOutboundFromURI(p, tag, format)
		ob["protocol"] = "vmess"
		if format == formatVNext {
			ob["settings"] = map[string]interface{}{"vnext": []interface{}{map[string]interface{}{"address": p.Address, "port": port, "users": []interface{}{map[string]interface{}{"id": text("id"), "security": security, "alterId": 0}}}}}
		} else {
			ob["settings"] = map[string]interface{}{"address": p.Address, "port": port, "id": text("id"), "security": security}
		}
		return ob, nil
	default:
		return nil, fmt.Errorf("протокол ссылки не поддерживается")
	}
}
