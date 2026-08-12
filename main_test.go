// Povez - Intermasq provisioning plugin
// Copyright (C) 2026 AlexRus1234
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfig_ApplyDefaults_FillsAll(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	cases := []struct {
		name, got, want string
	}{
		{"caddy.acme_url", cfg.Caddy.ACMEURL, "https://172.20.0.1:9000/acme/acme/directory"},
		{"caddy.ca_roots", cfg.Caddy.CARoots, "/etc/caddy/root_ca.crt"},
		{"caddy.listen", cfg.Caddy.Listen, ":443"},
		{"dnsmasq.conf_dir", cfg.Dnsmasq.ConfDir, "/etc/dnsmasq.d"},
		{"dnsmasq.caddy_file", cfg.Dnsmasq.CaddyFile, "/etc/dnsmasq.d/caddy.conf"},
		{"proxmox.port_prefix", cfg.Proxmox.PortPrefix, "port-"},
		{"proxmox.proto_prefix", cfg.Proxmox.ProtoPrefix, "proto-"},
		{"proxmox.name_prefix", cfg.Proxmox.NamePrefix, "name-"},
		{"plugin.tcp_debug_port", cfg.Plugin.TCPDebugPort, ":5000"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
	if cfg.HTTP.TimeoutSeconds != 10 {
		t.Errorf("http.timeout_seconds = %d, want 10", cfg.HTTP.TimeoutSeconds)
	}
	if cfg.Proxmox.VMIDBase != 98 {
		t.Errorf("proxmox.vmid_base = %d, want 98", cfg.Proxmox.VMIDBase)
	}
	if cfg.Plugin.CertSettleSeconds != 2 {
		t.Errorf("plugin.cert_settle_seconds = %d, want 2", cfg.Plugin.CertSettleSeconds)
	}
}

func TestConfig_ApplyDefaults_PreservesCustomValues(t *testing.T) {
	cfg := &Config{
		Caddy:   CaddyConfig{Listen: ":8443", ACMEURL: "https://custom/dir"},
		Proxmox: ProxmoxConfig{VMIDBase: 200, PortPrefix: "p-"},
		Plugin:  PluginConfig{TCPDebugPort: ":6000"},
	}
	cfg.applyDefaults()

	// Custom values must NOT be overwritten.
	if cfg.Caddy.Listen != ":8443" {
		t.Errorf("listen overwritten: %q", cfg.Caddy.Listen)
	}
	if cfg.Caddy.ACMEURL != "https://custom/dir" {
		t.Errorf("acme_url overwritten: %q", cfg.Caddy.ACMEURL)
	}
	if cfg.Proxmox.VMIDBase != 200 {
		t.Errorf("vmid_base overwritten: %d", cfg.Proxmox.VMIDBase)
	}
	if cfg.Proxmox.PortPrefix != "p-" {
		t.Errorf("port_prefix overwritten: %q", cfg.Proxmox.PortPrefix)
	}
	if cfg.Plugin.TCPDebugPort != ":6000" {
		t.Errorf("tcp_debug_port overwritten: %q", cfg.Plugin.TCPDebugPort)
	}
	// Untouched fields still get their defaults.
	if cfg.Caddy.CARoots != "/etc/caddy/root_ca.crt" {
		t.Errorf("ca_roots default not applied: %q", cfg.Caddy.CARoots)
	}
	if cfg.Proxmox.ProtoPrefix != "proto-" {
		t.Errorf("proto_prefix default not applied: %q", cfg.Proxmox.ProtoPrefix)
	}
}

func TestLoadConfig_ParsesAndAppliesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{
		"intermasq_url": "http://test:8080/api",
		"base_domain": ".test",
		"nodes": {"n1": {"subnet": "10.0.0"}},
		"caddy": {"listen": ":9999"}
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	// Explicit value read from file.
	if cfg.Caddy.Listen != ":9999" {
		t.Errorf("listen = %q, want :9999", cfg.Caddy.Listen)
	}
	if cfg.BaseDomain != ".test" {
		t.Errorf("base_domain = %q", cfg.BaseDomain)
	}
	if len(cfg.Nodes) != 1 {
		t.Errorf("expected 1 node, got %d", len(cfg.Nodes))
	}
	// Missing field → default applied.
	if cfg.Caddy.ACMEURL == "" {
		t.Errorf("ACMEURL default not applied")
	}
	if cfg.HTTP.TimeoutSeconds != 10 {
		t.Errorf("timeout default not applied: %d", cfg.HTTP.TimeoutSeconds)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	if _, err := loadConfig(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Errorf("expected error for missing config file, got nil")
	}
}

func TestLoadConfig_InvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Errorf("expected error for invalid JSON, got nil")
	}
}

// TestIntermasqAPIKey_Priority guards the env > config fallback order.
func TestIntermasqAPIKey_Priority(t *testing.T) {
	cfg := &Config{IntermasqKey: "from-config"}

	// env wins when set.
	t.Setenv("INTERMASQ_KEY", "from-env")
	if got := intermasqAPIKey(cfg); got != "from-env" {
		t.Errorf("env must win, got %q", got)
	}

	// fallback to config when env is empty.
	t.Setenv("INTERMASQ_KEY", "")
	if got := intermasqAPIKey(cfg); got != "from-config" {
		t.Errorf("config fallback failed, got %q", got)
	}
}
