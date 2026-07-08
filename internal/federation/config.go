package federation

import (
	"os"

	"hirebridge/internal/config"
)

type Config struct {
	Enabled       bool
	Port          string
	EndpointURL   string
	InstanceName  string
	KeyPath       string
	DiscoveryURL  string
	SyncInterval  string
	ExportFilter  string
	ImportMaxPeer int
}

func LoadConfig(cfg *config.Config) *Config {
	return &Config{
		Enabled:      os.Getenv("HB_FEDERATION_ENABLED") == "true",
		Port:         config.Env("HB_FEDERATION_PORT", ":8400"),
		EndpointURL:  config.Env("HB_FEDERATION_ENDPOINT", ""),
		InstanceName: config.Env("HB_FEDERATION_INSTANCE_NAME", ""),
		KeyPath:      config.Env("HB_FEDERATION_KEY_PATH", "federation_key.json"),
		DiscoveryURL: config.Env("HB_FEDERATION_DISCOVERY_URL", ""),
		SyncInterval: config.Env("HB_FEDERATION_SYNC_INTERVAL", "5m"),
		ExportFilter: config.Env("HB_FEDERATION_EXPORT_FILTER", ""),
		ImportMaxPeer: config.EnvInt("HB_FEDERATION_IMPORT_MAX", 10000),
	}
}
