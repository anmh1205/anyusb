// Package platform describes the native USB/IP backends used by the agent.
package platform

import (
	"context"
	"errors"
)

var (
	ErrInvalid     = errors.New("invalid request")
	ErrNotFound    = errors.New("device not found")
	ErrConflict    = errors.New("USB device is busy or its state changed")
	ErrUnavailable = errors.New("USB/IP backend unavailable")
)

type Device struct {
	ID           string `json:"id"`
	BusID        string `json:"bus_id"`
	PortPath     string `json:"port_path"`
	VendorID     string `json:"vendor_id"`
	ProductID    string `json:"product_id"`
	Serial       string `json:"serial,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Product      string `json:"product,omitempty"`
	Incarnation  string `json:"incarnation"`
	Exported     bool   `json:"exported"`
}

type RemoteDevice struct {
	BusID     string `json:"bus_id"`
	VendorID  string `json:"vendor_id,omitempty"`
	ProductID string `json:"product_id,omitempty"`
	Name      string `json:"name"`
}

type Import struct {
	Port        int      `json:"port"`
	State       string   `json:"state"`
	LocalBusID  string   `json:"local_bus_id,omitempty"`
	RemoteHost  string   `json:"remote_host,omitempty"`
	RemotePort  string   `json:"remote_port,omitempty"`
	RemoteBusID string   `json:"remote_bus_id,omitempty"`
	Name        string   `json:"name,omitempty"`
	SerialPorts []string `json:"serial_ports,omitempty"`
}

type HotplugEvent struct {
	Type   string `json:"type"`
	Device Device `json:"device"`
}

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type Adapter interface {
	ListLocalDevices(context.Context) ([]Device, error)
	Export(context.Context, string) (Device, error)
	Unexport(context.Context, string) (Device, error)
	ListRemote(context.Context, string) ([]RemoteDevice, error)
	ListImports(context.Context) ([]Import, error)
	Attach(context.Context, string, string) (Import, error)
	Detach(context.Context, int) error
	WatchHotplug(context.Context) (<-chan HotplugEvent, error)
	Doctor(context.Context) []Check
}
