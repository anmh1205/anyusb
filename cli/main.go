// Command anyusb operates the local USB/IP agent.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"unicode"

	"github.com/anmh1205/anyusb/agent/core"
	"github.com/anmh1205/anyusb/agent/platform"
)

const usage = `Usage: anyusb [-socket PATH] [-json] COMMAND

  list [--remote HOST | --imports]
  share DEVICE_ID_OR_BUSID
  unshare DEVICE_ID_OR_BUSID
  connect HOST BUSID
  disconnect VHCI_PORT
  watch
  doctor`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runCLI(ctx, os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) || ctx.Err() != nil {
			return
		}
		fmt.Fprintln(os.Stderr, "anyusb:", err)
		os.Exit(1)
	}
}

func runCLI(ctx context.Context, args []string, output io.Writer) error {
	socketDefault := os.Getenv("ANYUSB_SOCKET")
	if socketDefault == "" {
		socketDefault = core.DefaultSocket
	}
	flags := flag.NewFlagSet("anyusb", flag.ContinueOnError)
	flags.SetOutput(output)
	socket := flags.String("socket", socketDefault, "path to the local Unix socket")
	jsonOutput := flags.Bool("json", false, "write JSON output")
	flags.Usage = func() { fmt.Fprintln(output, usage) }
	if err := flags.Parse(args); err != nil {
		return err
	}
	args = flags.Args()
	if len(args) == 0 || args[0] == "help" {
		fmt.Fprintln(output, usage)
		return nil
	}
	command, args := args[0], args[1:]
	c := newClient(*socket)
	result := func(data any, message string, err error) error {
		if err != nil {
			return err
		}
		if *jsonOutput {
			return json.NewEncoder(output).Encode(data)
		}
		_, err = fmt.Fprintln(output, message)
		return err
	}
	switch command {
	case "list":
		return list(ctx, c, args, output, *jsonOutput)
	case "share", "unshare":
		if len(args) != 1 {
			return fmt.Errorf("usage: anyusb %s DEVICE_ID_OR_BUSID", command)
		}
		method, verb := http.MethodPut, "Shared"
		if command == "unshare" {
			method, verb = http.MethodDelete, "Unshared"
		}
		dev, err := call[platform.Device](ctx, c, method, "/v1/devices/"+url.PathEscape(args[0])+"/export", nil)
		return result(dev, fmt.Sprintf("%s %s (%s)", verb, dev.BusID, dev.ID), err)
	case "connect":
		if len(args) != 2 {
			return fmt.Errorf("usage: anyusb connect HOST BUSID")
		}
		request := core.ConnectRequest{Host: args[0], BusID: args[1]}
		imported, err := call[platform.Import](ctx, c, http.MethodPost, "/v1/imports", request)
		label := fmt.Sprintf("Connected %s/%s on virtual port %d", imported.RemoteHost, imported.RemoteBusID, imported.Port)
		if len(imported.SerialPorts) > 0 {
			label += " (" + strings.Join(imported.SerialPorts, ", ") + ")"
		}
		return result(imported, label, err)
	case "disconnect":
		if len(args) != 1 {
			return fmt.Errorf("usage: anyusb disconnect VHCI_PORT")
		}
		port, err := strconv.Atoi(args[0])
		if err != nil || port < 0 {
			return fmt.Errorf("VHCI_PORT must be a nonnegative integer")
		}
		removed, err := call[struct{}](ctx, c, http.MethodDelete, "/v1/imports/"+strconv.Itoa(port), nil)
		return result(removed, fmt.Sprintf("Disconnected virtual port %d", port), err)
	case "doctor":
		if len(args) != 0 {
			return fmt.Errorf("usage: anyusb doctor")
		}
		checks, err := call[[]platform.Check](ctx, c, http.MethodGet, "/v1/doctor", nil)
		if err != nil || *jsonOutput {
			return result(checks, "", err)
		}
		return printTable(output, []string{"CHECK", "STATUS", "DETAIL"}, checks, func(check platform.Check) []string {
			status := "CHECK"
			if check.OK {
				status = "OK"
			}
			return []string{check.Name, status, check.Detail}
		})
	case "watch":
		if len(args) != 0 {
			return fmt.Errorf("usage: anyusb watch")
		}
		res, err := c.request(ctx, http.MethodGet, "/v1/events", nil)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		_, err = io.Copy(output, res.Body)
		return err
	default:
		return fmt.Errorf("unknown command %q; run anyusb help", command)
	}
}

func list(ctx context.Context, c client, args []string, output io.Writer, jsonOutput bool) error {
	flags := flag.NewFlagSet("list", flag.ContinueOnError)
	flags.SetOutput(output)
	remote := flags.String("remote", "", "list exports at this hostname or IP")
	imports := flags.Bool("imports", false, "list local USB/IP imports")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (*remote != "" && *imports) {
		return fmt.Errorf("usage: anyusb list [--remote HOST | --imports]")
	}
	switch {
	case *remote != "":
		return listRows(ctx, c, "/v1/remote-devices?host="+url.QueryEscape(*remote), output, jsonOutput,
			[]string{"BUSID", "VID:PID", "DEVICE"}, func(d platform.RemoteDevice) []string {
				return []string{d.BusID, usbID(d.VendorID, d.ProductID), d.Name}
			})
	case *imports:
		return listRows(ctx, c, "/v1/imports", output, jsonOutput,
			[]string{"PORT", "HOST", "REMOTE BUSID", "SERIAL PORTS", "DEVICE"}, func(i platform.Import) []string {
				return []string{strconv.Itoa(i.Port), i.RemoteHost, i.RemoteBusID, strings.Join(i.SerialPorts, ", "), i.Name}
			})
	default:
		return listRows(ctx, c, "/v1/devices", output, jsonOutput,
			[]string{"BUSID", "ID", "VID:PID", "STATE", "DEVICE"}, func(d platform.Device) []string {
				state := "Local"
				if d.Exported {
					state = "Shared"
				}
				return []string{d.BusID, d.ID, usbID(d.VendorID, d.ProductID), state, strings.TrimSpace(d.Manufacturer + " " + d.Product)}
			})
	}
}

func usbID(vendor, product string) string {
	if vendor == "" || product == "" {
		return ""
	}
	return vendor + ":" + product
}

func listRows[T any](ctx context.Context, c client, path string, output io.Writer, jsonOutput bool, columns []string, fields func(T) []string) error {
	records, err := call[[]T](ctx, c, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(output).Encode(records)
	}
	return printTable(output, columns, records, fields)
}

func printTable[T any](output io.Writer, columns []string, records []T, fields func(T) []string) error {
	table := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, strings.Join(columns, "\t"))
	for _, record := range records {
		row := fields(record)
		for i, field := range row {
			row[i] = strings.Map(func(r rune) rune {
				if unicode.IsControl(r) {
					return ' '
				}
				return r
			}, field)
		}
		fmt.Fprintln(table, strings.Join(row, "\t"))
	}
	return table.Flush()
}
