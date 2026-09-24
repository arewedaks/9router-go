package shared

import (
	"strings"
	"sync"

	"9router/proxy/internal/tokensaver"
)

// TokenSaverConfig holds runtime-configurable token saver flags and levels.
// Thread-safe via RWMutex. Zero value = all off.
type TokenSaverConfig struct {
	mu                    sync.RWMutex
	rtkEnabled            bool
	cavemanEnabled        bool
	cavemanLevel          string
	ponytailEnabled       bool
	ponytailLevel         string
	injectionGuardEnabled bool

	// Headroom is the optional external compression proxy. Disabled by default:
	// it needs a sidecar the operator has to install, so turning it on without
	// one would only add a failed HTTP call to every request.
	headroomEnabled        bool
	headroomURL            string
	headroomTimeoutMs      int
	headroomCompressUserMs bool
	headroomCodeAware      bool
	headroomKompress       bool
}

// NewTokenSaverConfig creates config with initial values.
func NewTokenSaverConfig(rtk, caveman, ponytail bool) *TokenSaverConfig {
	return &TokenSaverConfig{
		rtkEnabled:            rtk,
		cavemanEnabled:        caveman,
		cavemanLevel:          "full",
		ponytailEnabled:       ponytail,
		ponytailLevel:         "full",
		injectionGuardEnabled: true, // on by default; toggle via settings
		headroomURL:           DefaultHeadroomURL,
		headroomTimeoutMs:     3000,
		headroomKompress:      true,
	}
}

// DefaultHeadroomURL is the local sidecar the reference ships with.
const DefaultHeadroomURL = "http://localhost:8787"

// HeadroomEnabled reports whether prompt compression via the Headroom proxy is on.
func (c *TokenSaverConfig) HeadroomEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.headroomEnabled
}

// HeadroomURL returns the configured proxy base URL, or the default.
func (c *TokenSaverConfig) HeadroomURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.headroomURL == "" {
		return DefaultHeadroomURL
	}
	return c.headroomURL
}

// HeadroomTimeoutMs returns the per-request compression deadline.
func (c *TokenSaverConfig) HeadroomTimeoutMs() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.headroomTimeoutMs <= 0 {
		return 3000
	}
	return c.headroomTimeoutMs
}

// HeadroomCompressUserMessages reports whether user turns are eligible for
// compression. Off by default: rewriting what the operator typed is a much
// bigger surprise than compressing tool output.
func (c *TokenSaverConfig) HeadroomCompressUserMessages() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.headroomCompressUserMs
}

// HeadroomCodeAware reports whether tree-sitter AST compression is active.
func (c *TokenSaverConfig) HeadroomCodeAware() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.headroomCodeAware
}

// HeadroomKompress reports whether the Kompress-v2 model extra is active.
func (c *TokenSaverConfig) HeadroomKompress() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.headroomKompress
}

// SetHeadroom updates the proxy switch and, when given, its URL and timeout.
// Empty/zero optional values leave the stored ones alone so a toggle from the
// dashboard cannot blank the URL a script configured.
func (c *TokenSaverConfig) SetHeadroom(enabled bool, url string, timeoutMs int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headroomEnabled = enabled
	if strings.TrimSpace(url) != "" {
		c.headroomURL = strings.TrimSpace(url)
	}
	if timeoutMs > 0 {
		c.headroomTimeoutMs = timeoutMs
	}
}

// SetHeadroomFlags updates the extras and user-message switches.
func (c *TokenSaverConfig) SetHeadroomFlags(codeAware, kompress, compressUserMessages bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.headroomCodeAware = codeAware
	c.headroomKompress = kompress
	c.headroomCompressUserMs = compressUserMessages
}

func (c *TokenSaverConfig) RTKEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rtkEnabled
}

func (c *TokenSaverConfig) SetRTK(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rtkEnabled = v
}

func (c *TokenSaverConfig) CavemanEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cavemanEnabled
}

func (c *TokenSaverConfig) CavemanLevel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.cavemanLevel == "" {
		return "full"
	}
	return c.cavemanLevel
}

func (c *TokenSaverConfig) SetCaveman(v bool, level ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cavemanEnabled = v
	if len(level) > 0 && level[0] != "" {
		c.cavemanLevel = tokensaver.NormalizeLevel(level[0], tokensaver.CavemanLevels)
	}
}

func (c *TokenSaverConfig) PonytailEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ponytailEnabled
}

func (c *TokenSaverConfig) PonytailLevel() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ponytailLevel == "" {
		return "full"
	}
	return c.ponytailLevel
}

func (c *TokenSaverConfig) SetPonytail(v bool, level ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ponytailEnabled = v
	if len(level) > 0 && level[0] != "" {
		c.ponytailLevel = tokensaver.NormalizeLevel(level[0], tokensaver.PonytailLevels)
	}
}

// InjectionGuardEnabled reports whether the prompt-injection detector is on.
func (c *TokenSaverConfig) InjectionGuardEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.injectionGuardEnabled
}

// SetInjectionGuard toggles the prompt-injection detector.
func (c *TokenSaverConfig) SetInjectionGuard(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.injectionGuardEnabled = v
}

// Snapshot returns all current values.
func (c *TokenSaverConfig) Snapshot() (rtk, caveman, ponytail bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rtkEnabled, c.cavemanEnabled, c.ponytailEnabled
}

// SetAll sets all flags atomically.
func (c *TokenSaverConfig) SetAll(rtk, caveman, ponytail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rtkEnabled = rtk
	c.cavemanEnabled = caveman
	c.ponytailEnabled = ponytail
}
