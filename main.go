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

// Плагин Povez запускается матерью (Intermasq) как subprocess по контракту
// internal/plugins.Load(): мать экспортирует PLUGIN_SOCKET (путь к unix-сокету)
// и INTERMASQ_KEY (API-ключ для обратных вызовов), а плагин отвечает на HTTP
// через этот сокет. Для локальной отладки без матери слушает TCP :5000 и
// читает config.json.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"povez/api"
	"povez/core"
)

//go:embed index.html
var indexHTML []byte

// version вшивается через -ldflags "-X main.version=..." в CI.
var version = "dev"

// Config читается из config.json. Опциональные секции (caddy/dnsmasq/http/
// proxmox/plugin) имеют разумные дефолты в applyDefaults — в config.json их
// можно не указывать. NodeConfig определён в пакете core.
type Config struct {
	IntermasqURL string                     `json:"intermasq_url"`
	IntermasqKey string                     `json:"intermasq_key"`
	BaseDomain   string                     `json:"base_domain"`
	Nodes        map[string]core.NodeConfig `json:"nodes"`

	Caddy   CaddyConfig   `json:"caddy"`
	Dnsmasq DnsmasqConfig `json:"dnsmasq"`
	HTTP    HTTPConfig    `json:"http"`
	Proxmox ProxmoxConfig `json:"proxmox"`
	Plugin  PluginConfig  `json:"plugin"`
}

// CaddyConfig — параметры TLS/reverse-proxy в Caddy.
type CaddyConfig struct {
	ACMEURL          string `json:"acme_url"`          // Step-CA ACME directory URL
	CARoots          string `json:"ca_roots"`          // путь к root CA PEM для Step-CA
	Listen           string `json:"listen"`            // порт, который слушает Caddy (":443")
	UpstreamInsecure *bool  `json:"upstream_insecure"` // insecure_skip_verify для https upstream (default true)
}

// DnsmasqConfig — расположение конфигов dnsmasq, куда плагин пишет host-записи.
type DnsmasqConfig struct {
	ConfDir   string `json:"conf_dir"`   // каталог include-файлов dnsmasq
	CaddyFile string `json:"caddy_file"` // файл хоста Caddy внутри матери
}

// HTTPConfig — общий таймаут HTTP-клиентов (PVE/Intermasq/Caddy).
type HTTPConfig struct {
	TimeoutSeconds int `json:"timeout_seconds"`
}

// ProxmoxConfig — конвенция тегов PVE и формула IP-адресации.
type ProxmoxConfig struct {
	PortPrefix         string `json:"port_prefix"`
	ProtoPrefix        string `json:"proto_prefix"`
	NamePrefix         string `json:"name_prefix"`
	VMIDBase           int    `json:"vmid_base"`            // базовый VMID для расчёта IP-суффикса
	InsecureSkipVerify *bool  `json:"insecure_skip_verify"` // пропускать проверку TLS PVE (default true)
}

// PluginConfig — параметры запуска плагина.
type PluginConfig struct {
	TCPDebugPort      string `json:"tcp_debug_port"`      // TCP-порт локальной отладки (без матери)
	CertSettleSeconds int    `json:"cert_settle_seconds"` // пауза перед рестартом Caddy (выпуск cert)
}

func loadConfig(path string) (*Config, error) {
	file, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(file, &cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	return &cfg, nil
}

// applyDefaults заполняет пустые опциональные поля разумными значениями,
// чтобы config.json мог содержать только обязательные intermasq_url/nodes.
func (c *Config) applyDefaults() {
	if c.Caddy.ACMEURL == "" {
		c.Caddy.ACMEURL = "https://172.20.0.1:9000/acme/acme/directory"
	}
	if c.Caddy.CARoots == "" {
		c.Caddy.CARoots = "/etc/caddy/root_ca.crt"
	}
	if c.Caddy.Listen == "" {
		c.Caddy.Listen = ":443"
	}
	if c.Dnsmasq.ConfDir == "" {
		c.Dnsmasq.ConfDir = "/etc/dnsmasq.d"
	}
	if c.Dnsmasq.CaddyFile == "" {
		c.Dnsmasq.CaddyFile = "/etc/dnsmasq.d/caddy.conf"
	}
	if c.HTTP.TimeoutSeconds == 0 {
		c.HTTP.TimeoutSeconds = 10
	}
	if c.Proxmox.PortPrefix == "" {
		c.Proxmox.PortPrefix = "port-"
	}
	if c.Proxmox.ProtoPrefix == "" {
		c.Proxmox.ProtoPrefix = "proto-"
	}
	if c.Proxmox.NamePrefix == "" {
		c.Proxmox.NamePrefix = "name-"
	}
	if c.Proxmox.VMIDBase == 0 {
		c.Proxmox.VMIDBase = 98
	}
	if c.Plugin.TCPDebugPort == "" {
		c.Plugin.TCPDebugPort = ":5000"
	}
	if c.Plugin.CertSettleSeconds == 0 {
		c.Plugin.CertSettleSeconds = 2
	}
}

// intermasqAPIKey отдаёт API-ключ для обратных вызовов в мать. Приоритет —
// env INTERMASQ_KEY (так плагин запускается в проде), fallback —
// config.intermasq_key (для локальной разработки).
func intermasqAPIKey(cfg *Config) string {
	if k := os.Getenv("INTERMASQ_KEY"); k != "" {
		return k
	}
	return cfg.IntermasqKey
}

func buildClients(cfg *Config) (*core.PveClient, *core.IntermasqClient, *core.CaddyClient) {
	// insecureSkipVerify разрешает nullable bool из config: если p != nil — *p,
	// иначе true. Нужно чтобы отличить явно заданный false от "поле отсутствует"
	// (default true для insecure TLS во внутренней сети).
	insecureSkipVerify := func(p *bool) bool {
		if p != nil {
			return *p
		}
		return true
	}

	timeout := time.Duration(cfg.HTTP.TimeoutSeconds) * time.Second
	settle := time.Duration(cfg.Plugin.CertSettleSeconds) * time.Second

	pve := core.NewPveClient(cfg.Nodes, core.ProxmoxSettings{
		PortPrefix:         cfg.Proxmox.PortPrefix,
		ProtoPrefix:        cfg.Proxmox.ProtoPrefix,
		NamePrefix:         cfg.Proxmox.NamePrefix,
		InsecureSkipVerify: insecureSkipVerify(cfg.Proxmox.InsecureSkipVerify),
		Timeout:            timeout,
	})
	imq := core.NewIntermasqClient(cfg.IntermasqURL, intermasqAPIKey(cfg), timeout)

	caddyURLs := make(map[string]string, len(cfg.Nodes))
	for name, data := range cfg.Nodes {
		caddyURLs[name] = data.CaddyURL
	}
	caddy := core.NewCaddyClient(caddyURLs, core.CaddySettings{
		ACMEURL:          cfg.Caddy.ACMEURL,
		CARoots:          cfg.Caddy.CARoots,
		Listen:           cfg.Caddy.Listen,
		UpstreamInsecure: insecureSkipVerify(cfg.Caddy.UpstreamInsecure),
		Timeout:          timeout,
		CertSettleTime:   settle,
	})
	return pve, imq, caddy
}

// newListener выбирает транспорт: если задан PLUGIN_SOCKET — unix-сокет
// (контракт Intermasq), иначе TCP tcpPort (только локальная отладка).
// Возвращает listener, функцию очистки socket-файла и ошибку.
func newListener(tcpPort string) (net.Listener, func(), error) {
	socketPath := os.Getenv("PLUGIN_SOCKET")
	if socketPath == "" {
		l, err := net.Listen("tcp", tcpPort)
		slog.Info("plugin started on TCP (debug mode)", "addr", tcpPort)
		return l, func() {}, err
	}

	// Stale socket from a previous run would block Listen.
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("remove stale socket failed", "path", socketPath, "err", err)
	}
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, nil, err
	}
	// 0770: владелец и группа. Под CI оба процесса root; в rootless-деплое
	// systemd RuntimeDirectory владеет папкой для сервис-юзера.
	if err := os.Chmod(socketPath, 0770); err != nil {
		slog.Warn("chmod socket failed", "path", socketPath, "err", err)
	}
	cleanup := func() { _ = os.Remove(socketPath) }
	slog.Info("plugin started on unix socket", "path", socketPath)
	return l, cleanup, nil
}

func buildMux(apiServer *api.ApiServer) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"plugin":  "povez",
			"version": version,
		})
	})

	mux.HandleFunc("/api/pending", apiServer.HandleGetPending)
	mux.HandleFunc("/api/provision", apiServer.HandleProvision)
	mux.HandleFunc("/api/deprovision", apiServer.HandleDeprovision)
	mux.HandleFunc("/api/state", apiServer.HandleGetState)
	mux.HandleFunc("/api/replay", apiServer.HandleReplay)
	return mux
}

// run обслуживает сервер до сигнала завершения, после чего корректно
// останавливает его (http.Server.Shutdown) и убирает socket-файл.
func run(server *http.Server, listener net.Listener, cleanup func()) error {
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serveErr:
		cleanup()
		return err
	case <-sigc:
		slog.Info("shutdown signal received")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
		}
		cleanup()
		return nil
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		configPath = "config.json"
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		slog.Error("config load failed", "path", configPath, "err", err)
		os.Exit(1)
	}

	pve, imq, caddy := buildClients(cfg)

	statePath := os.Getenv("STATE_FILE")
	if statePath == "" {
		statePath = "/etc/intermasq/plugins/povez/routes.json"
	}
	state := core.NewStateStore(statePath)
	engine := core.NewEngine(pve, imq, caddy, state, cfg.BaseDomain, cfg.Nodes, core.EngineSettings{
		DnsmasqDir: cfg.Dnsmasq.ConfDir,
		CaddyFile:  cfg.Dnsmasq.CaddyFile,
		VMIDBase:   cfg.Proxmox.VMIDBase,
		CertSettle: time.Duration(cfg.Plugin.CertSettleSeconds) * time.Second,
	})

	listener, cleanup, err := newListener(cfg.Plugin.TCPDebugPort)
	if err != nil {
		slog.Error("listener failed", "err", err)
		os.Exit(1)
	}

	server := &http.Server{
		Handler:           buildMux(api.NewApiServer(engine)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	slog.Info("povez starting", "version", version, "state", statePath, "nodes", len(cfg.Nodes))
	if err := run(server, listener, cleanup); err != nil {
		slog.Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}
