# AnyUSB development

- Read [the architecture](docs/architecture.md) and linked ADRs before changing
  the service or platform boundaries. Use `anyusbd` for the agent and `anyusb`
  for the CLI. Keep native USB/IP tool names in commands and diagnostics.
- Build with Go 1.24+ using the [README](README.md#build). Format Go changes
  with `gofmt` and use `git diff --check` before committing.
- The entry points are `./agent/cmd/anyusbd` and `./cli`. The
  [build script](scripts/build.ps1) targets Linux and Windows on amd64 and arm64.
  Run `go test ./...` and `go vet ./...` on a supported OS.
- Follow the [M0 lab procedure](docs/test-report/m0-lab.md) for stock-tool testing.
  Record OS, kernel and tool versions, commands, outputs, and USB recovery times
  in `docs/test-report/`. Use `UNVERIFIED` for any result without a hardware run.
- See the [software verification report](docs/test-report/phase1-software.md)
  and M0 lab report for evidence and pending hardware checks. Obtain explicit
  approval before driver installation or firewall and network changes.
- Keep commits small and update [the changelog](CHANGELOG.md) when changing the
  project.
