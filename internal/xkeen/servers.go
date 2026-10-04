package xkeen

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"xkeen-panel/internal/models"
)

var numberedGroupName = regexp.MustCompile(`^\s*(.+?)\s*[·•]\s*[1-9][0-9]*\s*$`)
var autoGroupName = regexp.MustCompile(`(?i)^\s*(?:⚡\s*)?(авто|auto)(\+?)\s*$`)

// NumberedGroupName recognises flattened members of a logical Happ profile.
// The distinctive " · N" / " • N" suffix is added to member names by the
// subscription exporter. Auto and Auto+ remain separate, as do country+ groups.
func NumberedGroupName(name string) string {
	parts := numberedGroupName.FindStringSubmatch(name)
	if len(parts) == 0 {
		return ""
	}
	base := strings.TrimSpace(parts[1])
	if base == "" {
		return ""
	}
	if auto := autoGroupName.FindStringSubmatch(base); len(auto) != 0 {
		if strings.EqualFold(auto[1], "auto") {
			return "⚡ Auto" + auto[2]
		}
		return "⚡ Авто" + auto[2]
	}
	if strings.HasSuffix(base, " +") {
		base = strings.TrimSpace(strings.TrimSuffix(base, " +")) + "+"
	}
	return base
}

// ParseSubscription parses subscription content, base64 or plain text.
func ParseSubscription(content string) ([]models.Server, error) {
	content = strings.TrimPrefix(strings.TrimSpace(content), "\ufeff")
	decoded, err := decodeShareBase64(content)
	if err != nil {
		decoded = []byte(content)
	}

	lines := strings.Split(strings.TrimSpace(string(decoded)), "\n")
	var servers []models.Server

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var server *models.Server
		var parseErr error

		switch {
		case strings.HasPrefix(line, "vless://"):
			server, parseErr = parseVLESS(line)
		case strings.HasPrefix(line, "vmess://"):
			server, parseErr = parseVMess(line)
		case strings.HasPrefix(line, "trojan://"):
			server, parseErr = parseTrojan(line)
		case strings.HasPrefix(line, "ss://"):
			server, parseErr = parseShadowsocks(line)
		default:
			continue
		}

		if parseErr != nil || server == nil {
			continue
		}

		server.ID = len(servers)
		server.GroupName = NumberedGroupName(server.Name)
		server.RawURI = line
		server.Country = detectCountry(server.Name)
		servers = append(servers, *server)
	}

	if len(servers) == 0 {
		return nil, fmt.Errorf("не удалось распарсить ни одного сервера")
	}

	return servers, nil
}

// parseVLESS parses vless://uuid@host:port?params#name
func parseVLESS(uri string) (*models.Server, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = 443
	}

	name := u.Fragment
	if name == "" {
		name = host
	}

	return &models.Server{
		Name:     name,
		Address:  host,
		Port:     port,
		Protocol: "vless",
		Latency:  -1,
	}, nil
}

// parseVMess parses vmess://base64json
func parseVMess(uri string) (*models.Server, error) {
	encoded := strings.TrimPrefix(uri, "vmess://")

	// Decode base64
	decoded, err := decodeShareBase64(encoded)
	if err != nil {
		return nil, fmt.Errorf("ошибка декодирования vmess: %w", err)
	}

	var vmessConfig map[string]interface{}
	if err := json.Unmarshal(decoded, &vmessConfig); err != nil {
		return nil, fmt.Errorf("ошибка парсинга vmess JSON: %w", err)
	}

	address, _ := vmessConfig["add"].(string)
	portStr := fmt.Sprintf("%v", vmessConfig["port"])
	port, _ := strconv.Atoi(portStr)
	name, _ := vmessConfig["ps"].(string)

	if address == "" {
		return nil, fmt.Errorf("vmess: отсутствует адрес")
	}
	if port == 0 {
		port = 443
	}
	if name == "" {
		name = address
	}

	return &models.Server{
		Name:     name,
		Address:  address,
		Port:     port,
		Protocol: "vmess",
		Latency:  -1,
	}, nil
}

// parseTrojan parses trojan://password@host:port?params#name
func parseTrojan(uri string) (*models.Server, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}

	host := u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	if port == 0 {
		port = 443
	}

	name := u.Fragment
	if name == "" {
		name = host
	}

	return &models.Server{
		Name:     name,
		Address:  host,
		Port:     port,
		Protocol: "trojan",
		Latency:  -1,
	}, nil
}

// parseShadowsocks parses ss://base64@host:port#name or ss://base64#name
func parseShadowsocks(uri string) (*models.Server, error) {
	raw := strings.TrimPrefix(uri, "ss://")
	name := ""
	if idx := strings.LastIndex(raw, "#"); idx != -1 {
		name = raw[idx+1:]
		raw = raw[:idx]
	}
	name, err := url.PathUnescape(name)
	if err != nil {
		return nil, fmt.Errorf("неверное название Shadowsocks")
	}
	if !strings.Contains(raw, "@") {
		// Legacy links encode the complete endpoint. Queries belong outside
		// that payload and must not be mistaken for part of its base64.
		decoded, err := decodeShareBase64(strings.Split(raw, "?")[0])
		if err != nil {
			return nil, fmt.Errorf("не удалось декодировать ss URI")
		}
		raw = string(decoded)
	}
	u, err := url.Parse("ss://" + raw)
	if err != nil || u.User == nil {
		return nil, fmt.Errorf("неверная ссылка Shadowsocks")
	}
	port, err := endpointPort(u)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if name == "" {
		name = host
	}

	return &models.Server{
		Name:     name,
		Address:  host,
		Port:     port,
		Protocol: "shadowsocks",
		Latency:  -1,
	}, nil
}

// CheckLatency measures the TCP connect time to a server.
func CheckLatency(address string, port int, timeout time.Duration) int {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", address, port), timeout)
	if err != nil {
		return -1
	}
	conn.Close()
	return int(time.Since(start).Milliseconds())
}

// CheckAllLatencies probes every server concurrently with a cap on simultaneous
// connections — uncapped, a large subscription takes the router down.
func CheckAllLatencies(servers []models.Server, timeout time.Duration, concurrency int) []models.Server {
	if concurrency <= 0 {
		concurrency = 20
	}

	var wg sync.WaitGroup
	result := make([]models.Server, len(servers))
	copy(result, servers)

	sem := make(chan struct{}, concurrency)
	for i := range result {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			result[idx].Latency = CheckLatency(result[idx].Address, result[idx].Port, timeout)
		}(i)
	}

	wg.Wait()
	return result
}
