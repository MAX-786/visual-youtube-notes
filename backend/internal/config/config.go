// Package config loads the API and worker configuration. Non-secret values
// come from VYN_* environment variables; every secret is a file, because the
// worker spawns yt-dlp, Deno and FFmpeg constantly and environment leaks into
// every child and into docker inspect (ADR 0008).
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is everything a process needs at boot.
type Config struct {
	// Listen is the API's bind address, such as ":8080".
	Listen string
	// PublicOrigin is the PWA's origin, the only origin cookie-authenticated
	// mutations may come from.
	PublicOrigin string
	// ExtensionID is the allowlisted Chromium extension id (ADR 0013).
	ExtensionID string
	// Database is the PostgreSQL connection.
	Database Database
	// MasterKeyFile holds the versioned application master key (ADR 0010).
	MasterKeyFile string
	// VapidPrivateKeyFile holds the Web Push private key (ADR 0014).
	VapidPrivateKeyFile string
	// SSEHeartbeat is the comment heartbeat interval on the event stream.
	SSEHeartbeat time.Duration
	// GotenbergURL is the PDF renderer (ADR 0011).
	GotenbergURL string
	// DevInsecure relaxes the Secure cookie attribute for plain-http local
	// development. Never set in production.
	DevInsecure bool
}

// Database is the PostgreSQL connection, with the password read from a file.
type Database struct {
	Host         string
	Port         int
	Name         string
	User         string
	PasswordFile string
}

// URL renders a pgx connection string. The password is read at call time so
// it never sits in a struct that might be logged.
func (d Database) URL() (string, error) {
	password, err := ReadSecretFile(d.PasswordFile)
	if err != nil {
		return "", err
	}
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(d.User, password),
		Host:     fmt.Sprintf("%s:%d", d.Host, d.Port),
		Path:     "/" + d.Name,
		RawQuery: "sslmode=disable",
	}
	return u.String(), nil
}

// ExtensionOrigin is the CORS origin the extension presents.
func (c *Config) ExtensionOrigin() string {
	return "chrome-extension://" + c.ExtensionID
}

// Load reads the configuration from the environment. Missing required values
// are reported together rather than one per restart.
func Load() (*Config, error) {
	var problems []string
	get := func(name, fallback string) string {
		if v, ok := os.LookupEnv("VYN_" + name); ok && v != "" {
			return v
		}
		return fallback
	}
	require := func(name string) string {
		v := get(name, "")
		if v == "" {
			problems = append(problems, "VYN_"+name+" is required")
		}
		return v
	}

	cfg := &Config{
		Listen:              get("LISTEN", ":8080"),
		PublicOrigin:        get("PUBLIC_ORIGIN", "https://yt-track.mkhismkh.com"),
		ExtensionID:         get("EXTENSION_ID", "fjodmlmkfifedoamachdbgigfpaldicm"),
		MasterKeyFile:       get("MASTER_KEY_FILE", "/run/secrets/master_key"),
		VapidPrivateKeyFile: get("VAPID_PRIVATE_KEY_FILE", "/run/secrets/vapid_private_key"),
		GotenbergURL:        get("GOTENBERG_URL", "http://gotenberg:3000"),
		DevInsecure:         get("DEV_INSECURE", "") == "1",
	}
	cfg.Database = Database{
		Host:         require("DATABASE_HOST"),
		Name:         require("DATABASE_NAME"),
		User:         require("DATABASE_USER"),
		PasswordFile: require("DATABASE_PASSWORD_FILE"),
	}
	port, err := strconv.Atoi(get("DATABASE_PORT", "5432"))
	if err != nil || port <= 0 {
		problems = append(problems, "VYN_DATABASE_PORT must be a port number")
	}
	cfg.Database.Port = port

	heartbeat, err := time.ParseDuration(get("SSE_HEARTBEAT", "30s"))
	if err != nil || heartbeat <= 0 {
		problems = append(problems, "VYN_SSE_HEARTBEAT must be a positive duration")
	}
	cfg.SSEHeartbeat = heartbeat

	if _, err := url.Parse(cfg.PublicOrigin); err != nil || !strings.HasPrefix(cfg.PublicOrigin, "http") {
		problems = append(problems, "VYN_PUBLIC_ORIGIN must be an absolute origin")
	}
	if !isExtensionID(cfg.ExtensionID) {
		problems = append(problems, "VYN_EXTENSION_ID must be 32 characters a-p")
	}

	if len(problems) > 0 {
		return nil, errors.New("configuration: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}

// ReadSecretFile reads a secret file and trims the trailing newline editors
// add. An empty file is an error: a blank password is never intended.
func ReadSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret %s: %w", path, err)
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", fmt.Errorf("secret %s is empty", path)
	}
	return value, nil
}

func isExtensionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if r < 'a' || r > 'p' {
			return false
		}
	}
	return true
}
