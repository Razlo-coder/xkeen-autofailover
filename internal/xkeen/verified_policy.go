package xkeen

import (
	"crypto/sha256"
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
	ca, ea := canonicalOutbound(a)
	cb, eb := canonicalOutbound(b)
	return ea == nil && eb == nil && reflect.DeepEqual(ca, cb)
}

func canonicalOutbound(ob map[string]interface{}) (map[string]interface{}, error) {
	if ob == nil {
		return nil, fmt.Errorf("нет outbound")
	}
	data, err := json.Marshal(ob)
	if err != nil {
		return nil, err
	}
	var c map[string]interface{}
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
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
	return c, nil
}

// OutboundFingerprint binds display metadata to the actual connection without
// storing credentials or assuming that a same-name subscription entry is live.
func OutboundFingerprint(ob map[string]interface{}) (string, error) {
	canonical, err := canonicalOutbound(ob)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
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
	for _, s := range servers {
		if PolicyExclusion(s, policy) == "" {
			allowed = append(allowed, s)
		}
	}
	sort.SliceStable(allowed, func(i, j int) bool {
		return PolicyBetter(allowed[i], allowed[j], policy)
	})
	return allowed
}

func policyRank(s models.Server, policy models.VerifiedFailoverConfig) (int, int) {
	country, name := len(policy.CountryPriority), len(policy.PreferredServerNames)
	for i, cc := range policy.CountryPriority {
		if strings.EqualFold(strings.TrimSpace(cc), PolicyCountry(s)) {
			country = i
			break
		}
	}
	for i, preferred := range policy.PreferredServerNames {
		if strings.EqualFold(strings.TrimSpace(preferred), strings.TrimSpace(s.Name)) {
			name = i
			break
		}
	}
	return country, name
}

// PolicyBetter compares explicit country/name preferences only. Subscription
// order breaks selection ties, but never causes a healthy connection to rotate.
func PolicyBetter(candidate, current models.Server, policy models.VerifiedFailoverConfig) bool {
	cc, cn := policyRank(candidate, policy)
	ac, an := policyRank(current, policy)
	return cc < ac || (cc == ac && cn < an)
}

// A configured higher rank may reappear on the next subscription refresh even
// when it is absent from the cached list.
func PolicyHasHigherPriority(current models.Server, policy models.VerifiedFailoverConfig) bool {
	country, name := policyRank(current, policy)
	return country > 0 || name > 0
}
