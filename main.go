package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"yadr-prov/api"
	"yadr-prov/core"
)

// Config использует структуру NodeConfig из пакета core
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
	err = json.Unmarshal(file, &cfg)
	return &cfg, err
}

func main() {
	// 1. Читаем конфиг
	cfg, err := loadConfig("config.json")
	if err != nil {
		log.Fatalf("Ошибка чтения config.json: %v", err)
	}

	// 2. Инициализация Клиентов
	pveClient := core.NewPveClient(cfg.Nodes)
	imqClient := core.NewIntermasqClient(cfg.IntermasqURL, cfg.IntermasqKey)
	
	// Собираем URL Caddy из конфига нод
	caddyURLs := make(map[string]string)
	for name, data := range cfg.Nodes {
		caddyURLs[name] = data.CaddyURL
	}
	caddyClient := core.NewCaddyClient(caddyURLs)

	// 3. Создаем Оркестратор
	statePath := os.Getenv("STATE_FILE")
	if statePath == "" {
		statePath = "/etc/intermasq/plugins/prov/routes.json"
	}
	stateStore := core.NewStateStore(statePath)
	engine := core.NewEngine(pveClient, imqClient, caddyClient, stateStore, cfg.BaseDomain, cfg.Nodes)

	// 4. Создаем API
	apiServer := api.NewApiServer(engine)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "index.html")
	})

	mux.HandleFunc("/api/pending", apiServer.HandleGetPending)
	mux.HandleFunc("/api/provision", apiServer.HandleProvision)
	mux.HandleFunc("/api/deprovision", apiServer.HandleDeprovision)
	mux.HandleFunc("/api/state", apiServer.HandleGetState)
	mux.HandleFunc("/api/replay", apiServer.HandleReplay)

	// 5. Выбор транспорта (Сокет или Порт)
	socketPath := os.Getenv("PLUGIN_SOCKET")
	
	if socketPath != "" {
		os.Remove(socketPath)
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			log.Fatalf("Ошибка создания сокета: %v", err)
		}
		
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-c
			os.Remove(socketPath)
			os.Exit(1)
		}()

		fmt.Printf("Plugin started on unix socket: %s\n", socketPath)
		os.Chmod(socketPath, 0770) 
		http.Serve(listener, mux)
		
	} else {
		fmt.Printf("Plugin started on TCP :5000\n")
		log.Fatal(http.ListenAndServe(":5000", mux))
	}
}
