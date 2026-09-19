package config

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"9router/proxy/internal/log"
		"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"9router/proxy/internal/constants"
)

// loadDotenv reads key=value pairs from .env file and sets them as env vars.
// Supports single/double-quoted values, strips inline `#` comments (except
// inside quotes), and never overrides an existing environment variable.
func loadDotenv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else if idx := strings.IndexByte(v, '#'); idx >= 0 {
			v = strings.TrimSpace(v[:idx])
		}
		// Existing env vars take precedence
		if os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

// Config holds the proxy gateway configuration.
type Config struct {
	Port            int
	Host            string
	DatabasePath    string
	JWTSecret       string
	InitialPassword string
	APIKeySecret    string
	MachineIDSalt   string
	RTKEnabled      bool
	CavemanEnabled  bool
	PonytailEnabled bool
}

// defaultDatabasePath returns the canonical database location for a data dir.
// This is the path the Node.js dashboard uses, so an existing installation is
// picked up without any flag or environment variable.
func defaultDatabasePath(dataDir string) string {
	return filepath.Join(dataDir, "db", "data.sqlite")
}

// findDatabaseInDir looks for a database file inside dir, accepting the layouts
// that different 9Router generations have used. It prefers DATA_DIR/db/data.sqlite
// (the current layout) and reports whether anything was found.
func findDatabaseInDir(dir string) (string, bool) {
	candidates := []string{
		filepath.Join(dir, "db", "data.sqlite"), // current layout
		filepath.Join(dir, "data.sqlite"),       // flat layout
		filepath.Join(dir, "9router.db"),        // legacy name
	}
	for _, candidate := range candidates {
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

// ResolveDataDir returns the base data directory: DATA_DIR env, else the
// platform default (~/.9router, or %APPDATA%/9router on Windows).
func ResolveDataDir() string {
	if dataDir := os.Getenv("DATA_DIR"); dataDir != "" {
		return dataDir
	}
	if homeDir, err := os.UserHomeDir(); err == nil {
		if runtime.GOOS == "windows" {
			appData := os.Getenv("APPDATA")
			if appData == "" {
				appData = filepath.Join(homeDir, "AppData", "Roaming")
			}
			return filepath.Join(appData, "9router")
		}
		return filepath.Join(homeDir, ".9router")
	}
	return ".9router"
}

// LoadConfig loads the configuration from environment variables and platform defaults.
func LoadConfig() *Config {
	loadDotenv(".env")
	portStr := os.Getenv("PORT")
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		port = 20128 // Default port
	}

	dataDir := ResolveDataDir()

	// Ensure the base data directory exists
	if err := os.MkdirAll(dataDir, constants.FilePermDir); err != nil {
		log.Warn("config", "create data dir failed", "dir", dataDir, "error", err)
	}

	// Database file: DB_PATH overrides default DATA_DIR/db/data.sqlite
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = defaultDatabasePath(dataDir)
	} else if fi, err := os.Stat(dbPath); err == nil && fi.IsDir() {
		// DB_PATH pointed at a directory — find the database inside it.
		if found, ok := findDatabaseInDir(dbPath); ok {
			dbPath = found
		} else {
			// Nothing to load. Creating a fresh database here would start the
			// proxy with zero providers and no explanation, which looks exactly
			// like "my database disappeared". Name the path we searched so the
			// operator can see which directory was wrong.
			log.Warn("config", "no database found in DB_PATH directory; a new empty database will be created",
				"dir", dbPath, "expected", filepath.Join(dbPath, "data.sqlite"))
			dbPath = filepath.Join(dbPath, "data.sqlite")
		}
	}

	// A missing database file is not an error at startup — the proxy creates
	// the schema on first use — but it is almost never what the operator meant.
	// Warn loudly so "the dashboard is empty" is traceable to a wrong path
	// rather than mistaken for data loss.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		log.Warn("config", "database not found; a new empty database will be created",
			"path", dbPath,
			"hint", "set --db-path or DB_PATH to an existing database file")
	}

	// INITIAL_PASSWORD has no hardcoded default — an empty value forces the
	// operator to set one explicitly rather than shipping a known password.
	initialPassword := os.Getenv("INITIAL_PASSWORD")

	apiKeySecret := os.Getenv("API_KEY_SECRET")
	if apiKeySecret == "" {
		apiKeySecret = "endpoint-proxy-api-key-secret"
	}

	machineIDSalt := os.Getenv("MACHINE_ID_SALT")
	if machineIDSalt == "" {
		machineIDSalt = "endpoint-proxy-salt"
	}

	rtkEnabled := os.Getenv("RTK_ENABLED") != "false" // default on
	cavemanEnabled := os.Getenv("CAVEMAN_ENABLED") == "true"
	ponytailEnabled := os.Getenv("PONYTAIL_ENABLED") == "true"

	// HOST bounds the listen address. Default is all interfaces, which is what a
	// bare-metal install wants. Behind Cloudflare Tunnel or any reverse proxy the
	// origin should bind 127.0.0.1 so the untrusted public internet cannot reach
	// the port directly and forge X-Forwarded-For to escape the login limiter.
	host := strings.TrimSpace(os.Getenv("HOST"))

	return &Config{
		Port:            port,
		Host:            host,
		DatabasePath:    dbPath,
		JWTSecret:       loadJWTSecret(dataDir),
		InitialPassword: initialPassword,
		APIKeySecret:    apiKeySecret,
		MachineIDSalt:   machineIDSalt,
		RTKEnabled:      rtkEnabled,
		CavemanEnabled:  cavemanEnabled,
		PonytailEnabled: ponytailEnabled,
	}
}

func loadJWTSecret(dataDir string) string {
	secret := os.Getenv("JWT_SECRET")
	if secret != "" {
		return secret
	}

	secretFile := filepath.Join(dataDir, "jwt-secret")
	data, err := os.ReadFile(secretFile)
	if err == nil {
		return string(data)
	}

	// Generate 32 cryptographically secure random bytes
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		log.Error("config", "crypto/rand failed to generate JWT secret; refusing to fall back to a static secret", "error", err)
		return ""
	}

	generated := hex.EncodeToString(bytes)
	if err := os.WriteFile(secretFile, []byte(generated), constants.FilePermKey); err != nil {
		log.Warn("config", "write JWT secret failed", "file", secretFile, "error", err)
	}
	return generated
}
