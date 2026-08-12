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
	"encoding/json"
	"errors"
	"fmt"
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

// version вшивается через -ldflags "-X main.version=..." в CI.
var version = "dev"

// Config читается из config.json. NodeConfig определён в пакете core.
type Config struct {
	IntermasqURL string                     `json:"intermasq_url"`
	IntermasqKey string                     `json:"intermasq_key"`
	BaseDomain   string                     `json:"base_domain"`
	Nodes        map[string]core.NodeConfig `json:"nodes"`
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
	return &cfg, nil
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
	pve := core.NewPveClient(cfg.Nodes)
	imq := core.NewIntermasqClient(cfg.IntermasqURL, intermasqAPIKey(cfg))
	caddyURLs := make(map[string]string, len(cfg.Nodes))
	for name, data := range cfg.Nodes {
		caddyURLs[name] = data.CaddyURL
	}
	return pve, imq, core.NewCaddyClient(caddyURLs)
}

// newListener выбирает транспорт: если задан PLUGIN_SOCKET — unix-сокет
// (контракт Intermasq), иначе TCP :5000 (только локальная отладка).
// Возвращает listener, функцию очистки socket-файла и ошибку.
func newListener() (net.Listener, func(), error) {
	socketPath := os.Getenv("PLUGIN_SOCKET")
	if socketPath == "" {
		l, err := net.Listen("tcp", ":5000")
		slog.Info("plugin started on TCP :5000 (debug mode)")
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
		http.ServeFile(w, r, "index.html")
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","plugin":"povez","version":%q}`, version)
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

	cfg, err := loadConfig("config.json")
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	pve, imq, caddy := buildClients(cfg)

	statePath := os.Getenv("STATE_FILE")
	if statePath == "" {
		statePath = "/etc/intermasq/plugins/povez/routes.json"
	}
	state := core.NewStateStore(statePath)
	engine := core.NewEngine(pve, imq, caddy, state, cfg.BaseDomain, cfg.Nodes)

	listener, cleanup, err := newListener()
	if err != nil {
		slog.Error("listener failed", "err", err)
		os.Exit(1)
	}

	server := &http.Server{Handler: buildMux(api.NewApiServer(engine))}

	slog.Info("povez starting", "version", version, "state", statePath, "nodes", len(cfg.Nodes))
	if err := run(server, listener, cleanup); err != nil {
		slog.Error("server stopped with error", "err", err)
		os.Exit(1)
	}
}
