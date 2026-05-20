package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Relay   RelayConfig   `yaml:"relay"`
	Quantum QuantumConfig `yaml:"quantum"`
	Spam    SpamConfig    `yaml:"spam"`
	Peers   []string      `yaml:"peers"`
	Trust   TrustConfig   `yaml:"trust"`
}

type RelayConfig struct {
	Listen      string `yaml:"listen"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type QuantumConfig struct {
	Gamma           float64 `yaml:"gamma"`
	FetchThreshold  float64 `yaml:"fetch_threshold"`
	ConsensusTickMs int     `yaml:"consensus_tick_ms"`
	QuantumTickMs   int     `yaml:"quantum_tick_ms"`
}

func (q QuantumConfig) ConsensusTick() time.Duration {
	return time.Duration(q.ConsensusTickMs) * time.Millisecond
}

func (q QuantumConfig) QuantumTick() time.Duration {
	return time.Duration(q.QuantumTickMs) * time.Millisecond
}

type SpamConfig struct {
	ClientEventsPerSec int `yaml:"client_events_per_sec"`
	PeerAnnouncePerSec int `yaml:"peer_announce_per_sec"`
}

type TrustConfig struct {
	Enabled bool     `yaml:"enabled"`
	Weight  float64  `yaml:"weight"`
	Peers   []string `yaml:"peers"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := defaultConfig()
	if err := parseConfig(string(data), cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func parseConfig(content string, cfg *Config) error {
	section := ""
	inPeers := false
	inTrustPeers := false

	lines := strings.Split(content, "\n")
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if section == "trust" && line == "peers:" {
			inTrustPeers = true
			continue
		}

		if strings.HasSuffix(line, ":") && !strings.Contains(line, " ") {
			section = strings.TrimSuffix(line, ":")
			inPeers = section == "peers"
			inTrustPeers = false
			continue
		}

		if inPeers {
			if strings.HasPrefix(line, "-") {
				item := strings.TrimSpace(strings.TrimPrefix(line, "-"))
				cfg.Peers = append(cfg.Peers, trimQuotes(item))
			}
			continue
		}

		if section == "trust" {
			switch {
			case strings.HasPrefix(line, "peers:"):
				inTrustPeers = true
				continue
			case inTrustPeers && strings.HasPrefix(line, "-"):
				item := strings.TrimSpace(strings.TrimPrefix(line, "-"))
				cfg.Trust.Peers = append(cfg.Trust.Peers, trimQuotes(item))
				continue
			}
		}

		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = trimQuotes(strings.TrimSpace(value))

		switch section {
		case "relay":
			switch key {
			case "listen":
				cfg.Relay.Listen = value
			case "name":
				cfg.Relay.Name = value
			case "description":
				cfg.Relay.Description = value
			}
		case "quantum":
			switch key {
			case "gamma":
				v, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return fmt.Errorf("invalid quantum.gamma: %w", err)
				}
				cfg.Quantum.Gamma = v
			case "fetch_threshold":
				v, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return fmt.Errorf("invalid quantum.fetch_threshold: %w", err)
				}
				cfg.Quantum.FetchThreshold = v
			case "consensus_tick_ms":
				v, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("invalid quantum.consensus_tick_ms: %w", err)
				}
				cfg.Quantum.ConsensusTickMs = v
			case "quantum_tick_ms":
				v, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("invalid quantum.quantum_tick_ms: %w", err)
				}
				cfg.Quantum.QuantumTickMs = v
			}
		case "spam":
			switch key {
			case "client_events_per_sec":
				v, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("invalid spam.client_events_per_sec: %w", err)
				}
				cfg.Spam.ClientEventsPerSec = v
			case "peer_announce_per_sec":
				v, err := strconv.Atoi(value)
				if err != nil {
					return fmt.Errorf("invalid spam.peer_announce_per_sec: %w", err)
				}
				cfg.Spam.PeerAnnouncePerSec = v
			}
		case "trust":
			switch key {
			case "enabled":
				cfg.Trust.Enabled = value == "true"
			case "weight":
				v, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return fmt.Errorf("invalid trust.weight: %w", err)
				}
				cfg.Trust.Weight = v
			}
		}
	}

	return nil
}

func trimQuotes(v string) string {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "\"")
	v = strings.TrimSuffix(v, "\"")
	v = strings.TrimPrefix(v, "'")
	v = strings.TrimSuffix(v, "'")
	return v
}
