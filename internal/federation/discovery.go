package federation

import (
	"log/slog"
	"time"
)

type Discovery struct {
	Client   *Client
	Identity *Identity
	URL      string
	Name     string
	Endpoint string
	Logger   *slog.Logger
	Interval time.Duration
}

func NewDiscovery(client *Client, ident *Identity, url, name, endpoint string, logger *slog.Logger) *Discovery {
	return &Discovery{
		Client: client, Identity: ident, URL: url,
		Name: name, Endpoint: endpoint, Logger: logger,
		Interval: 5 * time.Minute,
	}
}

func (d *Discovery) Run() {
	if d.URL == "" {
		d.Logger.Info("discovery server not configured, skipping")
		return
	}

	d.Logger.Info("federation discovery started", "url", d.URL, "name", d.Name)
	d.Register()
	t := time.NewTicker(d.Interval)
	defer t.Stop()
	for range t.C {
		d.Register()
	}
}

func (d *Discovery) Register() {
	payload := map[string]string{
		"instance_name": d.Name,
		"endpoint_url":  d.Endpoint,
		"public_key":    d.Identity.PublicKey,
	}
	resp, err := d.Client.SignedPost(d.Identity, d.URL+"/fed/register", payload)
	if err != nil {
		d.Logger.Warn("discovery: register failed", "error", err, "url", d.URL)
		return
	}
	d.Logger.Info("discovery: registered", "response", string(resp[:min(len(resp), 200)]))
}
