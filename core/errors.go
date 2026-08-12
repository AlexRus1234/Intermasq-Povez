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

package core

import "errors"

// Сентинельные ошибки engine, которые api-слой отображает в HTTP-статусы.
// Оборачиваются через fmt.Errorf("%w ...", ErrXxx) и ловятся errors.Is.
var (
	// ErrContainerNotFound — MAC не найден в Proxmox (Provision/Deprovision).
	ErrContainerNotFound = errors.New("container not found")
	// ErrContainerRunning — попытка Deprovision работающего контейнера.
	ErrContainerRunning = errors.New("container is running")
	// ErrInvalidIP — расчётный последний октет IP вне диапазона [0,255].
	ErrInvalidIP = errors.New("invalid computed IP")
)
