### v1.1.0

- Added inactivity timeout for downloads (default 20s) to prevent hanging on slow proxies.
- Added `--timeout` flag to configure the inactivity period.
- Added debug logging for proxy switching events.
- Integrated speed and ETA metrics into the `--verbose` logging mode.
- Improved `--json-output` flag to automatically enable `--verbose` mode.
- Improved `--json-output` to report full progress statistics every 5 seconds.

### v1.0.0

- Initial release.
