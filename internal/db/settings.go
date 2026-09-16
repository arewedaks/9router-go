package db

import (
	json "encoding/json/v2"

	"9router/proxy/internal/handlerutil"
)

// ProviderStrategy defines routing and proxy pool options for a specific provider.
type ProviderStrategy struct {
	ProxyPoolID    string `json:"proxyPoolId"`
	RotateStrategy string `json:"rotateStrategy"` // "none", "round-robin", "random", "sticky"
	StickyLimit    int    `json:"stickyLimit,omitempty"`
}

// SettingsData represents token saver and general settings stored in the settings table.
type SettingsData struct {
	RTKEnabled         bool                        `json:"rtkEnabled"`
	CavemanEnabled     bool                        `json:"cavemanEnabled"`
	CavemanLevel       string                      `json:"cavemanLevel"`
	PonytailEnabled    bool                        `json:"ponytailEnabled"`
	PonytailLevel      string                      `json:"ponytailLevel"`
	HeadroomUrl        string                      `json:"headroomUrl"`
	HeadroomCodeAware  bool                        `json:"headroomCodeAware"`
	HeadroomKompress   bool                        `json:"headroomKompress"`
	HeadroomTimeoutMs  int                         `json:"headroomTimeoutMs"`
	AutoUpdate         bool                        `json:"autoUpdate"`
	ProviderStrategies map[string]ProviderStrategy `json:"providerStrategies,omitempty"`

	// PasswordHash is the bcrypt hash of the dashboard login password. Empty
	// means no password has been set yet, in which case login falls back to
	// InitialPassword (the "123456" default, VansRouter-compatible). It is never
	// returned to clients.
	PasswordHash string `json:"password,omitempty"`
	// RequireLogin mirrors VansRouter's requireLogin: when explicitly false the
	// dashboard is open and no session is needed. Nil (absent) means the default,
	// which is to require login.
	RequireLogin *bool `json:"requireLogin,omitempty"`
}

// DefaultSettings returns fallback settings.
func DefaultSettings() *SettingsData {
	return &SettingsData{
		RTKEnabled:        true,
		CavemanEnabled:    false,
		CavemanLevel:      "full",
		PonytailEnabled:   false,
		PonytailLevel:     "full",
		HeadroomUrl:       "http://localhost:8787",
		HeadroomKompress:  true,
		HeadroomTimeoutMs: 3000,
		AutoUpdate:        false,
	}
}

// GetSettings reads settings row id = 1 from SQLite settings table.
func (r *Repo) GetSettings() (*SettingsData, error) {
	var rawData string
	err := r.db.QueryRow(`SELECT data FROM settings WHERE id = 1`).Scan(&rawData)
	if err != nil {
		return DefaultSettings(), nil
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(rawData), &raw); err != nil {
		return DefaultSettings(), nil
	}

	s := DefaultSettings()
	if v, ok := raw["rtkEnabled"].(bool); ok {
		s.RTKEnabled = v
	}
	if v, ok := raw["cavemanEnabled"].(bool); ok {
		s.CavemanEnabled = v
	}
	if lvl := handlerutil.GetString(raw, "cavemanLevel"); lvl != "" {
		s.CavemanLevel = lvl
	}
	if v, ok := raw["ponytailEnabled"].(bool); ok {
		s.PonytailEnabled = v
	}
	if lvl := handlerutil.GetString(raw, "ponytailLevel"); lvl != "" {
		s.PonytailLevel = lvl
	}
	if v := handlerutil.GetString(raw, "headroomUrl"); v != "" {
		s.HeadroomUrl = v
	}
	if v, ok := raw["headroomCodeAware"].(bool); ok {
		s.HeadroomCodeAware = v
	}
	if v, ok := raw["headroomKompress"].(bool); ok {
		s.HeadroomKompress = v
	}
	if v, ok := raw["headroomTimeoutMs"].(float64); ok && v > 0 {
		s.HeadroomTimeoutMs = int(v)
	}
	if v, ok := raw["autoUpdate"].(bool); ok {
		s.AutoUpdate = v
	}
	if v := handlerutil.GetString(raw, "password"); v != "" {
		s.PasswordHash = v
	}
	if v, ok := raw["requireLogin"].(bool); ok {
		s.RequireLogin = &v
	}
	if ps, ok := raw["providerStrategies"].(map[string]any); ok {
		s.ProviderStrategies = make(map[string]ProviderStrategy)
		for k, v := range ps {
			if vm, ok := v.(map[string]any); ok {
				strat := ProviderStrategy{
					ProxyPoolID:    handlerutil.GetString(vm, "proxyPoolId"),
					RotateStrategy: handlerutil.GetString(vm, "rotateStrategy"),
				}
				if sl, ok := vm["stickyLimit"].(float64); ok && sl > 0 {
					strat.StickyLimit = int(sl)
				}
				s.ProviderStrategies[k] = strat
			}
		}
	}

	return s, nil
}

// SetPasswordHash stores (or clears, when hash is empty) the dashboard login
// password hash. Mirrors VansRouter's `updateSettings({ password })`.
func (r *Repo) SetPasswordHash(hash string) error {
	s, err := r.GetSettings()
	if err != nil {
		s = DefaultSettings()
	}
	s.PasswordHash = hash
	return r.saveSettings(s)
}

// SetRequireLogin toggles whether the dashboard demands a login. Passing nil
// removes the override so the default (require login) applies again.
func (r *Repo) SetRequireLogin(require *bool) error {
	s, err := r.GetSettings()
	if err != nil {
		s = DefaultSettings()
	}
	s.RequireLogin = require
	return r.saveSettings(s)
}

// saveSettings persists the whole settings blob for row id = 1.
func (r *Repo) saveSettings(s *SettingsData) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(b))
	return err
}

// SetAutoUpdate updates the autoUpdate flag in the settings table.
func (r *Repo) SetAutoUpdate(enabled bool) error {
	s, err := r.GetSettings()
	if err != nil {
		s = DefaultSettings()
	}
	s.AutoUpdate = enabled
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(b))
	return err
}

// SetProviderStrategy updates or sets the routing strategy and proxy pool for a provider.
func (r *Repo) SetProviderStrategy(provider string, strat ProviderStrategy) error {
	s, err := r.GetSettings()
	if err != nil {
		s = DefaultSettings()
	}
	if s.ProviderStrategies == nil {
		s.ProviderStrategies = make(map[string]ProviderStrategy)
	}
	s.ProviderStrategies[provider] = strat
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = r.db.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, string(b))
	return err
}
