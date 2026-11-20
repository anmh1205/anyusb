# 0001. Go agent and native USB/IP backends

**Proposed at M0.**

Use Go for `anyusbd` and `anyusb`, with one executable per tool and target
OS/architecture. The agent can embed the local web assets. Keep a common service
core with OS-specific adapters for USB inventory, USB/IP and service lifecycle.
Use existing userspace tools and operating-system drivers for the USB data path.

Go provides cross-compilation for the portable core, typed platform interfaces,
HTTP/TLS support, and cancellable subprocesses. Privileged backend work stays
inside the service. The CLI and web UI call its local API, which keeps inventory,
lease rules and backend state consistent between the two interfaces.

CLI wrapping keeps the implementation independent of each backend's internal
driver API and helps keep upstream packages separately serviceable. Prefer
structured output and OS inventory APIs. Use version-scoped parsers where the
only available interface is text. If a backend CLI cannot expose reliable
import state or cancel retry attempts, reassess that backend's adapter and
version before adopting direct driver calls.
