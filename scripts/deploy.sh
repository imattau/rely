#!/usr/bin/env bash
set -Eeuo pipefail

APP_NAME="quantum-relay"
SERVICE_NAME="quantum-relay"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_BIN_DIR="/usr/local/bin"
INSTALL_BIN="${INSTALL_BIN_DIR}/${APP_NAME}"
CONFIG_DIR="/etc/rely"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
DATA_DIR="/var/lib/rely"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
CADDY_MAIN_FILE="/etc/caddy/Caddyfile"
CADDY_CADDYFILE_D="/etc/caddy/Caddyfile.d"
CADDY_CONF_D="/etc/caddy/conf.d"
NGINX_SITE_DIR="/etc/nginx/sites-available"
NGINX_ENABLED_DIR="/etc/nginx/sites-enabled"
NGINX_SITE_FILE="${NGINX_SITE_DIR}/${SERVICE_NAME}.rely.conf"
NGINX_ENABLED_FILE="${NGINX_ENABLED_DIR}/${SERVICE_NAME}.rely.conf"

DEFAULT_RELAY_NAME="Quantum Relay"
DEFAULT_RELAY_DESCRIPTION="Nostr relay with quantum walk propagation"
DEFAULT_DIRECT_LISTEN=":8080"
DEFAULT_PROXY_LISTEN="127.0.0.1:8080"
DEFAULT_PEER_LISTEN=":8081"
DEFAULT_PEER_PUBLIC_PORT="8443"
DEFAULT_GAMMA="0.5"
DEFAULT_FETCH_THRESHOLD="0.05"
DEFAULT_CONSENSUS_TICK_MS="500"
DEFAULT_QUANTUM_TICK_MS="1000"
DEFAULT_MAX_CONCURRENT_FETCHES="32"
DEFAULT_CLIENT_EVENTS_PER_SEC="10"
DEFAULT_PEER_ANNOUNCE_PER_SEC="100"
DEFAULT_STORAGE_PATH="/var/lib/rely/events.db"
DEFAULT_TRUST_WEIGHT="2.0"
MANAGED_MARKER="# managed by rely"

ACTION="install"
PROXY_MODE="${RELY_PROXY:-auto}"
DOMAIN="${RELY_DOMAIN:-}"
RELAY_NAME_ENV_SET=false
if [[ -n ${RELY_NAME+x} ]]; then
	RELAY_NAME_ENV_SET=true
fi
RELAY_NAME="${RELY_NAME:-$DEFAULT_RELAY_NAME}"
RELAY_DESCRIPTION_ENV_SET=false
if [[ -n ${RELY_DESCRIPTION+x} ]]; then
	RELAY_DESCRIPTION_ENV_SET=true
fi
RELAY_DESCRIPTION="${RELY_DESCRIPTION:-$DEFAULT_RELAY_DESCRIPTION}"
LISTEN_OVERRIDE="${RELY_LISTEN:-}"
PEER_LISTEN_OVERRIDE="${RELY_PEER_LISTEN:-}"
RELAY_LISTEN_RESOLVED=""
PEER_LISTEN_RESOLVED=""
NON_INTERACTIVE=false
DRY_RUN="${RELY_DRY_RUN:-false}"
GO_BIN="${GO_BIN:-}"
TEMP_DIRS=()
declare -A BACKUPS=()
declare -a CREATED_FILES=()
IN_ROLLBACK=false
CURRENT_ACTION=""
RELAY_NAME_FLAG_SET=false
RELAY_DESCRIPTION_FLAG_SET=false

log() {
	printf '[deploy] %s\n' "$*"
}

warn() {
	printf '[deploy] warning: %s\n' "$*" >&2
}

die() {
	printf '[deploy] error: %s\n' "$*" >&2
	exit 1
}

on_error() {
	local line="$1"
	local cmd="$2"
	printf '[deploy] error: command failed at line %s: %s\n' "$line" "$cmd" >&2
	if [[ "$IN_ROLLBACK" == false && "$CURRENT_ACTION" =~ ^(install|update)$ ]]; then
		rollback
	fi
	exit 1
}

trap 'on_error "$LINENO" "$BASH_COMMAND"' ERR
trap 'cleanup' EXIT

cleanup() {
	local dir
	for dir in "${TEMP_DIRS[@]}"; do
		if [[ -n "$dir" && -d "$dir" ]]; then
			rm -rf "$dir"
		fi
	done
}

is_true() {
	case "${1,,}" in
		1|true|yes|on) return 0 ;;
		*) return 1 ;;
	esac
}

ensure_not_dry_run() {
	if is_true "$DRY_RUN"; then
		return 1
	fi
	return 0
}

log_plan() {
	log "[dry-run] $*"
}

rollback() {
	if [[ "$IN_ROLLBACK" == true ]]; then
		return
	fi
	if [[ -z "${SUDO:-}" && "${EUID}" -ne 0 ]]; then
		return
	fi
	IN_ROLLBACK=true
	trap - ERR
	log "rolling back partial deployment"

	run_root systemctl stop "$SERVICE_NAME" 2>/dev/null || true
	run_root systemctl reset-failed "$SERVICE_NAME" 2>/dev/null || true

	local target backup
	for target in "${!BACKUPS[@]}"; do
		backup="${BACKUPS[$target]}"
		if [[ -f "$backup" ]]; then
			local restore_target
			restore_target="${target}.restore.$$"
			if [[ -n "${SUDO:-}" ]]; then
				"$SUDO" cp -a "$backup" "$restore_target"
				"$SUDO" mv -f "$restore_target" "$target"
			else
				cp -a "$backup" "$restore_target"
				mv -f "$restore_target" "$target"
			fi
		fi
	done

	for target in "${CREATED_FILES[@]}"; do
		if [[ -e "$target" ]]; then
			run_root rm -f "$target"
		fi
	done

	run_root systemctl daemon-reload 2>/dev/null || true
	run_root systemctl restart "$SERVICE_NAME" 2>/dev/null || true
	IN_ROLLBACK=false
}

usage() {
	cat <<'EOF'
Usage:
  scripts/deploy.sh install [--domain example.com] [--proxy auto|caddy|nginx|none] [--listen addr] [--name text] [--description text] [--non-interactive] [--dry-run]
  scripts/deploy.sh update
  scripts/deploy.sh test

Environment overrides:
  RELY_PROXY=auto|caddy|nginx|none
  RELY_DOMAIN=example.com
  RELY_LISTEN=127.0.0.1:8080
  RELY_PEER_LISTEN=127.0.0.1:8081
  RELY_NAME="Quantum Relay"
  RELY_DESCRIPTION="Nostr relay with quantum walk propagation"
  RELY_DRY_RUN=true
  GO_BIN=/path/to/go

Defaults:
  binary: /usr/local/bin/quantum-relay
  config: /etc/rely/config.yaml
  data:   /var/lib/rely
EOF
}

print_install_plan() {
	log_plan "would build ${APP_NAME} from ${REPO_ROOT}"
	log_plan "would install the binary to ${INSTALL_BIN}"
	log_plan "would write config to ${CONFIG_FILE}"
	log_plan "would write systemd service to ${SERVICE_FILE}"
	if [[ "$PROXY_MODE" == "none" ]]; then
		log_plan "would skip reverse proxy setup"
	else
		log_plan "would configure ${PROXY_MODE} for domain ${DOMAIN}"
		log_plan "would configure peer endpoint on port ${DEFAULT_PEER_PUBLIC_PORT}"
	fi
	log_plan "would restart ${SERVICE_NAME} and smoke-test the relay"
}

print_update_plan() {
	log_plan "would fetch and pull the latest source in ${REPO_ROOT}"
	log_plan "would rebuild ${APP_NAME} and reinstall it to ${INSTALL_BIN}"
	log_plan "would restart ${SERVICE_NAME} and smoke-test the relay"
}

print_test_plan() {
	log_plan "would verify ${SERVICE_NAME} is active"
	log_plan "would probe the relay at the configured listen address"
	if [[ "$PROXY_MODE" != "none" && -n "$DOMAIN" ]]; then
		log_plan "would also probe the reverse proxy for ${DOMAIN}"
	fi
}

detect_existing_proxy_config() {
	local caddy_snippet_file
	caddy_snippet_file="$(caddy_snippet_file)"
	if [[ -f "$caddy_snippet_file" ]]; then
		PROXY_SMOKE_MODE="caddy"
		PROXY_SMOKE_DOMAIN="$(awk 'NR==2 { gsub(/[[:space:]]*\{.*$/, "", $0); print $0; exit }' "$caddy_snippet_file")"
		return 0
	fi
	if [[ -f "$NGINX_SITE_FILE" ]]; then
		PROXY_SMOKE_MODE="nginx"
		PROXY_SMOKE_DOMAIN="$(awk '/^[[:space:]]*server_name[[:space:]]+/ { sub(/^[[:space:]]*server_name[[:space:]]+/, "", $0); sub(/;[[:space:]]*$/, "", $0); print $0; exit }' "$NGINX_SITE_FILE")"
		return 0
	fi
	return 1
}

caddy_snippet_dir() {
	if [[ -n "${RELY_CADDY_SNIPPET_DIR:-}" ]]; then
		printf '%s' "$RELY_CADDY_SNIPPET_DIR"
		return 0
	fi
	if [[ -f "$CADDY_MAIN_FILE" ]]; then
		if grep -qE '^[[:space:]]*import[[:space:]].*(/etc/caddy/)?conf\.d/\*' "$CADDY_MAIN_FILE"; then
			printf '%s' "$CADDY_CONF_D"
			return 0
		fi
		if grep -qE '^[[:space:]]*import[[:space:]].*(/etc/caddy/)?Caddyfile\.d/\*' "$CADDY_MAIN_FILE"; then
			printf '%s' "$CADDY_CADDYFILE_D"
			return 0
		fi
	fi
	if [[ -d "$CADDY_CONF_D" ]]; then
		printf '%s' "$CADDY_CONF_D"
		return 0
	fi
	printf '%s' "$CADDY_CADDYFILE_D"
}

caddy_snippet_file() {
	local dir
	dir="$(caddy_snippet_dir)"
	printf '%s/%s.caddy' "$dir" "$SERVICE_NAME"
}

proxy_smoke_test() {
	local mode="$1"
	local domain="$2"
	local peer_port="${3:-}"
	if [[ -z "$domain" ]]; then
		warn "skipping proxy smoke test because no domain could be determined"
		return 0
	fi

	log "probing ${mode} proxy for ${domain}"
	case "$mode" in
		caddy)
			local ok=false
			for _ in $(seq 1 30); do
				if curl -fsS --noproxy '*' --max-time 20 -L -k \
					--resolve "${domain}:80:127.0.0.1" \
					--resolve "${domain}:443:127.0.0.1" \
					-H 'Accept: application/nostr+json' \
					"https://${domain}/" >/dev/null; then
					ok=true
					break
				fi
				sleep 2
			done
			if [[ "$ok" != true ]]; then
				warn "https proxy probe failed for ${domain}; checking whether Caddy is still provisioning TLS"
				if systemctl is-active --quiet caddy 2>/dev/null; then
					warn "Caddy is active; leaving the install in place and allowing TLS to finish provisioning"
					return 0
				fi
				die "https proxy probe failed for ${domain}"
			fi
			if [[ -n "$peer_port" ]]; then
				local peer_ok=false
				for _ in $(seq 1 30); do
					if probe_websocket_proxy "$domain" "$peer_port" "wss"; then
						peer_ok=true
						break
					fi
					sleep 2
				done
				if [[ "$peer_ok" != true ]]; then
					die "wss proxy probe failed for ${domain}:${peer_port}"
				fi
			fi
			probe_websocket_proxy "$domain" "443" "wss"
			;;
		nginx)
			local ok=false
			for _ in $(seq 1 15); do
				if curl -fsS --noproxy '*' --max-time 10 \
					--resolve "${domain}:80:127.0.0.1" \
					-H 'Accept: application/nostr+json' \
					"http://${domain}/" >/dev/null; then
					ok=true
					break
				fi
				sleep 1
			done
			if [[ "$ok" != true ]]; then
				die "http proxy probe failed for ${domain}"
			fi
			probe_websocket_proxy "$domain" "80" "ws"
			;;
		*)
			warn "unknown proxy mode ${mode}; skipping proxy smoke test"
			return 0
			;;
	esac
}

listen_port_in_use() {
	local listen="$1"
	local port
	port="${listen##*:}"
	if [[ "$port" == "$listen" ]]; then
		return 1
	fi
	if ! command -v ss >/dev/null 2>&1; then
		return 1
	fi
	ss -H -ltn "sport = :${port}" | grep -q .
}

normalize_listen_identity() {
	local listen="$1"
	local host port
	host="${listen%:*}"
	port="${listen##*:}"
	if [[ -z "$host" || "$host" == "$listen" ]]; then
		host="127.0.0.1"
	fi
	if [[ -z "$port" || "$port" == "$listen" ]]; then
		port="8080"
	fi
	printf '%s:%s' "$host" "$port"
}

listen_matches() {
	local left right
	left="$(normalize_listen_identity "$1")"
	right="$(normalize_listen_identity "$2")"
	[[ "$left" == "$right" ]]
}

pick_free_listen() {
	local desired="$1"
	local avoid="${2:-}"
	local host port candidate

	host="${desired%:*}"
	port="${desired##*:}"
	if [[ -z "$host" || "$host" == "$desired" ]]; then
		host="127.0.0.1"
	fi
	if [[ -z "$port" || "$port" == "$desired" ]]; then
		port="8080"
	fi

	candidate="${host}:${port}"
	if ! command -v ss >/dev/null 2>&1; then
		warn "ss not available; using requested listen address ${candidate}"
		printf '%s' "$candidate"
		return 0
	fi

	while listen_port_in_use "$candidate" || { [[ -n "$avoid" ]] && listen_matches "$candidate" "$avoid"; }; do
		port=$((port + 1))
		if (( port > 65535 )); then
			die "no free listen ports available starting at ${desired}"
		fi
		candidate="${host}:${port}"
	done

	if [[ "$candidate" != "$desired" ]]; then
		warn "listen address ${desired} is busy; using ${candidate} instead"
	fi

	printf '%s' "$candidate"
}

update_config_listen() {
	local listen="$1"
	local current
	if [[ ! -f "$CONFIG_FILE" ]]; then
		return 0
	fi
	current="$(parse_listen_from_config)"
	if [[ -z "$current" || "$current" == "$listen" ]]; then
		return 0
	fi

	log "updating relay listen in existing config from ${current} to ${listen}"
	local tmp backup_dir backup_file
	tmp="$(mktemp)"
	if [[ -f "$CONFIG_FILE" ]]; then
		backup_dir="$(mktemp -d)"
		TEMP_DIRS+=("$backup_dir")
		backup_file="${backup_dir}/$(basename "$CONFIG_FILE").bak"
		run_root cp -a "$CONFIG_FILE" "$backup_file"
		BACKUPS["$CONFIG_FILE"]="$backup_file"
	fi

	awk -v new_listen="$listen" '
		BEGIN {
			in_relay = 0
			replaced = 0
		}
		/^[[:space:]]*relay:[[:space:]]*$/ {
			print
			in_relay = 1
			next
		}
		in_relay && /^[[:space:]]*listen:[[:space:]]*/ && !replaced {
			printf "  listen: \"%s\"\n", new_listen
			replaced = 1
			next
		}
		in_relay && /^[^[:space:]]/ {
			if (!replaced) {
				printf "  listen: \"%s\"\n", new_listen
				replaced = 1
			}
			in_relay = 0
		}
		{
			print
		}
		END {
			if (in_relay && !replaced) {
				printf "  listen: \"%s\"\n", new_listen
			}
		}
	' "$CONFIG_FILE" >"$tmp"
	run_root install -m 0644 "$tmp" "$CONFIG_FILE"
	rm -f "$tmp"
}

update_config_peer_listen() {
	local listen="$1"
	local current
	if [[ ! -f "$CONFIG_FILE" ]]; then
		return 0
	fi
	current="$(parse_peer_listen_from_config)"
	if [[ -z "$current" || "$current" == "$listen" ]]; then
		return 0
	fi

	log "updating peer listen in existing config from ${current} to ${listen}"
	local tmp backup_dir backup_file
	tmp="$(mktemp)"
	backup_dir="$(mktemp -d)"
	TEMP_DIRS+=("$backup_dir")
	backup_file="${backup_dir}/$(basename "$CONFIG_FILE").bak"
	run_root cp -a "$CONFIG_FILE" "$backup_file"
	BACKUPS["$CONFIG_FILE"]="$backup_file"

	awk -v new_listen="$listen" '
		BEGIN {
			in_peer = 0
			replaced = 0
		}
		/^[[:space:]]*peer:[[:space:]]*$/ {
			print
			in_peer = 1
			next
		}
		in_peer && /^[[:space:]]*listen:[[:space:]]*/ && !replaced {
			printf "  listen: \"%s\"\n", new_listen
			replaced = 1
			next
		}
		in_peer && /^[^[:space:]]/ {
			if (!replaced) {
				printf "  listen: \"%s\"\n", new_listen
				replaced = 1
			}
			in_peer = 0
		}
		{
			print
		}
		END {
			if (in_peer && !replaced) {
				printf "  listen: \"%s\"\n", new_listen
			}
		}
	' "$CONFIG_FILE" >"$tmp"
	run_root install -m 0644 "$tmp" "$CONFIG_FILE"
	rm -f "$tmp"
}

check_listen_port_free() {
	local listen="$1"
	local port
	port="${listen##*:}"
	if [[ "$port" == "$listen" ]]; then
		warn "unable to parse listen port from ${listen}; skipping pre-start port check"
		return 0
	fi

	if ! command -v ss >/dev/null 2>&1; then
		warn "ss not available; skipping pre-start port check for ${listen}"
		return 0
	fi

	if ss -H -ltn "sport = :${port}" | grep -q .; then
		warn "port ${port} is already in use before starting ${SERVICE_NAME}"
		ss -H -ltnp "sport = :${port}" || true
		die "listen address ${listen} is occupied; stop the other process or set RELY_LISTEN to a free port"
	fi
}

probe_websocket_proxy() {
	local domain="$1"
	local port="$2"
	local scheme="$3"
	local response
	local key="dGhlIHNhbXBsZSBub25jZQ=="

	if ! command -v openssl >/dev/null 2>&1; then
		warn "openssl not found; skipping ${scheme} websocket smoke test for ${domain}"
		return 0
	fi

	response="$(
		printf 'GET / HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n' "$domain" "$key" |
			openssl s_client -connect "${domain}:${port}" -servername "$domain" -quiet 2>/dev/null |
			head -n 20
	)"

	if ! grep -q "101 Switching Protocols" <<<"$response"; then
		die "${scheme} websocket probe failed for ${domain}:${port}"
	fi
}

require_root_tools() {
	command -v git >/dev/null 2>&1 || die "git is required"
	command -v curl >/dev/null 2>&1 || die "curl is required"
	command -v openssl >/dev/null 2>&1 || warn "openssl is not installed; websocket proxy smoke tests will be skipped"
	command -v ss >/dev/null 2>&1 || warn "ss is not installed; port conflict diagnostics will be limited"
	command -v systemctl >/dev/null 2>&1 || die "systemctl is required"
	command -v install >/dev/null 2>&1 || die "install is required"
	detect_go_binary
}

detect_go_binary() {
	if [[ -n "$GO_BIN" && -x "$GO_BIN" ]]; then
		return
	fi
	if command -v go >/dev/null 2>&1; then
		GO_BIN="$(command -v go)"
		return
	fi
	if [[ -x /usr/lib/go-1.24/bin/go ]]; then
		GO_BIN="/usr/lib/go-1.24/bin/go"
		return
	fi
	die "go is required"
}

check_go_version() {
	local have need
	need="1.25.0"
	have="$("$GO_BIN" version | awk '{print $3}' | sed 's/^go//')"
	if [[ "$(printf '%s\n%s\n' "$need" "$have" | sort -V | head -n1)" != "$need" ]]; then
		die "Go ${need} or newer is required; found ${have}"
	fi
}

ensure_sudo() {
	if [[ "${EUID}" -ne 0 ]]; then
		command -v sudo >/dev/null 2>&1 || die "sudo is required when not running as root"
		sudo -v
		SUDO="sudo"
	else
		SUDO=""
	fi
}

run_root() {
	if [[ -n "${SUDO}" ]]; then
		"${SUDO}" "$@"
	else
		"$@"
	fi
}

prompt() {
	local question="$1"
	local default="$2"
	local answer=""

	if [[ "$NON_INTERACTIVE" == true || ! -t 0 ]]; then
		answer="$default"
	else
		if [[ -r /dev/tty && -w /dev/tty ]]; then
			printf '%s [%s]: ' "$question" "$default" >/dev/tty
			exec 3</dev/tty
			IFS= read -r -u 3 answer || true
			exec 3<&-
		else
			read -r -p "${question} [${default}]: " answer
		fi
	fi

	answer="${answer:-$default}"

	printf '%s' "$answer"
}

prompt_required() {
	local __var="$1"
	local question="$2"
	local default="$3"
	local answer=""

	while :; do
		answer="$(prompt "$question" "$default")"
		if [[ -n "$answer" ]]; then
			break
		fi
		if [[ "$NON_INTERACTIVE" == true || ! -t 0 ]]; then
			break
		fi
		warn "value cannot be empty"
	done

	printf -v "${__var}" '%s' "$answer"
}

version_gate() {
	local have="$1"
	local need="$2"
	if [[ "$(printf '%s\n%s\n' "$need" "$have" | sort -V | head -n1)" != "$need" ]]; then
		return 1
	fi
}

repo_branch() {
	local branch
	branch="$(git -C "$REPO_ROOT" branch --show-current 2>/dev/null || true)"
	if [[ -z "$branch" ]]; then
		branch="main"
	fi
	printf '%s' "$branch"
}

update_source() {
	log "updating source checkout in ${REPO_ROOT}"
	local status
	status="$(git -C "$REPO_ROOT" status --porcelain)"
	if [[ -n "$status" ]]; then
		die "repository has local changes; commit or stash them before updating"
	fi
	git -C "$REPO_ROOT" fetch --tags origin
	git -C "$REPO_ROOT" pull --ff-only origin "$(repo_branch)"
}

build_binary() {
	local tmpdir
	tmpdir="$(mktemp -d)"
	TEMP_DIRS+=("$tmpdir")
	BINARY_TMP="${tmpdir}/${APP_NAME}"
	log "building ${APP_NAME}"
	(
		cd "$REPO_ROOT"
		"$GO_BIN" build -trimpath -o "$BINARY_TMP" ./cmd/quantum-relay
	)
	run_root install -d -m 0755 "$INSTALL_BIN_DIR"
	if [[ -f "$INSTALL_BIN" ]]; then
		local backup_dir backup_file
		backup_dir="$(mktemp -d)"
		TEMP_DIRS+=("$backup_dir")
		backup_file="${backup_dir}/$(basename "$INSTALL_BIN").bak"
		run_root cp -a "$INSTALL_BIN" "$backup_file"
		BACKUPS["$INSTALL_BIN"]="$backup_file"
	else
		CREATED_FILES+=("$INSTALL_BIN")
	fi
	run_root install -m 0755 "$BINARY_TMP" "$INSTALL_BIN"
}

choose_listen() {
	if [[ -n "$LISTEN_OVERRIDE" ]]; then
		printf '%s' "$LISTEN_OVERRIDE"
		return
	fi
	if [[ "$PROXY_MODE" == "none" ]]; then
		printf '%s' "$DEFAULT_DIRECT_LISTEN"
		return
	fi
	printf '%s' "$DEFAULT_PROXY_LISTEN"
}

choose_peer_listen() {
	if [[ -n "$PEER_LISTEN_OVERRIDE" ]]; then
		printf '%s' "$PEER_LISTEN_OVERRIDE"
		return
	fi
	printf '%s' "$DEFAULT_PEER_LISTEN"
}

choose_public_url() {
	if [[ -z "$DOMAIN" ]]; then
		printf '%s' ""
		return
	fi
	case "$PROXY_MODE" in
		caddy) printf 'wss://%s' "$DOMAIN" ;;
		nginx) printf 'ws://%s' "$DOMAIN" ;;
		none) printf '%s' "" ;;
		*) printf 'wss://%s' "$DOMAIN" ;;
	esac
}

detect_proxy_mode() {
	if [[ "$PROXY_MODE" != "auto" ]]; then
		printf '%s' "$PROXY_MODE"
		return
	fi
	if command -v caddy >/dev/null 2>&1; then
		printf '%s' "caddy"
		return
	fi
	if command -v nginx >/dev/null 2>&1; then
		printf '%s' "nginx"
		return
	fi
	printf '%s' "none"
}

ensure_proxy_domain() {
	if [[ "$PROXY_MODE" == "none" ]]; then
		return
	fi

	if [[ -z "$DOMAIN" ]]; then
		if [[ "$NON_INTERACTIVE" == true || ! -t 0 ]]; then
			die "a domain is required when configuring a reverse proxy"
		fi
		DOMAIN="$(prompt "Reverse proxy hostname" "relay.example.com")"
	fi

	if [[ -z "$DOMAIN" ]]; then
		die "a domain is required when configuring a reverse proxy"
	fi
}

ensure_system_user() {
	if ! id -u rely >/dev/null 2>&1; then
		log "creating rely system user"
		run_root useradd --system --home-dir "$DATA_DIR" --shell /usr/sbin/nologin --user-group rely
	fi
}

ensure_directories() {
	run_root install -d -m 0755 "$CONFIG_DIR"
	run_root install -d -m 0755 "$DATA_DIR"
}

write_config() {
	local listen
	listen="${RELAY_LISTEN_RESOLVED:-$(choose_listen)}"
	local public_url
	public_url="$(choose_public_url)"
	local peer_listen
	peer_listen="${PEER_LISTEN_RESOLVED:-$(choose_peer_listen)}"
	if [[ -f "$CONFIG_FILE" ]]; then
		log "rewriting complete config at ${CONFIG_FILE}"
		local backup_dir backup_file
		backup_dir="$(mktemp -d)"
		TEMP_DIRS+=("$backup_dir")
		backup_file="${backup_dir}/$(basename "$CONFIG_FILE").bak"
		run_root cp -a "$CONFIG_FILE" "$backup_file"
		BACKUPS["$CONFIG_FILE"]="$backup_file"
	else
		log "writing config to ${CONFIG_FILE}"
	fi
	local tmp
	tmp="$(mktemp)"
	cat >"$tmp" <<EOF
relay:
  listen: "${listen}"
  public_url: "${public_url}"
  name: "${RELAY_NAME}"
  description: "${RELAY_DESCRIPTION}"

peer:
  listen: "${peer_listen}"
  public_port: ${DEFAULT_PEER_PUBLIC_PORT}

quantum:
  gamma: ${DEFAULT_GAMMA}
  fetch_threshold: ${DEFAULT_FETCH_THRESHOLD}
  consensus_tick_ms: ${DEFAULT_CONSENSUS_TICK_MS}
  quantum_tick_ms: ${DEFAULT_QUANTUM_TICK_MS}
  max_concurrent_fetches: ${DEFAULT_MAX_CONCURRENT_FETCHES}

storage:
  path: "${DEFAULT_STORAGE_PATH}"

auth:
  required: false

spam:
  client_events_per_sec: ${DEFAULT_CLIENT_EVENTS_PER_SEC}
  peer_announce_per_sec: ${DEFAULT_PEER_ANNOUNCE_PER_SEC}

peers: []

trust:
  enabled: false
  weight: ${DEFAULT_TRUST_WEIGHT}
  peers: []
EOF
	run_root install -m 0644 "$tmp" "$CONFIG_FILE"
	CREATED_FILES+=("$CONFIG_FILE")
	rm -f "$tmp"
}

is_managed_file() {
	local file="$1"
	[[ -f "$file" ]] || return 1
	grep -qF "$MANAGED_MARKER" "$file"
}

write_managed_file() {
	local target="$1"
	local source="$2"
	if [[ -f "$target" ]] && ! is_managed_file "$target"; then
		die "${target} exists and is not managed by rely; refusing to overwrite"
	fi

	if [[ -f "$target" ]]; then
		local backup_dir backup_file
		backup_dir="$(mktemp -d)"
		TEMP_DIRS+=("$backup_dir")
		backup_file="${backup_dir}/$(basename "$target").bak"
		run_root cp -a "$target" "$backup_file"
		BACKUPS["$target"]="$backup_file"
	else
		CREATED_FILES+=("$target")
	fi

	run_root install -m 0644 "$source" "$target"
}

write_service() {
	log "writing systemd unit to ${SERVICE_FILE}"
	local tmp
	tmp="$(mktemp)"
	cat >"$tmp" <<EOF
[Unit]
Description=Quantum relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=rely
Group=rely
Environment=RELY_CONFIG=${CONFIG_FILE}
StateDirectory=rely
WorkingDirectory=${DATA_DIR}
ExecStart=${INSTALL_BIN}
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
EOF
	if [[ -f "$SERVICE_FILE" ]]; then
		local backup_dir backup_file
		backup_dir="$(mktemp -d)"
		TEMP_DIRS+=("$backup_dir")
		backup_file="${backup_dir}/$(basename "$SERVICE_FILE").bak"
		run_root cp -a "$SERVICE_FILE" "$backup_file"
		BACKUPS["$SERVICE_FILE"]="$backup_file"
	else
		CREATED_FILES+=("$SERVICE_FILE")
	fi
	run_root install -m 0644 "$tmp" "$SERVICE_FILE"
	rm -f "$tmp"
}

write_caddy_config() {
	local domain="$1"
	local listen="$2"
	local snippet_dir snippet_file import_line
	snippet_dir="$(caddy_snippet_dir)"
	snippet_file="$(caddy_snippet_file)"
	import_line="import ${snippet_dir}/*.caddy"
	log "configuring Caddy for ${domain}"
	run_root install -d -m 0755 "$snippet_dir"
	local tmp
	tmp="$(mktemp)"
	cat >"$tmp" <<EOF
${MANAGED_MARKER}
${domain} {
	encode zstd gzip
	reverse_proxy ${listen}
}
EOF
	write_managed_file "$snippet_file" "$tmp"
	rm -f "$tmp"

	if [[ ! -f "$CADDY_MAIN_FILE" ]]; then
		local main_tmp
		main_tmp="$(mktemp)"
		cat >"$main_tmp" <<EOF
${MANAGED_MARKER}
${import_line}
EOF
		run_root install -d -m 0755 "$(dirname "$CADDY_MAIN_FILE")"
		run_root install -m 0644 "$main_tmp" "$CADDY_MAIN_FILE"
		CREATED_FILES+=("$CADDY_MAIN_FILE")
		rm -f "$main_tmp"
	elif grep -qE '^[[:space:]]*import[[:space:]].*(Caddyfile\.d|conf\.d)/\*' "$CADDY_MAIN_FILE"; then
		log "existing Caddyfile already imports a snippet directory; leaving it unchanged"
	else
		warn "existing Caddyfile does not import ${snippet_dir}; leaving it unchanged to avoid overwriting"
		warn "add this line manually if you want Caddy to load the relay snippet:"
		warn "${import_line}"
		return 0
	fi
}

write_caddy_peer_config() {
	local domain="$1"
	local listen="$2"
	local port="$3"
	local snippet_dir snippet_file
	snippet_dir="$(caddy_snippet_dir)"
	snippet_file="${snippet_dir}/${SERVICE_NAME}-peer.caddy"

	if [[ -z "$port" ]]; then
		port="$DEFAULT_PEER_PUBLIC_PORT"
	fi

	log "configuring Caddy peer endpoint for ${domain}:${port}"
	run_root install -d -m 0755 "$snippet_dir"
	local tmp
	tmp="$(mktemp)"
	cat >"$tmp" <<EOF
${MANAGED_MARKER}
${domain}:${port} {
	encode zstd gzip
	reverse_proxy ${listen}
}
EOF
	write_managed_file "$snippet_file" "$tmp"
	rm -f "$tmp"
}

write_nginx_config() {
	local domain="$1"
	local listen="$2"
	log "configuring nginx for ${domain}"
	run_root install -d -m 0755 "$NGINX_SITE_DIR"
	run_root install -d -m 0755 "$NGINX_ENABLED_DIR"
	local tmp
	tmp="$(mktemp)"
	cat >"$tmp" <<EOF
${MANAGED_MARKER}
server {
	listen 80;
	server_name ${domain};

	location / {
		proxy_pass http://${listen};
		proxy_http_version 1.1;
		proxy_set_header Upgrade \$http_upgrade;
		proxy_set_header Connection "upgrade";
		proxy_set_header Host \$host;
		proxy_set_header X-Real-IP \$remote_addr;
		proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
		proxy_set_header X-Forwarded-Proto \$scheme;
		proxy_read_timeout 3600;
		proxy_send_timeout 3600;
	}
}
EOF
	write_managed_file "$NGINX_SITE_FILE" "$tmp"
	rm -f "$tmp"
	if [[ -e "$NGINX_ENABLED_FILE" && ! -L "$NGINX_ENABLED_FILE" ]]; then
		die "${NGINX_ENABLED_FILE} exists and is not a symlink; refusing to overwrite"
	fi
	if [[ -L "$NGINX_ENABLED_FILE" ]]; then
		local current_target
		current_target="$(readlink "$NGINX_ENABLED_FILE")"
		if [[ "$current_target" != "$NGINX_SITE_FILE" ]]; then
			die "${NGINX_ENABLED_FILE} already points to ${current_target}; refusing to overwrite"
		fi
	else
		CREATED_FILES+=("$NGINX_ENABLED_FILE")
	fi
	run_root ln -sfn "$NGINX_SITE_FILE" "$NGINX_ENABLED_FILE"
	run_root nginx -t
	run_root systemctl reload nginx 2>/dev/null || run_root systemctl restart nginx
}

configure_proxy() {
	local mode="$1"
	local listen="$2"
	local peer_listen="$3"
	local peer_public_port="$4"
	if [[ "$mode" == "none" ]]; then
		warn "no reverse proxy detected or requested; skipping proxy setup"
		return
	fi
	if [[ -z "$DOMAIN" ]]; then
		warn "skipping reverse proxy setup because no domain was provided"
		return
	fi
	case "$mode" in
		caddy)
			write_caddy_config "$DOMAIN" "$listen"
			write_caddy_peer_config "$DOMAIN" "$peer_listen" "$peer_public_port"
			run_root caddy validate --config "$CADDY_MAIN_FILE"
			run_root systemctl reload caddy 2>/dev/null || run_root systemctl restart caddy
			;;
		nginx)
			write_nginx_config "$DOMAIN" "$listen"
			warn "peer endpoint setup is currently only automated for Caddy; configure nginx manually for ${DOMAIN}:${peer_public_port} -> ${peer_listen}"
			;;
		*) die "unknown proxy mode: ${mode}" ;;
	esac
}

wait_for_relay() {
	local listen="$1"
	local host port url i
	if [[ "$listen" == :* ]]; then
		host="127.0.0.1"
		port="${listen#:}"
	else
		host="${listen%:*}"
		port="${listen##*:}"
		if [[ -z "$host" || "$host" == "0.0.0.0" ]]; then
			host="127.0.0.1"
		fi
	fi
	url="http://${host}:${port}/"

	for i in $(seq 1 30); do
		if curl -fsS --max-time 5 -H 'Accept: application/nostr+json' "$url" >/dev/null 2>&1; then
			log "relay responded on ${url}"
			return 0
		fi
		sleep 1
	done

	if command -v systemctl >/dev/null 2>&1; then
		warn "relay did not respond; checking service status"
		run_root systemctl --no-pager --full status "$SERVICE_NAME" || true
	fi

	die "relay did not respond at ${url}"
}

parse_listen_from_config() {
	if [[ ! -f "$CONFIG_FILE" ]]; then
		printf '%s' "$DEFAULT_PROXY_LISTEN"
		return
	fi
	local listen
	listen="$(awk '
		/^[[:space:]]*relay:[[:space:]]*$/ { in_relay=1; next }
		in_relay && /^[[:space:]]*listen:[[:space:]]*/ {
			sub(/^[[:space:]]*listen:[[:space:]]*/, "", $0)
			gsub(/"/, "", $0)
			gsub(/\047/, "", $0)
			print $0
			exit
		}
	' "$CONFIG_FILE")"
	if [[ -z "$listen" ]]; then
		printf '%s' "$DEFAULT_PROXY_LISTEN"
		return
	fi
	printf '%s' "$listen"
}

parse_public_url_from_config() {
	if [[ ! -f "$CONFIG_FILE" ]]; then
		printf '%s' ""
		return
	fi
	local public_url
	public_url="$(awk '
		/^[[:space:]]*relay:[[:space:]]*$/ { in_relay=1; next }
		in_relay && /^[[:space:]]*public_url:[[:space:]]*/ {
			sub(/^[[:space:]]*public_url:[[:space:]]*/, "", $0)
			gsub(/"/, "", $0)
			gsub(/\047/, "", $0)
			print $0
			exit
		}
	' "$CONFIG_FILE")"
	printf '%s' "${public_url:-}"
}

parse_relay_name_from_config() {
	if [[ ! -f "$CONFIG_FILE" ]]; then
		printf '%s' ""
		return
	fi
	local value
	value="$(awk '
		/^[[:space:]]*relay:[[:space:]]*$/ { in_relay=1; next }
		in_relay && /^[[:space:]]*name:[[:space:]]*/ {
			sub(/^[[:space:]]*name:[[:space:]]*/, "", $0)
			gsub(/"/, "", $0)
			gsub(/\047/, "", $0)
			print $0
			exit
		}
	' "$CONFIG_FILE")"
	printf '%s' "${value:-}"
}

parse_relay_description_from_config() {
	if [[ ! -f "$CONFIG_FILE" ]]; then
		printf '%s' ""
		return
	fi
	local value
	value="$(awk '
		/^[[:space:]]*relay:[[:space:]]*$/ { in_relay=1; next }
		in_relay && /^[[:space:]]*description:[[:space:]]*/ {
			sub(/^[[:space:]]*description:[[:space:]]*/, "", $0)
			gsub(/"/, "", $0)
			gsub(/\047/, "", $0)
			print $0
			exit
		}
	' "$CONFIG_FILE")"
	printf '%s' "${value:-}"
}

update_config_public_url() {
	local public_url="$1"
	local current
	if [[ ! -f "$CONFIG_FILE" ]]; then
		return 0
	fi
	current="$(parse_public_url_from_config)"
	if [[ "$current" == "$public_url" ]]; then
		return 0
	fi

	log "updating relay public_url in existing config from ${current:-<empty>} to ${public_url:-<empty>}"
	local tmp backup_dir backup_file
	tmp="$(mktemp)"
	backup_dir="$(mktemp -d)"
	TEMP_DIRS+=("$backup_dir")
	backup_file="${backup_dir}/$(basename "$CONFIG_FILE").bak"
	run_root cp -a "$CONFIG_FILE" "$backup_file"
	BACKUPS["$CONFIG_FILE"]="$backup_file"

	awk -v new_public_url="$public_url" '
		BEGIN {
			in_relay = 0
			replaced = 0
		}
		/^[[:space:]]*relay:[[:space:]]*$/ {
			print
			in_relay = 1
			next
		}
		in_relay && /^[[:space:]]*public_url:[[:space:]]*/ && !replaced {
			printf "  public_url: \"%s\"\n", new_public_url
			replaced = 1
			next
		}
		in_relay && /^[^[:space:]]/ {
			if (!replaced) {
				printf "  public_url: \"%s\"\n", new_public_url
				replaced = 1
			}
			in_relay = 0
		}
		{
			print
		}
		END {
			if (in_relay && !replaced) {
				printf "  public_url: \"%s\"\n", new_public_url
			}
		}
	' "$CONFIG_FILE" >"$tmp"
	run_root install -m 0644 "$tmp" "$CONFIG_FILE"
	rm -f "$tmp"
}

parse_peer_listen_from_config() {
	if [[ ! -f "$CONFIG_FILE" ]]; then
		printf '%s' "$DEFAULT_PEER_LISTEN"
		return
	fi
	local listen
	listen="$(awk '
		/^[[:space:]]*peer:[[:space:]]*$/ { in_peer=1; next }
		in_peer && /^[[:space:]]*listen:[[:space:]]*/ {
			sub(/^[[:space:]]*listen:[[:space:]]*/, "", $0)
			gsub(/"/, "", $0)
			gsub(/\047/, "", $0)
			print $0
			exit
		}
	' "$CONFIG_FILE")"
	if [[ -z "$listen" ]]; then
		printf '%s' "$DEFAULT_PEER_LISTEN"
		return
	fi
	printf '%s' "$listen"
}

parse_peer_public_port_from_config() {
	if [[ ! -f "$CONFIG_FILE" ]]; then
		printf '%s' "$DEFAULT_PEER_PUBLIC_PORT"
		return
	fi
	local port
	port="$(awk '
		/^[[:space:]]*peer:[[:space:]]*$/ { in_peer=1; next }
		in_peer && /^[[:space:]]*public_port:[[:space:]]*/ {
			sub(/^[[:space:]]*public_port:[[:space:]]*/, "", $0)
			gsub(/"/, "", $0)
			gsub(/\047/, "", $0)
			print $0
			exit
		}
	' "$CONFIG_FILE")"
	if [[ -z "$port" ]]; then
		printf '%s' "$DEFAULT_PEER_PUBLIC_PORT"
		return
	fi
	printf '%s' "$port"
}

restart_service() {
	run_root systemctl daemon-reload
	run_root systemctl stop "$SERVICE_NAME" 2>/dev/null || true
	check_listen_port_free "$(parse_listen_from_config)"
	check_listen_port_free "$(parse_peer_listen_from_config)"
	run_root systemctl enable --now "$SERVICE_NAME"
}

install_action() {
	CURRENT_ACTION="install"
	require_root_tools
	check_go_version
	PROXY_MODE="$(detect_proxy_mode)"
	ensure_proxy_domain
	if is_true "$DRY_RUN"; then
		print_install_plan
		return
	fi
	ensure_sudo
	run_root systemctl stop "$SERVICE_NAME" 2>/dev/null || true
	RELAY_LISTEN_RESOLVED="$(pick_free_listen "$(choose_listen)")"
	PEER_LISTEN_RESOLVED="$(pick_free_listen "$(choose_peer_listen)" "$RELAY_LISTEN_RESOLVED")"
	ensure_directories
	if [[ -n "$DOMAIN" ]]; then
		update_config_public_url "$(choose_public_url)"
	fi
	update_config_listen "$RELAY_LISTEN_RESOLVED"
	update_config_peer_listen "$PEER_LISTEN_RESOLVED"
	if [[ -f "$CONFIG_FILE" ]]; then
		if [[ "$RELAY_NAME_FLAG_SET" == false && "$RELAY_NAME_ENV_SET" == false ]]; then
			local existing_name
			existing_name="$(parse_relay_name_from_config)"
			if [[ -n "$existing_name" ]]; then
				RELAY_NAME="$existing_name"
			fi
		fi
		if [[ "$RELAY_DESCRIPTION_FLAG_SET" == false && "$RELAY_DESCRIPTION_ENV_SET" == false ]]; then
			local existing_description
			existing_description="$(parse_relay_description_from_config)"
			if [[ -n "$existing_description" ]]; then
				RELAY_DESCRIPTION="$existing_description"
			fi
		fi
	fi
	if [[ "$RELAY_NAME_FLAG_SET" == false && "$RELAY_NAME_ENV_SET" == false ]]; then
		prompt_required RELAY_NAME "Relay name" "$RELAY_NAME"
	fi
	if [[ "$RELAY_DESCRIPTION_FLAG_SET" == false && "$RELAY_DESCRIPTION_ENV_SET" == false ]]; then
		prompt_required RELAY_DESCRIPTION "Relay description" "$RELAY_DESCRIPTION"
	fi
	ensure_system_user
	build_binary
	write_config
	write_service
	configure_proxy \
		"$PROXY_MODE" \
		"$(parse_listen_from_config)" \
		"$(parse_peer_listen_from_config)" \
		"$(parse_peer_public_port_from_config)"
	restart_service
	wait_for_relay "$(parse_listen_from_config)"
	if [[ "$PROXY_MODE" != "none" ]]; then
		proxy_smoke_test "$PROXY_MODE" "$DOMAIN" "$(parse_peer_public_port_from_config)"
	fi
	log "install complete"
}

update_action() {
	CURRENT_ACTION="update"
	require_root_tools
	check_go_version
	if is_true "$DRY_RUN"; then
		print_update_plan
		return
	fi
	ensure_sudo
	update_source
	run_root systemctl stop "$SERVICE_NAME" 2>/dev/null || true
	RELAY_LISTEN_RESOLVED="$(pick_free_listen "$(parse_listen_from_config)")"
	PEER_LISTEN_RESOLVED="$(pick_free_listen "$(parse_peer_listen_from_config)" "$RELAY_LISTEN_RESOLVED")"
	if [[ -n "$DOMAIN" ]]; then
		update_config_public_url "$(choose_public_url)"
	fi
	update_config_listen "$RELAY_LISTEN_RESOLVED"
	update_config_peer_listen "$PEER_LISTEN_RESOLVED"
	if [[ "$RELAY_NAME_FLAG_SET" == false && "$RELAY_NAME_ENV_SET" == false ]]; then
		local existing_name
		existing_name="$(parse_relay_name_from_config)"
		if [[ -n "$existing_name" ]]; then
			RELAY_NAME="$existing_name"
		fi
	fi
	if [[ "$RELAY_DESCRIPTION_FLAG_SET" == false && "$RELAY_DESCRIPTION_ENV_SET" == false ]]; then
		local existing_description
		existing_description="$(parse_relay_description_from_config)"
		if [[ -n "$existing_description" ]]; then
			RELAY_DESCRIPTION="$existing_description"
		fi
	fi
	build_binary
	if [[ ! -f "$CONFIG_FILE" ]]; then
		warn "config file missing; run install to create ${CONFIG_FILE}"
	fi
	write_config
	run_root systemctl daemon-reload
	run_root systemctl restart "$SERVICE_NAME"
	wait_for_relay "$(parse_listen_from_config)"
	if detect_existing_proxy_config; then
		proxy_smoke_test "$PROXY_SMOKE_MODE" "$PROXY_SMOKE_DOMAIN" "$(parse_peer_public_port_from_config)"
	fi
	log "update complete"
}

test_action() {
	CURRENT_ACTION="test"
	require_root_tools
	check_go_version
	if is_true "$DRY_RUN"; then
		print_test_plan
		return
	fi
	ensure_sudo
	local listen
	listen="$(parse_listen_from_config)"
	wait_for_relay "$listen"
	run_root systemctl is-active --quiet "$SERVICE_NAME" || die "service ${SERVICE_NAME} is not active"
	if detect_existing_proxy_config; then
		proxy_smoke_test "$PROXY_SMOKE_MODE" "$PROXY_SMOKE_DOMAIN" "$(parse_peer_public_port_from_config)"
	fi
	log "install test passed"
}

parse_args() {
	while (($#)); do
		case "$1" in
			install|update|test)
				ACTION="$1"
				;;
			--domain)
				shift
				[[ $# -gt 0 ]] || die "--domain requires a value"
				DOMAIN="$1"
				;;
			--domain=*)
				DOMAIN="${1#*=}"
				;;
			--proxy)
				shift
				[[ $# -gt 0 ]] || die "--proxy requires a value"
				PROXY_MODE="$1"
				;;
			--proxy=*)
				PROXY_MODE="${1#*=}"
				;;
			--listen)
				shift
				[[ $# -gt 0 ]] || die "--listen requires a value"
				LISTEN_OVERRIDE="$1"
				;;
			--listen=*)
				LISTEN_OVERRIDE="${1#*=}"
				;;
			--name)
				shift
				[[ $# -gt 0 ]] || die "--name requires a value"
				RELAY_NAME="$1"
				RELAY_NAME_FLAG_SET=true
				;;
			--name=*)
				RELAY_NAME="${1#*=}"
				RELAY_NAME_FLAG_SET=true
				;;
			--description)
				shift
				[[ $# -gt 0 ]] || die "--description requires a value"
				RELAY_DESCRIPTION="$1"
				RELAY_DESCRIPTION_FLAG_SET=true
				;;
			--description=*)
				RELAY_DESCRIPTION="${1#*=}"
				RELAY_DESCRIPTION_FLAG_SET=true
				;;
			--non-interactive)
				NON_INTERACTIVE=true
				;;
			--dry-run)
				DRY_RUN=true
				;;
			-h|--help)
				usage
				exit 0
				;;
			*)
				die "unknown argument: $1"
				;;
		esac
		shift
	done
}

main() {
	parse_args "$@"

	case "$ACTION" in
		install) install_action ;;
		update) update_action ;;
		test) test_action ;;
		*)
			die "unknown action: ${ACTION}"
			;;
	esac
}

main "$@"
