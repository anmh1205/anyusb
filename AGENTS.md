# AnyUSB development

- Read [the architecture](docs/architecture.md) and linked ADRs before changing
  the service or platform boundaries. Use `anyusbd` for the agent and `anyusb`
  for the CLI. Keep native USB/IP tool names in commands and diagnostics.
- M0 contains documentation only. There is no build, runnable service, CLI, or
  unit test suite yet. Use `git diff --check` to check documentation changes.
- Follow the [M0 lab procedure](docs/test-report/m0-lab.md) for stock-tool testing.
  Record OS, kernel and tool versions, commands, outputs, and USB recovery times
  in `docs/test-report/`. Use `UNVERIFIED` for any result without a hardware run.
- Stop and report at M0 before starting M1. Obtain explicit approval before
  driver installation or firewall and network changes.
- Keep commits small and update [the changelog](CHANGELOG.md) when changing the
  project.
