package main

import (
	"os"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	content := `
relay:
  listen: ":9999"
  name: "Test"
peer:
  listen: ":9001"
  public_port: 9443
quantum:
  gamma: 0.3
  fetch_threshold: 0.1
  consensus_tick_ms: 200
  quantum_tick_ms: 400
spam:
  client_events_per_sec: 5
  peer_announce_per_sec: 50
storage:
  path: "relay.db"
peers:
  - "ws://a.example.com"
`

	f, err := os.CreateTemp("", "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())

	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(f.Name())
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Relay.Listen != ":9999" {
		t.Errorf("Listen = %q, want :9999", cfg.Relay.Listen)
	}
	if cfg.Peer.Listen != ":9001" {
		t.Errorf("Peer.Listen = %q, want :9001", cfg.Peer.Listen)
	}
	if cfg.Peer.PublicPort != 9443 {
		t.Errorf("Peer.PublicPort = %d, want 9443", cfg.Peer.PublicPort)
	}
	if cfg.Quantum.Gamma != 0.3 {
		t.Errorf("Gamma = %f, want 0.3", cfg.Quantum.Gamma)
	}
	if len(cfg.Peers) != 1 {
		t.Errorf("Peers len = %d, want 1", len(cfg.Peers))
	}
	if cfg.Storage.Path != "relay.db" {
		t.Errorf("Storage.Path = %q, want relay.db", cfg.Storage.Path)
	}
}

func TestParseTrustConfig(t *testing.T) {
	input := `
relay:
  address: ":8080"
trust:
  enabled: true
  weight: 3.0
  peers:
    - "wss://trusted1.example.com"
    - "wss://trusted2.example.com"
`

	cfg := defaultConfig()
	if err := parseConfig(input, cfg); err != nil {
		t.Fatalf("parseConfig error: %v", err)
	}
	if !cfg.Trust.Enabled {
		t.Error("expected Trust.Enabled == true")
	}
	if cfg.Trust.Weight != 3.0 {
		t.Errorf("expected Trust.Weight == 3.0, got %v", cfg.Trust.Weight)
	}
	if len(cfg.Trust.Peers) != 2 {
		t.Fatalf("expected 2 trust peers, got %d", len(cfg.Trust.Peers))
	}
	if cfg.Trust.Peers[0] != "wss://trusted1.example.com" {
		t.Errorf("unexpected peer[0]: %s", cfg.Trust.Peers[0])
	}
	if cfg.Trust.Peers[1] != "wss://trusted2.example.com" {
		t.Errorf("unexpected peer[1]: %s", cfg.Trust.Peers[1])
	}
}

func TestConfigAuthRequired(t *testing.T) {
	input := `
auth:
  required: true
`

	cfg := defaultConfig()
	if err := parseConfig(input, cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Auth.Required {
		t.Fatal("expected auth.required = true")
	}
}

func TestConfigAuthRequiredDefault(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Auth.Required {
		t.Fatal("expected auth.required to default to false")
	}
}

func TestConfigMaxConcurrentFetches(t *testing.T) {
	input := `
quantum:
  gamma: 0.5
  fetch_threshold: 0.05
  consensus_tick_ms: 500
  quantum_tick_ms: 1000
  max_concurrent_fetches: 16
`

	cfg := defaultConfig()
	if err := parseConfig(input, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Quantum.MaxConcurrentFetches != 16 {
		t.Fatalf("expected 16, got %d", cfg.Quantum.MaxConcurrentFetches)
	}
}

func TestConfigMaxConcurrentFetchesDefault(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Quantum.MaxConcurrentFetches != 32 {
		t.Fatalf("expected default 32, got %d", cfg.Quantum.MaxConcurrentFetches)
	}
}

func TestConfigPeerDefaults(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Peer.Listen != ":8081" {
		t.Fatalf("expected default peer listen :8081, got %q", cfg.Peer.Listen)
	}
	if cfg.Peer.PublicPort != 8443 {
		t.Fatalf("expected default peer public port 8443, got %d", cfg.Peer.PublicPort)
	}
}
