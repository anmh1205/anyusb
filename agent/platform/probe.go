package platform

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type ProbeResult struct {
	Profile    string   `json:"profile"`
	Tool       string   `json:"tool,omitempty"`
	Version    string   `json:"version,omitempty"`
	Command    []string `json:"command,omitempty"`
	Serial     string   `json:"serial,omitempty"`
	Matched    bool     `json:"matched"`
	OK         bool     `json:"ok"`
	DurationMS int64    `json:"duration_ms"`
	Output     string   `json:"output,omitempty"`
	Error      string   `json:"error,omitempty"`
}

type probeSpec struct {
	names       []string
	commonPaths []string
	absolute    []string
	args        []string
	versionArgs []string
	jlink       bool
}

var profileProbeSpecs = map[string]probeSpec{
	"stlink": {
		names:       []string{"STM32_Programmer_CLI", "STM32_Programmer_CLI.exe"},
		commonPaths: []string{`STMicroelectronics\STM32Cube\STM32CubeProgrammer\bin\STM32_Programmer_CLI.exe`},
		absolute:    []string{"/opt/st/stm32cubeprogrammer/bin/STM32_Programmer_CLI"},
		args:        []string{"-l"},
		versionArgs: []string{"--version"},
	},
	"jlink": {
		names:       []string{"JLinkExe", "JLink.exe"},
		commonPaths: []string{`SEGGER\JLink\JLink.exe`},
		absolute:    []string{"/opt/SEGGER/JLink/JLinkExe"},
		versionArgs: []string{"-?"},
		jlink:       true,
	},
	"cmsis-dap": {
		names:       []string{"pyocd", "pyocd.exe"},
		args:        []string{"list"},
		versionArgs: []string{"--version"},
	},
}

func RunProfileProbe(ctx context.Context, profile, serial string) (ProbeResult, error) {
	return runProfileProbe(ctx, OSNativeRunner{}, profile, serial)
}

func runProfileProbe(ctx context.Context, runner NativeRunner, profile, serial string) (ProbeResult, error) {
	profile = strings.ToLower(strings.TrimSpace(profile))
	serial = strings.TrimSpace(serial)
	result := ProbeResult{Profile: profile, Serial: serial}
	spec, valid := profileProbeSpecs[profile]
	if !valid {
		return result, fmt.Errorf("%w: profile probe must be stlink, jlink, or cmsis-dap", ErrInvalid)
	}
	tool := findProbeTool(spec)
	if tool == "" {
		result.Error = fmt.Sprintf("optional %s probe tool is not installed or on PATH; generic USB/IP remains attached", profile)
		return result, nil
	}
	result.Tool = tool
	args := spec.args
	var commandFile string
	if spec.jlink {
		file, err := os.CreateTemp("", "anyusb-jlink-*.jlink")
		if err != nil {
			return result, err
		}
		commandFile = file.Name()
		if _, err := file.WriteString("ShowEmuList USB\nexit\n"); err != nil {
			file.Close()
			os.Remove(commandFile)
			return result, err
		}
		if err := file.Close(); err != nil {
			os.Remove(commandFile)
			return result, err
		}
		defer os.Remove(commandFile)
		args = []string{"-NoGui", "1", "-CommandFile", commandFile}
	}
	result.Version = probeToolVersion(ctx, runner, tool, spec.versionArgs)
	result.Command = append([]string{tool}, args...)
	command, err := runner.Run(ctx, CommandRequest{
		Tool: profile + " probe", Path: tool, Args: args, Environment: []string{"LC_ALL=C"}, Timeout: 20 * time.Second, OutputLimit: 64 << 10,
		SensitiveOutput: true,
	})
	result.DurationMS = command.DurationMS
	result.Command = command.Command
	text := command.Stdout
	if command.Stderr != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += command.Stderr
	}
	if command.Truncated {
		text += "\n[output truncated]"
	}
	result.Output = strings.TrimSpace(text)
	if err != nil {
		result.Error = err.Error()
	}
	if serial == "" {
		result.Error = "device serial is unavailable; tool output cannot be tied to this import"
		return result, nil
	}
	result.Matched = containsIdentifier(result.Output, serial)
	result.OK = err == nil && result.Matched
	if err == nil && !result.Matched {
		result.Error = "native tool did not report the imported device serial"
	}
	return result, nil
}

func findProbeTool(spec probeSpec) string {
	for _, name := range spec.names {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	for _, path := range spec.absolute {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	if runtime.GOOS == "windows" {
		roots := []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramW6432"), os.Getenv("ProgramFiles(x86)")}
		for _, root := range roots {
			if root == "" {
				continue
			}
			for _, pattern := range spec.commonPaths {
				path := filepath.Join(root, pattern)
				if info, err := os.Stat(path); err == nil && !info.IsDir() && filepath.IsAbs(path) {
					return path
				}
			}
		}
	}
	return ""
}

func probeToolVersion(ctx context.Context, runner NativeRunner, tool string, args []string) string {
	if len(args) == 0 {
		return ""
	}
	result, _ := runner.Run(ctx, CommandRequest{Tool: filepath.Base(tool) + " version", Path: tool, Args: args, Timeout: 5 * time.Second, OutputLimit: 4 << 10})
	output := result.Stdout + "\n" + result.Stderr
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			if len(line) > 240 {
				line = line[:240]
			}
			return line
		}
	}
	return "version unavailable"
}

// ProfileToolChecks reports optional native profile tools without treating a
// missing vendor tool as a broken USB/IP backend.
func ProfileToolChecks() []Check {
	profiles := []string{"stlink", "jlink", "cmsis-dap"}
	checks := make([]Check, 0, len(profiles))
	for _, profile := range profiles {
		path := findProbeTool(profileProbeSpecs[profile])
		detail := "optional profile tool not installed; generic USB/IP remains available"
		if path != "" {
			detail = "optional profile tool found at " + path
		}
		checks = append(checks, Check{Name: profile + " profile", OK: path != "", Optional: true, Detail: detail})
	}
	return checks
}

func containsIdentifier(output, serial string) bool {
	normalized := strings.NewReplacer(" ", "", "-", "", ":", "", "_", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(serial)))
	if normalized == "" {
		return false
	}
	var pattern strings.Builder
	pattern.WriteString(`(?i)(^|[^0-9a-z])`)
	for index, char := range normalized {
		if index > 0 {
			pattern.WriteString(`[\s:_.-]*`)
		}
		pattern.WriteString(regexp.QuoteMeta(string(char)))
	}
	pattern.WriteString(`([^0-9a-z]|$)`)
	return regexp.MustCompile(pattern.String()).FindStringIndex(output) != nil
}
