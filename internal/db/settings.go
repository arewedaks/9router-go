package db

import (
	json "encoding/json/v2"
	"os"
	"strings"

	"9router/proxy/internal/handlerutil"
)

// ProviderStrategy defines routing and proxy pool options for a specific provider.
type ProviderStrategy struct {
	ProxyPoolID    string `json:"proxyPoolId"`
	RotateStrategy string `json:"rotateStrategy"` // "none", "round-robin", "random", "sticky"
	StickyLimit    int    `json:"stickyLimit,omitempty"`
	// TargetProxyPoolIds narrows dynamic rotation (rotateStrategy != "none") to a
	// subset of active pools. Empty means "all active pools are eligible".
	// Mirrors VansRouter's settings.providerStrategies[id].targetProxyPoolIds.
	TargetProxyPoolIds []string `json:"targetProxyPoolIds,omitempty"`

	// FallbackStrategy rotates between a provider's own ACCOUNTS, not its proxy
	// pools: "fill-first" (default) always uses the highest-priority connection,
	// "round-robin" spreads requests so no single account carries the load.
	// VansRouter stores this under the same name and reads it in
	// src/sse/services/auth.js.
	FallbackStrategy string `json:"fallbackStrategy,omitempty"`
	// StickyRoundRobinLimit is how many consecutive requests one account serves
	// before rotation moves on. VansRouter defaults it to 3; 1 rotates every
	// request. Only read when FallbackStrategy is "round-robin".
	StickyRoundRobinLimit int `json:"stickyRoundRobinLimit,omitempty"`

	// Extra holds every key of this provider's strategy object that this struct
	// does not model, exactly as it was stored. The settings blob is shared with
	// VansRouter, which also writes strictModelAssignment here. Dropping those on
	// read would make a settings round-trip destroy the operator's real
	// configuration, so unknown keys are carried through untouched.
	Extra map[string]any `json:"-"`
}

// knownStrategyKeys lists the keys represented by the typed fields above. Any
// other key found in a stored strategy object is preserved in Extra.
var knownStrategyKeys = map[string]bool{
	"proxyPoolId":           true,
	"rotateStrategy":        true,
	"stickyLimit":           true,
	"targetProxyPoolIds":    true,
	"fallbackStrategy":      true,
	"stickyRoundRobinLimit": true,
}

// IsEmpty reports whether the strategy carries no configuration at all, counting
// preserved passthrough keys. Only a truly empty object may be pruned from the
// settings blob.
func (p ProviderStrategy) IsEmpty() bool {
	return p.ProxyPoolID == "" &&
		p.RotateStrategy == "" &&
		p.StickyLimit == 0 &&
		len(p.TargetProxyPoolIds) == 0 &&
		p.FallbackStrategy == "" &&
		p.StickyRoundRobinLimit == 0 &&
		len(p.Extra) == 0
}

// WantsAccountRoundRobin reports whether this provider should spread requests
// across its accounts. "fill-first", "none" and "" all mean no rotation, which
// is the historical behaviour.
func (p ProviderStrategy) WantsAccountRoundRobin() bool {
	return strings.EqualFold(strings.TrimSpace(p.FallbackStrategy), "round-robin")
}

// AccountStickyLimit returns how many consecutive requests one account serves
// before the rotation advances. Defaults to 3 to match VansRouter, whose UI
// seeds the field with 3 when the toggle is switched on.
func (p ProviderStrategy) AccountStickyLimit() int {
	if p.StickyRoundRobinLimit > 0 {
		return p.StickyRoundRobinLimit
	}
	return defaultAccountStickyLimit
}

// defaultAccountStickyLimit mirrors VansRouter's 3. A value of 1 would rotate on
// every request, which is worse for prompt caching: each account keeps its own
// cache, so hopping every request discards it.
const defaultAccountStickyLimit = 3

// UnmarshalJSON fills the typed fields and routes every other key into Extra, so
// the request-body decode path (POST /settings) is lossless too — not just the
// stored-row decode in GetSettings.
func (p *ProviderStrategy) UnmarshalJSON(data []byte) error {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = ProviderStrategy{
		ProxyPoolID:        handlerutil.GetString(raw, "proxyPoolId"),
		RotateStrategy:     handlerutil.GetString(raw, "rotateStrategy"),
		TargetProxyPoolIds: parseStringSlice(raw["targetProxyPoolIds"]),
		FallbackStrategy:   handlerutil.GetString(raw, "fallbackStrategy"),
	}
	if sl, ok := raw["stickyLimit"].(float64); ok && sl > 0 {
		p.StickyLimit = int(sl)
	}
	if sr, ok := raw["stickyRoundRobinLimit"].(float64); ok && sr > 0 {
		p.StickyRoundRobinLimit = int(sr)
	}
	for k, v := range raw {
		if knownStrategyKeys[k] {
			continue
		}
		if p.Extra == nil {
			p.Extra = make(map[string]any)
		}
		p.Extra[k] = v
	}
	return nil
}

// MarshalJSON writes the typed fields followed by the preserved passthrough
// keys, so unknown VansRouter settings survive a read/write round-trip. Typed
// fields win on collision, which cannot normally happen because Extra only ever
// receives keys that knownStrategyKeys does not list.
func (p ProviderStrategy) MarshalJSON() ([]byte, error) {
	out := make(map[string]any, len(p.Extra)+6)
	for k, v := range p.Extra {
		out[k] = v
	}
	if p.ProxyPoolID != "" {
		out["proxyPoolId"] = p.ProxyPoolID
	}
	if p.RotateStrategy != "" {
		out["rotateStrategy"] = p.RotateStrategy
	}
	if p.StickyLimit != 0 {
		out["stickyLimit"] = p.StickyLimit
	}
	if len(p.TargetProxyPoolIds) > 0 {
		out["targetProxyPoolIds"] = p.TargetProxyPoolIds
	}
	if p.FallbackStrategy != "" {
		out["fallbackStrategy"] = p.FallbackStrategy
	}
	if p.StickyRoundRobinLimit != 0 {
		out["stickyRoundRobinLimit"] = p.StickyRoundRobinLimit
	}
	return json.Marshal(out)
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

	// AntigravityClientProfile is the single, provider-wide client identity
	// ("ide" or "cli") presented to Google's Code Assist backend. It replaced a
	// per-connection setting: the profile describes which official client the
	// operator is emulating, not which account is in use, so storing it once
	// avoids six accounts drifting apart. Empty means "not chosen yet", which
	// resolves to the "ide" default so an untouched install behaves as before.
	AntigravityClientProfile string `json:"antigravityClientProfile,omitempty"`

	// TrustProxy makes the login limiter read X-Forwarded-For instead of the
	// socket peer. Required behind Cloudflare Tunnel or any reverse proxy, where
	// every request arrives from 127.0.0.1 and would otherwise share one lockout
	// bucket, so five bad guesses from anyone lock out everyone.
	//
	// Only safe when the origin cannot be reached directly: if the port is
	// exposed, a client can forge the header to dodge the limiter. That is why
	// the UI pairs it with the HOST bind address rather than trusting it alone.
	TrustProxy *bool `json:"trustProxy,omitempty"`

	// AuthCookieSecure forces the session cookie's Secure flag. A reverse proxy
	// terminates TLS, so the origin sees plain HTTP and cannot infer it from the
	// request; without this the cookie would be sendable over an unencrypted hop.
	AuthCookieSecure *bool `json:"authCookieSecure,omitempty"`

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
	if v := handlerutil.GetString(raw, "antigravityClientProfile"); v != "" {
		s.AntigravityClientProfile = v
	}
	if v, ok := raw["requireLogin"].(bool); ok {
		s.RequireLogin = &v
	}
	if v, ok := raw["trustProxy"].(bool); ok {
		s.TrustProxy = &v
	}
	if v, ok := raw["authCookieSecure"].(bool); ok {
		s.AuthCookieSecure = &v
	}
	if ps, ok := raw["providerStrategies"].(map[string]any); ok {
		s.ProviderStrategies = make(map[string]ProviderStrategy)
		for k, v := range ps {
			if vm, ok := v.(map[string]any); ok {
				strat := ProviderStrategy{
					ProxyPoolID:        handlerutil.GetString(vm, "proxyPoolId"),
					TargetProxyPoolIds: parseStringSlice(vm["targetProxyPoolIds"]),
					RotateStrategy:     handlerutil.GetString(vm, "rotateStrategy"),
					FallbackStrategy:   handlerutil.GetString(vm, "fallbackStrategy"),
				}
				if sl, ok := vm["stickyLimit"].(float64); ok && sl > 0 {
					strat.StickyLimit = int(sl)
				}
				if sr, ok := vm["stickyRoundRobinLimit"].(float64); ok && sr > 0 {
					strat.StickyRoundRobinLimit = int(sr)
				}
				// Preserve any key we do not model (strictModelAssignment, …) so a
				// read/write round-trip cannot destroy it.
				for k, kv := range vm {
					if knownStrategyKeys[k] {
						continue
					}
					if strat.Extra == nil {
						strat.Extra = make(map[string]any)
					}
					strat.Extra[k] = kv
				}
				s.ProviderStrategies[k] = strat
			}
		}
	}

	return s, nil
}

// parseStringSlice extracts a []string from a decoded JSON value, dropping
// empty/non-string entries. Used for providerStrategies.targetProxyPoolIds.
func parseStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

// SetAntigravityClientProfile stores the provider-wide Antigravity client
// identity. An empty value clears the override so the default ("ide") applies.
func (r *Repo) SetAntigravityClientProfile(profile string) error {
	s, err := r.GetSettings()
	if err != nil {
		s = DefaultSettings()
	}
	s.AntigravityClientProfile = profile
	return r.saveSettings(s)
}

// SetProxyAwareness stores the reverse-proxy flags. Both are pointers so an
// absent value keeps meaning "use the environment default" rather than "false",
// which lets an env-only install carry on working unchanged.
func (r *Repo) SetProxyAwareness(trustProxy, cookieSecure *bool) error {
	s, err := r.GetSettings()
	if err != nil {
		s = DefaultSettings()
	}
	s.TrustProxy = trustProxy
	s.AuthCookieSecure = cookieSecure
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

// ListenHost and ListenPort report the address the process was started with.
//
// They live here rather than in the config package so the dashboard can show
// them without importing config (which would create a cycle: config already
// imports db). The values are fixed at startup — the listening socket cannot be
// rebound at runtime — so the UI presents them as diagnostics, and tells the
// operator to restart rather than pretending a change took effect.
func ListenHost() string {
	if h := strings.TrimSpace(os.Getenv("HOST")); h != "" {
		return h
	}
	return "0.0.0.0"
}

func ListenPort() string {
	if p := strings.TrimSpace(os.Getenv("PORT")); p != "" {
		return p
	}
	return "20128"
}
