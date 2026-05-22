#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT_DIR/.env"
MIGRATIONS_DIR="$ROOT_DIR/migrations"

PREPARE_ONLY=0
MIGRATE_ONLY=0

case "${1:-}" in
	"")
		;;
	--prepare-only)
		PREPARE_ONLY=1
		;;
	--migrate-only)
		MIGRATE_ONLY=1
		;;
	*)
		printf '[dev-start] unknown argument: %s\n' "$1" >&2
		exit 2
		;;
esac

log() {
	printf '[dev-start] %s\n' "$*" >&2
}

die() {
	printf '[dev-start] ERROR: %s\n' "$*" >&2
	exit 1
}

read_env() {
	local key="$1"
	local default="${2:-}"
	local value=""

	if [[ -v "$key" ]]; then
		printf '%s' "${!key}"
		return
	fi

	if [[ -f "$ENV_FILE" ]]; then
		value="$(awk -v key="$key" '
			BEGIN { FS = "=" }
			$0 ~ "^[[:space:]]*" key "=" {
				sub("^[[:space:]]*" key "=", "")
				sub(/\r$/, "")
				print
				exit
			}
		' "$ENV_FILE")"
	fi

	if [[ -z "$value" ]]; then
		printf '%s' "$default"
		return
	fi

	if [[ "$value" == \"*\" && "$value" == *\" ]]; then
		value="${value:1:${#value}-2}"
	elif [[ "$value" == \'*\' && "$value" == *\' ]]; then
		value="${value:1:${#value}-2}"
	fi

	printf '%s' "$value"
}

require_command() {
	local command_name="$1"

	if ! command -v "$command_name" >/dev/null 2>&1; then
		die "$command_name is required"
	fi
}

compose() {
	if docker compose version >/dev/null 2>&1; then
		docker compose "$@"
	elif command -v docker-compose >/dev/null 2>&1; then
		docker-compose "$@"
	else
		die "Docker Compose is required"
	fi
}

wait_for_service() {
	local service="$1"
	local container_id
	local status

	container_id="$(compose ps -q "$service")"
	if [[ -z "$container_id" ]]; then
		die "compose service $service is not running"
	fi

	for _ in $(seq 1 60); do
		status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container_id" 2>/dev/null || true)"
		if [[ "$status" == "healthy" || "$status" == "running" ]]; then
			log "$service is $status"
			return
		fi
		sleep 1
	done

	compose logs --tail=80 "$service" >&2 || true
	die "$service did not become healthy in time"
}

psql_exec() {
	compose exec -T -e "PGPASSWORD=$DB_PASSWORD" postgres \
		psql -h localhost -U "$DB_USER" -d "$DB_NAME" -v ON_ERROR_STOP=1 "$@"
}

psql_scalar() {
	local sql="$1"

	psql_exec -XAtq -c "$sql"
}

is_api_running() {
	if ! command -v curl >/dev/null 2>&1; then
		return 1
	fi

	curl -fsS --max-time 2 "http://localhost:$SERVER_PORT/healthz" 2>/dev/null | grep -q '"status":"ok"'
}

ensure_api_port_available() {
	local pid
	local command_line

	if ! command -v lsof >/dev/null 2>&1; then
		return
	fi

	pid="$(lsof -nP -tiTCP:"$SERVER_PORT" -sTCP:LISTEN | head -n 1 || true)"
	if [[ -z "$pid" ]]; then
		return
	fi

	command_line="$(ps -p "$pid" -o command= 2>/dev/null || printf 'unknown')"
	die "port $SERVER_PORT is already used by pid $pid: $command_line"
}

record_migration() {
	local version="$1"
	local name="$2"

	psql_exec -Xq -v "version=$version" -v "name=$name" <<'SQL'
INSERT INTO schema_migrations (version, name)
VALUES (:version, :'name')
ON CONFLICT (version) DO UPDATE SET name = EXCLUDED.name;
SQL
}

record_current_migrations_as_applied() {
	local file
	local name
	local number
	local version

	for file in "$MIGRATIONS_DIR"/[0-9][0-9][0-9]_*.sql; do
		[[ -e "$file" ]] || die "no migration files found in $MIGRATIONS_DIR"
		name="$(basename "$file")"
		number="${name%%_*}"
		version=$((10#$number))
		record_migration "$version" "$name"
	done
}

apply_migrations() {
	local file
	local name
	local number
	local version
	local is_applied
	local applied_count
	local table_count
	local core_table_count

	[[ -d "$MIGRATIONS_DIR" ]] || die "migrations directory not found: $MIGRATIONS_DIR"

	psql_exec -Xq <<'SQL'
SET client_min_messages TO warning;
CREATE TABLE IF NOT EXISTS schema_migrations (
	version integer PRIMARY KEY,
	name text NOT NULL,
	applied_at timestamptz NOT NULL DEFAULT now()
);
RESET client_min_messages;
SQL

	applied_count="$(psql_scalar "SELECT count(*) FROM schema_migrations;")"
	table_count="$(psql_scalar "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name <> 'schema_migrations';")"

	if [[ "$applied_count" == "0" && "$table_count" != "0" ]]; then
		core_table_count="$(psql_scalar "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name IN ('users', 'workspaces', 'channels', 'messages', 'calls');")"
		if [[ "$core_table_count" != "5" ]]; then
			die "database has an untracked partial schema; reset the local Postgres volume before running migrations"
		fi

		log "existing untracked schema detected; recording current migrations as applied"
		record_current_migrations_as_applied
		return
	fi

	for file in "$MIGRATIONS_DIR"/[0-9][0-9][0-9]_*.sql; do
		[[ -e "$file" ]] || die "no migration files found in $MIGRATIONS_DIR"
		name="$(basename "$file")"
		number="${name%%_*}"
		version=$((10#$number))
		is_applied="$(psql_scalar "SELECT 1 FROM schema_migrations WHERE version = $version;")"

		if [[ "$is_applied" == "1" ]]; then
			continue
		fi

		log "applying migration $name"
		psql_exec -Xq -f - <"$file"
		record_migration "$version" "$name"
	done
}

main() {
	require_command docker
	if [[ "$PREPARE_ONLY" != "1" && "$MIGRATE_ONLY" != "1" ]]; then
		require_command go
	fi

	DB_USER="$(read_env DB_USER aloqa)"
	DB_PASSWORD="$(read_env DB_PASSWORD aloqa)"
	DB_NAME="$(read_env DB_NAME aloqa)"
	SERVER_PORT="$(read_env SERVER_PORT 8080)"

	export DB_USER
	export DB_PASSWORD
	export DB_NAME
	export SERVER_PORT

	cd "$ROOT_DIR"

	log "starting Docker dependencies"
	compose up -d postgres redis nats
	wait_for_service postgres
	wait_for_service redis
	wait_for_service nats

	log "applying pending migrations"
	apply_migrations

	if [[ "$PREPARE_ONLY" == "1" || "$MIGRATE_ONLY" == "1" ]]; then
		log "dependencies and migrations are ready"
		return
	fi

	if is_api_running; then
		log "Go API is already running on http://localhost:$SERVER_PORT"
		return
	fi

	ensure_api_port_available

	log "starting Go API on http://localhost:$SERVER_PORT"
	exec go run ./cmd/server
}

main "$@"
