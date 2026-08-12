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

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"povez/core"
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
	_ = json.NewEncoder(w).Encode(payload)
}

func jsonError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

// allowMethod проверяет HTTP-метод запроса. Если не разрешён — пишет 405 с
// заголовком Allow и возвращает false.
func allowMethod(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, m := range methods {
		if r.Method == m {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	jsonError(w, http.StatusMethodNotAllowed, "method "+r.Method+" not allowed")
	return false
}

// statusForError отображает типизированную ошибку engine в HTTP-статус.
// Незнакомые ошибки → 500.
func statusForError(err error) int {
	switch {
	case errors.Is(err, core.ErrContainerNotFound):
		return http.StatusNotFound
	case errors.Is(err, core.ErrContainerRunning):
		return http.StatusConflict
	case errors.Is(err, core.ErrInvalidIP):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

type provisionRequest struct {
	MAC     string `json:"mac"`
	DnsOnly bool   `json:"dnsOnly"`
}

type deprovisionRequest struct {
	MAC string `json:"mac"`
}

func (s *ApiServer) HandleGetPending(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	devices, err := s.Engine.GetPendingDevices()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, devices)
}

func (s *ApiServer) HandleProvision(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) {
		return
	}
	var req provisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if req.MAC == "" {
		jsonError(w, http.StatusBadRequest, "MAC address is required")
		return
	}
	resultMsg, err := s.Engine.Provision(req.MAC, req.DnsOnly)
	if err != nil {
		jsonError(w, statusForError(err), err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"message": resultMsg})
}

func (s *ApiServer) HandleDeprovision(w http.ResponseWriter, r *http.Request) {
	// UI шлёт axios.delete с телом — поддерживаем DELETE (канонический) и
	// POST (обратная совместимость со старыми клиентами).
	if !allowMethod(w, r, http.MethodDelete, http.MethodPost) {
		return
	}
	var req deprovisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if req.MAC == "" {
		jsonError(w, http.StatusBadRequest, "MAC address is required")
		return
	}
	if err := s.Engine.Deprovision(req.MAC); err != nil {
		jsonError(w, statusForError(err), err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]string{"message": "Успешно удалено из Dnsmasq и Caddy"})
}

func (s *ApiServer) HandleGetState(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	records, err := s.Engine.GetState()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, records)
}

func (s *ApiServer) HandleReplay(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) {
		return
	}
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
