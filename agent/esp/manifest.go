// Package esp runs built-in ESP ROM bootloader and serial monitor operations on a host's physical serial port.
package esp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const MaxUpload = 128 << 20

type Manifest struct {
	FlashFiles    map[string]string `json:"flash_files"`
	FlashSettings map[string]string `json:"flash_settings,omitempty"`
	Extra         struct {
		Chip   string `json:"chip"`
		Before string `json:"before"`
		After  string `json:"after"`
	} `json:"extra_esptool_args,omitempty"`
}

type segment struct {
	Offset uint64
	Name   string
	Path   string
	Size   uint64
	Digest [32]byte
}

var settingValues = map[string][]string{
	"flash_mode": {"keep", "qio", "qout", "dio", "dout"},
	"flash_freq": {"keep", "12m", "15m", "16m", "20m", "24m", "26m", "30m", "40m", "48m", "60m", "80m", "120m"},
	"flash_size": {"keep", "detect", "256KB", "512KB", "1MB", "2MB", "4MB", "8MB", "16MB", "32MB", "64MB", "128MB"},
}

func options(m Manifest) (global, flash []string, err error) {
	chip := strings.ToLower(m.Extra.Chip)
	if chip == "" {
		chip = "auto"
	}
	allowed := []string{"auto", "esp8266", "esp32", "esp32s2", "esp32s3", "esp32c2", "esp32c3", "esp32c5", "esp32c6", "esp32c61", "esp32h2", "esp32p4"}
	if !oneOf(chip, allowed) {
		return nil, nil, fmt.Errorf("unsupported ESP chip %q", chip)
	}
	global = []string{"--chip", chip}
	before := strings.ReplaceAll(m.Extra.Before, "_", "-")
	if before != "" {
		if !oneOf(before, []string{"default-reset", "usb-reset", "no-reset", "no-reset-no-sync"}) {
			return nil, nil, fmt.Errorf("unsupported before-reset action %q", before)
		}
		global = append(global, "--before", before)
	}
	after := strings.ReplaceAll(m.Extra.After, "_", "-")
	if after == "" {
		after = "hard-reset"
	}
	if !oneOf(after, []string{"hard-reset", "soft-reset", "no-reset", "no-reset-stub", "watchdog-reset"}) {
		return nil, nil, fmt.Errorf("unsupported after-reset action %q", after)
	}
	global = append(global, "--after", after)
	for _, name := range []string{"flash_mode", "flash_freq", "flash_size"} {
		if v, ok := m.FlashSettings[name]; ok {
			matched := ""
			for _, allowed := range settingValues[name] {
				if strings.EqualFold(v, allowed) {
					matched = allowed
				}
			}
			if matched == "" {
				return nil, nil, fmt.Errorf("invalid %s %q", name, v)
			}
			flash = append(flash, "--"+strings.ReplaceAll(name, "_", "-"), matched)
		}
	}
	return global, flash, nil
}

func oneOf(value string, options []string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

func normalizeName(name string) (string, error) {
	name = strings.TrimPrefix(strings.ReplaceAll(name, `\`, "/"), "./")
	if name == "." || name == ".." || name == "" || strings.Contains(name, ":") || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("invalid firmware file name %q", name)
	}
	return name, nil
}

func readUpload(w http.ResponseWriter, r *http.Request, dir string) (Manifest, []segment, error) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload)
	parts, err := r.MultipartReader()
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("expected multipart firmware upload: %w", err)
	}
	var manifest Manifest
	hasManifest := false
	files := make(map[string]segment)
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, nil, err
		}
		switch part.FormName() {
		case "manifest":
			if hasManifest {
				return Manifest{}, nil, fmt.Errorf("multiple manifests")
			}
			data, err := io.ReadAll(io.LimitReader(part, (64<<10)+1))
			if err != nil || len(data) > 64<<10 {
				return Manifest{}, nil, fmt.Errorf("invalid manifest size")
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return Manifest{}, nil, fmt.Errorf("parse flash manifest: %w", err)
			}
			hasManifest = true
		case "files":
			_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			name, err := normalizeName(params["filename"])
			if err != nil || files[name].Path != "" || len(files) >= 64 {
				return Manifest{}, nil, fmt.Errorf("invalid, duplicate, or excessive firmware file %q", params["filename"])
			}
			file, err := os.CreateTemp(dir, "firmware-*.bin")
			if err != nil {
				return Manifest{}, nil, err
			}
			sum := sha256.New()
			size, err := io.Copy(io.MultiWriter(file, sum), part)
			closeErr := file.Close()
			if err != nil || closeErr != nil || size == 0 {
				return Manifest{}, nil, fmt.Errorf("could not store firmware file %q", name)
			}
			var digest [32]byte
			copy(digest[:], sum.Sum(nil))
			files[name] = segment{Name: name, Path: file.Name(), Size: uint64(size), Digest: digest}
		default:
			return Manifest{}, nil, fmt.Errorf("unexpected multipart field %q", part.FormName())
		}
		part.Close()
	}
	if !hasManifest || len(manifest.FlashFiles) == 0 || len(manifest.FlashFiles) > 64 {
		return Manifest{}, nil, fmt.Errorf("manifest needs 1 to 64 flash_files entries")
	}
	segments := make([]segment, 0, len(manifest.FlashFiles))
	for offset, name := range manifest.FlashFiles {
		radix := 10
		if strings.HasPrefix(strings.ToLower(offset), "0x") {
			radix, offset = 16, offset[2:]
		}
		address, err := strconv.ParseUint(offset, radix, 32)
		name, nameErr := normalizeName(name)
		file, exists := files[name]
		if err != nil || nameErr != nil || !exists || address+file.Size > 1<<32 {
			return Manifest{}, nil, fmt.Errorf("invalid offset or missing file for %s → %q", offset, name)
		}
		file.Offset = address
		segments = append(segments, file)
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].Offset < segments[j].Offset })
	for i := 1; i < len(segments); i++ {
		if prev := segments[i-1]; prev.Offset+prev.Size > segments[i].Offset {
			return Manifest{}, nil, fmt.Errorf("overlapping firmware segments at 0x%x and 0x%x", prev.Offset, segments[i].Offset)
		}
	}
	return manifest, segments, nil
}

// WriteUpload streams an IDF or explicit manifest and its files to the local agent.
func WriteUpload(w *multipart.Writer, manifest Manifest, paths map[string]string) error {
	part, err := w.CreateFormField("manifest")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(part).Encode(manifest); err != nil {
		return err
	}
	for name, filename := range paths {
		part, err := w.CreateFormFile("files", name)
		if err != nil {
			return err
		}
		file, err := os.Open(filename)
		if err != nil {
			return err
		}
		_, err = io.Copy(part, file)
		file.Close()
		if err != nil {
			return err
		}
	}
	return w.Close()
}

func IDFManifest(filename string) (Manifest, map[string]string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return Manifest{}, nil, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, nil, err
	}
	paths := make(map[string]string)
	for offset, name := range manifest.FlashFiles {
		normalized, err := normalizeName(name)
		if err != nil {
			return Manifest{}, nil, err
		}
		manifest.FlashFiles[offset] = normalized
		paths[normalized] = filepath.Join(filepath.Dir(filename), filepath.FromSlash(normalized))
	}
	return manifest, paths, nil
}
