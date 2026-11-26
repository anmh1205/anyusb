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
	ID              string        `json:"id"`
	BusID           string        `json:"bus_id"`
	PortPath        string        `json:"port_path"`
	VendorID        string        `json:"vendor_id"`
	ProductID       string        `json:"product_id"`
	Serial          string        `json:"serial,omitempty"`
	Manufacturer    string        `json:"manufacturer,omitempty"`
	Product         string        `json:"product,omitempty"`
	Incarnation     string        `json:"incarnation"`
	USBVersion      string        `json:"usb_version,omitempty"`
	Speed           string        `json:"speed,omitempty"`
	DeviceClass     string        `json:"device_class,omitempty"`
	DeviceSubClass  string        `json:"device_subclass,omitempty"`
	DeviceProtocol  string        `json:"device_protocol,omitempty"`
	Functions       []USBFunction `json:"functions,omitempty"`
	Profiles        []string      `json:"profiles,omitempty"`
	Risk            string        `json:"risk,omitempty"`
	SharePolicy     string        `json:"share_policy,omitempty"`
	ReconnectPolicy string        `json:"reconnect_policy,omitempty"`
	Exported        bool          `json:"exported"`
}

type USBFunction struct {
	ID            string `json:"id"`
	Configuration int    `json:"configuration,omitempty"`
	Interface     int    `json:"interface,omitempty"`
	Alternate     int    `json:"alternate,omitempty"`
	CDCControl    *int   `json:"cdc_control_interface,omitempty"`
	Class         string `json:"class,omitempty"`
	SubClass      string `json:"subclass,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Driver        string `json:"driver,omitempty"`
	Description   string `json:"description,omitempty"`
}

type LocalBinding struct {
	Function string `json:"function,omitempty"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Driver   string `json:"driver,omitempty"`
	Status   string `json:"status,omitempty"`
	Problem  string `json:"problem,omitempty"`
}

type RemoteDevice struct {
	BusID     string `json:"bus_id"`
	VendorID  string `json:"vendor_id,omitempty"`
	ProductID string `json:"product_id,omitempty"`
	Name      string `json:"name"`
}

type Import struct {
	Port        int            `json:"port"`
	State       string         `json:"state"`
	Readiness   string         `json:"readiness,omitempty"`
	LocalBusID  string         `json:"local_bus_id,omitempty"`
	RemoteHost  string         `json:"remote_host,omitempty"`
	RemotePort  string         `json:"remote_port,omitempty"`
	RemoteBusID string         `json:"remote_bus_id,omitempty"`
	Name        string         `json:"name,omitempty"`
	Functions   []USBFunction  `json:"functions,omitempty"`
	Bindings    []LocalBinding `json:"local_bindings,omitempty"`
	SerialPorts []string       `json:"serial_ports,omitempty"`
}

type HotplugEvent struct {
	Type   string `json:"type"`
	Device Device `json:"device"`
}

type Check struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Optional bool   `json:"optional,omitempty"`
	Detail   string `json:"detail"`
}

type Adapter interface {
	ListLocalDevices(context.Context) ([]Device, error)
	LocalSerialPorts(context.Context, string) ([]string, error)
	Export(context.Context, string) (Device, error)
	Unexport(context.Context, string) (Device, error)
	ListRemote(context.Context, string) ([]RemoteDevice, error)
	ListImports(context.Context) ([]Import, error)
	Attach(context.Context, string, string) (Import, error)
	Detach(context.Context, int) error
	WatchHotplug(context.Context) (<-chan HotplugEvent, error)
	Doctor(context.Context) []Check
}
