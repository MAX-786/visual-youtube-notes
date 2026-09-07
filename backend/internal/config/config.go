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
	// Worker holds the settings only the worker process reads.
	Worker Worker
}

// Worker is the worker process's configuration (ADR 0006, ADR 0008).
type Worker struct {
	// TempDir is the root of per-Attempt temporary directories.
	TempDir string
	// QueueDefaultWorkers and QueueMediaWorkers are River's per-queue
	// concurrency; media stays at one so FFmpeg and speech to text serialize.
	QueueDefaultWorkers int
	QueueMediaWorkers   int
	// HealthListen is the internal health endpoint's bind address.
	HealthListen string
	// YtDlpBin is the yt-dlp binary, on PATH by default.
	YtDlpBin string
	// FFmpegBin and FFprobeBin split and measure audio for speech to text.
	FFmpegBin  string
	FFprobeBin string
	// YtDlpCacheDir persists Deno's solved player challenges across runs.
	YtDlpCacheDir string
	// TempMinFreeBytes is the preflight floor for the transcribing Stage.
	TempMinFreeBytes int64
	// BackupStatusURL is the backup sidecar's /status; the two staleness
	// thresholds and the health cadence follow ADR 0009.
	BackupStatusURL     string
	BackupStaleLocal    time.Duration
	BackupStaleOffsite  time.Duration
	HealthCheckInterval time.Duration
	// The disk alarm fires below max(DiskMinFreeBytes, DiskMinFreePercent)
	// free, chosen to precede the transcribing preflight's own floor.
	DiskMinFreeBytes   int64
	DiskMinFreePercent int
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

	cfg.Worker = Worker{
		TempDir:       get("TEMP_DIR", "/var/lib/vyn/tmp"),
		HealthListen:  get("HEALTH_LISTEN", ":8081"),
		YtDlpBin:      get("YTDLP_BIN", "yt-dlp"),
		FFmpegBin:     get("FFMPEG_BIN", "ffmpeg"),
		FFprobeBin:    get("FFPROBE_BIN", "ffprobe"),
		YtDlpCacheDir: get("YTDLP_CACHE_DIR", "/var/cache/yt-dlp"),
	}
	cfg.Worker.QueueDefaultWorkers = intSetting(get("QUEUE_DEFAULT_WORKERS", "2"), &problems, "VYN_QUEUE_DEFAULT_WORKERS")
	cfg.Worker.QueueMediaWorkers = intSetting(get("QUEUE_MEDIA_WORKERS", "1"), &problems, "VYN_QUEUE_MEDIA_WORKERS")
	cfg.Worker.TempMinFreeBytes = int64(intSetting(get("TEMP_MIN_FREE_BYTES", "2147483648"), &problems, "VYN_TEMP_MIN_FREE_BYTES"))
	cfg.Worker.BackupStatusURL = get("BACKUP_STATUS_URL", "http://backup:8080/status")
	cfg.Worker.BackupStaleLocal = durationSetting(get("BACKUP_STALE_LOCAL", "48h"), &problems, "VYN_BACKUP_STALE_LOCAL")
	cfg.Worker.BackupStaleOffsite = durationSetting(get("BACKUP_STALE_OFFSITE", "240h"), &problems, "VYN_BACKUP_STALE_OFFSITE")
	cfg.Worker.HealthCheckInterval = durationSetting(get("HEALTH_CHECK_INTERVAL", "15m"), &problems, "VYN_HEALTH_CHECK_INTERVAL")
	cfg.Worker.DiskMinFreeBytes = int64(intSetting(get("DISK_MIN_FREE_BYTES", "5368709120"), &problems, "VYN_DISK_MIN_FREE_BYTES"))
	cfg.Worker.DiskMinFreePercent = intSetting(get("DISK_MIN_FREE_PERCENT", "10"), &problems, "VYN_DISK_MIN_FREE_PERCENT")
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

func durationSetting(raw string, problems *[]string, name string) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		*problems = append(*problems, name+" must be a positive duration")
	}
	return d
}

func intSetting(raw string, problems *[]string, name string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		*problems = append(*problems, name+" must be a non-negative integer")
		return 0
	}
	return n
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
