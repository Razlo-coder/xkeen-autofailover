package xkeen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"xkeen-panel/internal/models"
)

type SubscriptionManager struct {
	dataDir string
	data    *models.SubscriptionData
	mu      sync.RWMutex
	client  *http.Client
}

// SetHTTPClient is called once, before any background work starts.
func (sm *SubscriptionManager) SetHTTPClient(client *http.Client) { sm.client = client }

// ReconcileActive records only a server actually present in the active config.
// An unmatched config stays usable, but no unrelated subscription entry is
// displayed as active and no first-entry fallback gets applied automatically.
func (sm *SubscriptionManager) ReconcileActive(outbound map[string]interface{}) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.data.ActiveID = sm.matchingServerLocked(outbound)
	for i := range sm.data.Servers {
		sm.data.Servers[i].Active = i == sm.data.ActiveID
	}
}

// MatchConfiguredServer is read-only. The saved selection is only a tie-breaker
// between entries that really match the configuration, never evidence itself.
func (sm *SubscriptionManager) MatchConfiguredServer(outbound map[string]interface{}) *models.Server {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	idx := sm.matchingServerLocked(outbound)
	if idx < 0 {
		return nil
	}
	server := sm.data.Servers[idx]
	return &server
}

func (sm *SubscriptionManager) matchingServerLocked(outbound map[string]interface{}) int {
	first := -1
	for i := range sm.data.Servers {
		ob, err := OutboundForServer(outbound, &sm.data.Servers[i])
		match := err == nil && SameOutbound(outbound, ob)
		if match {
			if i == sm.data.ActiveID {
				return i
			}
			if first < 0 {
				first = i
			}
		}
	}
	return first
}

func NewSubscriptionManager(dataDir string) *SubscriptionManager {
	return &SubscriptionManager{
		dataDir: dataDir,
		data:    &models.SubscriptionData{},
	}
}

func (sm *SubscriptionManager) filePath() string {
	return filepath.Join(sm.dataDir, "subscription.json")
}

// Load reads the stored subscription.
func (sm *SubscriptionManager) Load() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	data, err := os.ReadFile(sm.filePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	return json.Unmarshal(data, sm.data)
}

// Save writes the subscription to disk.
func (sm *SubscriptionManager) Save() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := os.MkdirAll(sm.dataDir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(sm.data, "", "  ")
	if err != nil {
		return err
	}

	return writeFileAtomic(sm.filePath(), data, 0600)
}

// UpdateURL sets a new subscription URL, then downloads and parses it.
func (sm *SubscriptionManager) UpdateURL(url string) ([]models.Server, error) {
	return sm.UpdateURLContext(context.Background(), url)
}

func (sm *SubscriptionManager) UpdateURLContext(ctx context.Context, url string) ([]models.Server, error) {
	return sm.UpdateSourceContext(ctx, 0, url)
}

// UpdateSourceContext replaces one subscription without discarding the other.
// An empty secondary URL removes that source; the primary cannot be removed.
func (sm *SubscriptionManager) UpdateSourceContext(ctx context.Context, source int, url string) ([]models.Server, error) {
	if source < 0 || source > 1 {
		return nil, fmt.Errorf("неизвестный номер подписки")
	}
	url = strings.TrimSpace(url)
	if url == "" {
		if source != 1 {
			return nil, fmt.Errorf("URL основной подписки обязателен")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sm.mu.Lock()
		sm.data.SecondaryURL = ""
		sm.applyRefreshLocked(source, nil)
		sm.data.SecondaryLastUpdated = time.Time{}
		servers := append([]models.Server(nil), sm.data.Servers...)
		sm.mu.Unlock()
		return servers, sm.Save()
	}
	servers, err := sm.downloadAndParseContext(ctx, url)
	if err != nil {
		return nil, err
	}

	sm.mu.Lock()
	if err := ctx.Err(); err != nil {
		sm.mu.Unlock()
		return nil, err
	}
	if source == 0 {
		sm.data.URL = url
	} else {
		sm.data.SecondaryURL = url
	}
	sm.applyRefreshLocked(source, servers)
	combined := append([]models.Server(nil), sm.data.Servers...)
	sm.mu.Unlock()

	return combined, sm.Save()
}

// Refresh reloads the servers from the current URL.
func (sm *SubscriptionManager) Refresh() ([]models.Server, error) {
	return sm.RefreshContext(context.Background())
}

func (sm *SubscriptionManager) RefreshContext(ctx context.Context) ([]models.Server, error) {
	sm.mu.RLock()
	urls := [2]string{sm.data.URL, sm.data.SecondaryURL}
	sm.mu.RUnlock()

	if urls[0] == "" && urls[1] == "" {
		return nil, fmt.Errorf("URL подписки не задан")
	}
	type download struct {
		servers []models.Server
		err     error
	}
	var results [2]download
	var wg sync.WaitGroup
	for source, url := range urls {
		if url == "" {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[source].servers, results[source].err = sm.downloadAndParseContext(ctx, url)
		}()
	}
	wg.Wait()

	sm.mu.Lock()
	if err := ctx.Err(); err != nil {
		sm.mu.Unlock()
		return nil, err
	}
	succeeded := false
	var failures []string
	for source, url := range urls {
		if url == "" || url != sm.sourceURLLocked(source) {
			continue
		}
		if results[source].err != nil {
			failures = append(failures, fmt.Sprintf("подписка %d: %v", source+1, results[source].err))
			continue
		}
		sm.applyRefreshLocked(source, results[source].servers)
		succeeded = true
	}
	servers := append([]models.Server(nil), sm.data.Servers...)
	sm.mu.Unlock()
	if !succeeded {
		if len(failures) == 0 {
			return nil, fmt.Errorf("подписки изменились во время обновления")
		}
		return nil, fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	if len(failures) > 0 {
		log.Printf("[SUBSCRIPTION] Частичное обновление: %s; сохранённые серверы недоступной подписки оставлены", strings.Join(failures, "; "))
	}
	return servers, sm.Save()
}

func (sm *SubscriptionManager) sourceURLLocked(source int) string {
	if source == 1 {
		return sm.data.SecondaryURL
	}
	return sm.data.URL
}

// applyRefreshLocked replaces only one source and keeps the active server and
// country overrides tied to both source and URI. Call with sm.mu held.
func (sm *SubscriptionManager) applyRefreshLocked(source int, servers []models.Server) {
	var activeURI string
	activeSource := 0
	if sm.data.ActiveID >= 0 && sm.data.ActiveID < len(sm.data.Servers) {
		active := sm.data.Servers[sm.data.ActiveID]
		activeURI, activeSource = active.RawURI, active.SourceID
	}
	var oldSource, combined []models.Server
	for _, old := range sm.data.Servers {
		if old.SourceID == source {
			oldSource = append(oldSource, old)
		} else {
			combined = append(combined, old)
		}
	}
	carryOverrides(oldSource, servers)
	for i := range servers {
		servers[i].SourceID = source
	}
	if source == 0 {
		combined = append(servers, combined...)
		sm.data.LastUpdated = time.Now()
	} else {
		combined = append(combined, servers...)
		sm.data.SecondaryLastUpdated = time.Now()
	}
	sm.data.Servers = combined

	newActive := 0
	if activeURI != "" {
		for i := range combined {
			if combined[i].SourceID == activeSource && combined[i].RawURI == activeURI {
				newActive = i
				break
			}
		}
	}
	sm.data.ActiveID = newActive
	for i := range sm.data.Servers {
		sm.data.Servers[i].ID = i
		sm.data.Servers[i].Active = i == newActive
	}
}

// carryOverrides moves manual CountryOverride values onto the new list by RawURI.
func carryOverrides(old, fresh []models.Server) {
	if len(old) == 0 {
		return
	}
	overrides := make(map[string]string, len(old))
	byName := map[string]string{}
	nameCounts := map[string]int{}
	freshNameCounts := map[string]int{}
	for _, s := range fresh {
		freshNameCounts[strings.ToLower(strings.TrimSpace(s.Name))]++
	}
	for i := range old {
		name := strings.ToLower(strings.TrimSpace(old[i].Name))
		nameCounts[name]++
		if old[i].CountryOverride != "" {
			byName[name] = old[i].CountryOverride
		}
		if old[i].CountryOverride != "" && old[i].RawURI != "" {
			overrides[old[i].RawURI] = old[i].CountryOverride
		}
	}
	for i := range fresh {
		if ov, ok := overrides[fresh[i].RawURI]; ok {
			fresh[i].CountryOverride = ov
		} else if name := strings.ToLower(strings.TrimSpace(fresh[i].Name)); name != "" && nameCounts[name] == 1 && freshNameCounts[name] == 1 {
			fresh[i].CountryOverride = byName[name]
		}
	}
}

// GetData returns a copy of the subscription. Servers is deep-copied: otherwise
// the caller would read the live slice unlocked, racing SetActive and friends.
func (sm *SubscriptionManager) GetData() models.SubscriptionData {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	d := *sm.data
	d.Servers = append([]models.Server(nil), sm.data.Servers...)
	return d
}

// GetServers returns the server list.
func (sm *SubscriptionManager) GetServers() []models.Server {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]models.Server, len(sm.data.Servers))
	copy(result, sm.data.Servers)
	return result
}

// UpdateLatencies stores measured latencies back into the subscription by RawURI,
// so the UI shows them at once and does not lose them on refresh.
func (sm *SubscriptionManager) UpdateLatencies(checked []models.Server) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	byURI := make(map[string]int, len(checked))
	for _, c := range checked {
		if c.RawURI != "" {
			byURI[fmt.Sprintf("%d:%s", c.SourceID, c.RawURI)] = c.Latency
		}
	}

	now := time.Now()
	for i := range sm.data.Servers {
		if lat, ok := byURI[fmt.Sprintf("%d:%s", sm.data.Servers[i].SourceID, sm.data.Servers[i].RawURI)]; ok {
			sm.data.Servers[i].Latency = lat
			sm.data.Servers[i].LastChecked = now
		}
	}

	data, err := json.MarshalIndent(sm.data, "", "  ")

	if err != nil {
		return
	}
	if err := os.MkdirAll(sm.dataDir, 0700); err == nil {
		writeFileAtomic(sm.filePath(), data, 0600)
	}
}

// SetCountryOverride sets a server's country by hand — needed in strict mode when
// detection could not resolve it.
func (sm *SubscriptionManager) SetCountryOverride(id int, country string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if id < 0 || id >= len(sm.data.Servers) {
		return fmt.Errorf("сервер с id %d не найден", id)
	}

	sm.data.Servers[id].CountryOverride = strings.ToUpper(strings.TrimSpace(country))

	data, err := json.MarshalIndent(sm.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(sm.dataDir, 0700); err != nil {
		return err
	}
	return writeFileAtomic(sm.filePath(), data, 0600)
}

// SetActive makes the server with the given id active.
func (sm *SubscriptionManager) SetActive(id int) (*models.Server, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if id < 0 || id >= len(sm.data.Servers) {
		return nil, fmt.Errorf("сервер с id %d не найден", id)
	}

	sm.data.ActiveID = id
	for i := range sm.data.Servers {
		sm.data.Servers[i].Active = i == id
	}

	server := sm.data.Servers[id]

	// Saving in a goroutine would race the next call — write synchronously
	data, err := json.MarshalIndent(sm.data, "", "  ")
	if err != nil {
		return nil, err
	}

	if err := os.MkdirAll(sm.dataDir, 0700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(sm.filePath(), data, 0600); err != nil {
		return nil, err
	}

	return &server, nil
}

// SetActiveByRawURI activates a server by its stable RawURI under a single lock.
// Safer than SetActive(id) when a Refresh may have run between snapshot and
// activation: indices move, RawURI does not.
func (sm *SubscriptionManager) SetActiveByRawURI(uri string, sourceID ...int) (*models.Server, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	idx := -1
	for i := range sm.data.Servers {
		if sm.data.Servers[i].RawURI == uri && (len(sourceID) == 0 || sm.data.Servers[i].SourceID == sourceID[0]) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("сервер не найден по RawURI")
	}

	sm.data.ActiveID = idx
	for i := range sm.data.Servers {
		sm.data.Servers[i].Active = i == idx
	}
	server := sm.data.Servers[idx]

	data, err := json.MarshalIndent(sm.data, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(sm.dataDir, 0700); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(sm.filePath(), data, 0600); err != nil {
		return nil, err
	}

	return &server, nil
}

// GetActiveServer returns the active server.
func (sm *SubscriptionManager) GetActiveServer() *models.Server {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.data.Servers) == 0 {
		return nil
	}

	id := sm.data.ActiveID
	if id < 0 || id >= len(sm.data.Servers) {
		return nil
	}

	s := sm.data.Servers[id]
	return &s
}

// SelectNext switches to the next server in the list.
func (sm *SubscriptionManager) SelectNext() (*models.Server, error) {
	sm.mu.RLock()
	count := len(sm.data.Servers)
	current := sm.data.ActiveID
	sm.mu.RUnlock()

	if count == 0 {
		return nil, fmt.Errorf("нет доступных серверов")
	}

	next := (current + 1) % count
	return sm.SetActive(next)
}

func (sm *SubscriptionManager) downloadAndParse(url string) ([]models.Server, error) {
	return sm.downloadAndParseContext(context.Background(), url)
}

func (sm *SubscriptionManager) downloadAndParseContext(ctx context.Context, url string) ([]models.Server, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	if sm.client != nil {
		client = sm.client
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, subscriptionDownloadError(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, subscriptionDownloadError(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("сервер вернул код %d", resp.StatusCode)
	}

	const maxBody = 4 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа: %w", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("подписка превышает 4 МБ")
	}

	return ParseSubscription(string(body))
}

func subscriptionDownloadError(err error) error {
	// http.Client includes the full URL (a bearer credential) in url.Error.
	if e, ok := err.(*url.Error); ok {
		err = e.Err
	}
	return fmt.Errorf("ошибка загрузки подписки: %w", err)
}
