package xkeen

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"xkeen-panel/internal/models"
)

func SingleProxy(path string) (map[string]interface{}, error) {
	cfg, err := ReadOutboundsConfig(path)
	if err != nil {
		return nil, err
	}
	obs := asSlice(cfg["outbounds"])
	if countProxyOutbounds(obs) != 1 {
		return nil, fmt.Errorf("проверяемое переключение требует ровно один прокси-outbound")
	}
	_, ob := findProxyOutbound(obs)
	if protocol, _ := ob["protocol"].(string); !SupportedProxyProtocol(protocol) {
		return nil, fmt.Errorf("поддерживаются VLESS, VMess, Trojan и Shadowsocks")
	}
	return ob, nil
}

func OutboundForServer(template map[string]interface{}, s *models.Server) (map[string]interface{}, error) {
	ob, err := buildProxyURI(s, outboundTag(template), detectOutboundFormat(template))
	if err != nil {
		return nil, err
	}
	return mergeOutbound(template, ob), nil
}

// SameOutbound ignores display-only metadata and local socket options while
// comparing credentials, endpoint, transport and TLS/Reality parameters.
func SameOutbound(a, b map[string]interface{}) bool {
	canonical := func(ob map[string]interface{}) interface{} {
		data, _ := json.Marshal(ob)
		var c map[string]interface{}
		json.Unmarshal(data, &c)
		delete(c, "tag")
		ss := mapOf(c["streamSettings"])
		delete(ss, "sockopt")
		if ss != nil {
			n, _ := ss["network"].(string)
			ss["network"] = canonicalNetwork(n)
			// Empty spiderX is equivalent to the generated default "/".
			if rs := mapOf(ss["realitySettings"]); rs != nil {
				if rs["spiderX"] == nil || rs["spiderX"] == "" {
					rs["spiderX"] = "/"
				}
			}
		}
		return c
	}
	return reflect.DeepEqual(canonical(a), canonical(b))
}

func PolicyCountry(s models.Server) string {
	cc := s.CountryOverride
	if cc == "" {
		cc = s.Country
	}
	if cc == "" {
		cc = detectCountry(s.Name)
	}
	return strings.ToUpper(strings.TrimSpace(cc))
}

// PolicyExclusion explains why an entry cannot be selected or probed. This is
// also shown in the UI so filtered entries remain available for configuration.
func PolicyExclusion(s models.Server, policy models.VerifiedFailoverConfig) string {
	if s.Protocol != "" && !SupportedProxyProtocol(s.Protocol) {
		return "Протокол не поддерживается автоматическим переключением"
	}
	for _, name := range policy.ExcludedServerNames {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(s.Name)) {
			return "Исключён в настройках"
		}
	}
	for _, part := range policy.ExcludeNameContains {
		if part != "" && strings.Contains(strings.ToLower(s.Name), strings.ToLower(part)) {
			return "Название содержит исключённую фразу"
		}
	}
	if !policy.AllowOtherCountries {
		allowedCountry := false
		for _, cc := range policy.CountryPriority {
			if strings.EqualFold(strings.TrimSpace(cc), PolicyCountry(s)) {
				allowedCountry = true
				break
			}
		}
		if !allowedCountry {
			return "Страна не разрешена в настройках"
		}
	}
	if s.RawURI != "" {
		if _, err := buildProxyURI(&s, "policy-check", formatFlat); err != nil {
			return err.Error()
		}
	}
	return ""
}

// Countries are considered first, then preferred names within each country.
// Unlisted countries are only allowed when explicitly enabled. Equal ranks
// retain subscription order. Endpoint changes do not reset name preferences.
func PolicyCandidates(servers []models.Server, policy models.VerifiedFailoverConfig) []models.Server {
	var allowed []models.Server
	rank := map[string]int{}
	for i, cc := range policy.CountryPriority {
		rank[strings.ToUpper(strings.TrimSpace(cc))] = i
	}
	for _, s := range servers {
		if PolicyExclusion(s, policy) == "" {
			allowed = append(allowed, s)
		}
	}
	countryRank := func(s models.Server) int {
		if r, ok := rank[PolicyCountry(s)]; ok {
			return r
		}
		return len(rank)
	}
	nameRank := func(s models.Server) int {
		for i, name := range policy.PreferredServerNames {
			if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(s.Name)) {
				return i
			}
		}
		return len(policy.PreferredServerNames)
	}
	sort.SliceStable(allowed, func(i, j int) bool {
		ri, rj := countryRank(allowed[i]), countryRank(allowed[j])
		if ri != rj {
			return ri < rj
		}
		return nameRank(allowed[i]) < nameRank(allowed[j])
	})
	return allowed
}
