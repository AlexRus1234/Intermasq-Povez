package api

import (
	"encoding/json"
	"net/http"
	"yadr-prov/core"
)

type ApiServer struct {
	Engine *core.Engine
}

func NewApiServer(e *core.Engine) *ApiServer {
	return &ApiServer{Engine: e}
}

func jsonResponse(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

func (s *ApiServer) HandleGetPending(w http.ResponseWriter, r *http.Request) {
	devices, err := s.Engine.GetPendingDevices()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, devices)
}

func (s *ApiServer) HandleProvision(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MAC     string `json:"mac"`
		DnsOnly bool   `json:"dnsOnly"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	resultMsg, err := s.Engine.Provision(req.MAC, req.DnsOnly)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"message": resultMsg})
}

// === ВОТ ТУТ БЫЛА ПРОБЛЕМА, МЕНЯЕМ НА УМНОЕ УДАЛЕНИЕ ===
func (s *ApiServer) HandleDeprovision(w http.ResponseWriter, r *http.Request) {
	var req struct { MAC string `json:"mac"` }
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	if req.MAC == "" {
		jsonError(w, http.StatusBadRequest, "MAC address is required")
		return
	}

	// Вызываем умный метод движка (он сам найдет ноду и VMID)
	err := s.Engine.Deprovision(req.MAC)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResponse(w, http.StatusOK, map[string]string{"message": "Успешно удалено из Dnsmasq и Caddy"})
}

func (s *ApiServer) HandleGetState(w http.ResponseWriter, r *http.Request) {
	records, err := s.Engine.GetState()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, records)
}

func (s *ApiServer) HandleReplay(w http.ResponseWriter, r *http.Request) {
	ok, errs, err := s.Engine.ReplayCaddy()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]interface{}{
		"ok":     ok,
		"errors": errs,
	})
}
