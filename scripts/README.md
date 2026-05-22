# Deployment Script

`scripts/deploy.sh` installs and updates the relay on Linux hosts with `systemd`.
It uses the current git checkout as its source tree and pulls from `origin` during updates.

## Supported actions

- `install` builds the relay, installs it to `/usr/local/bin/quantum-relay`, writes `/etc/rely/config.yaml`, creates the `quantum-relay` service, and configures Caddy or Nginx when detected.
- `update` pulls the latest source from GitHub, rebuilds the binary, restarts the service, and runs a smoke test.
- `test` checks that the service is active and that the relay responds on its configured listen address.
- `--dry-run` prints the planned actions without writing files or restarting services.
- In dry-run mode the installer does not prompt; it uses the values you provided on the command line or through environment variables.

## Proxy behavior

- Caddy snippets are written to `/etc/caddy/Caddyfile.d/quantum-relay.caddy`.
- If `/etc/caddy/Caddyfile` already exists and does not import a snippet directory, the script leaves it untouched and prints the import line to add manually.
- Nginx writes a dedicated `quantum-relay.rely.conf` site file in `sites-available` and symlinks it into `sites-enabled`.
- Existing unmanaged proxy files are never overwritten.
- If an install or update fails after making managed changes, the script restores backed-up files where possible and removes files it created in that run.
- The relay smoke test requests NIP-11 with `Accept: application/nostr+json`; plain GETs are expected to return `400` by the relay and are not used as readiness checks.
- When Caddy is detected, the installer also probes the public websocket endpoint over `wss://` using a real websocket upgrade handshake. Nginx is probed over `ws://` because the generated site config is HTTP-only unless you add TLS separately.
- Before starting the service, the installer stops any existing `quantum-relay` unit and checks whether the listen port is already occupied by another process. If the port is busy, it exits with a diagnostic instead of looping on restart failures.
- If the requested listen address is occupied, the installer automatically walks upward from the requested port until it finds a free local port, then writes that chosen port into the config and proxy snippets. If an existing config is present, it patches `relay.listen` to match the chosen port instead of leaving the old value behind.
- Proxy smoke tests retry for a short period before failing so first-start Caddy certificate issuance or reload lag does not trigger a false negative.
- If deployment fails after updating managed files, rollback stops the relay service first so the binary can be restored without hitting a `text file busy` error.

## Examples

```bash
./scripts/deploy.sh install --domain relay.example.com
./scripts/deploy.sh install --proxy nginx --domain relay.example.com
./scripts/deploy.sh update
./scripts/deploy.sh test
./scripts/deploy.sh install --domain relay.example.com --dry-run
```

## Environment overrides

- `RELY_PROXY=auto|caddy|nginx|none`
- `RELY_DOMAIN=relay.example.com`
- `RELY_LISTEN=127.0.0.1:8080`
- `RELY_NAME="Quantum Relay"`
- `RELY_DESCRIPTION="Nostr relay with quantum walk propagation"`
- `GO_BIN=/usr/local/go/bin/go` if Go is not on `PATH`
- `RELY_DRY_RUN=true`

## Notes

- The script expects Go 1.25 or newer because the module now targets Go 1.25.
- If a reverse proxy is configured, the relay listens on `127.0.0.1:8080` by default.
- The relay reads its runtime config from `RELY_CONFIG=/etc/rely/config.yaml` when launched by the service.
