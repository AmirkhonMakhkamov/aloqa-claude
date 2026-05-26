#!/usr/bin/env bash
set -euo pipefail

IFS='
	'

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
source "$script_dir/lib/env.sh"
source "$script_dir/lib/portable.sh"

cd "$ALOQA_LIVEKIT_OBSERVABILITY_ROOT"
mkdir -p .state/inventory

python3 - "$LK_API_KEY" "$LK_API_SECRET" templates/livekit.yaml.template .state/inventory/livekit.yaml <<'PY'
import pathlib
import sys

api_key, api_secret, src, dest = sys.argv[1:5]
text = pathlib.Path(src).read_text(encoding="utf-8")
text = text.replace("${LK_API_KEY}", api_key).replace("${LK_API_SECRET}", api_secret)
pathlib.Path(dest).write_text(text, encoding="utf-8")
PY

cat > .state/inventory/prometheus.inventory.yml <<'YAML'
global:
  scrape_interval: 15s
  evaluation_interval: 15s

scrape_configs:
  - job_name: livekit
    metrics_path: /metrics
    static_configs:
      - targets:
          - livekit:6789
YAML

cleanup() {
  docker compose -p "$INVENTORY_PROJECT" -f docker-compose.inventory.yml down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker compose -p "$INVENTORY_PROJECT" -f docker-compose.inventory.yml up -d

portable_timeout 45 bash -c '
  until docker compose -p "$1" -f docker-compose.inventory.yml exec -T livekit-cli sh -c "wget -qO- http://livekit:6789/metrics >/dev/null 2>&1 || curl -fsS http://livekit:6789/metrics >/dev/null"; do
    sleep 2
  done
' _ "$INVENTORY_PROJECT"

docker compose -p "$INVENTORY_PROJECT" -f docker-compose.inventory.yml exec -T livekit-cli sh -c 'lk room create inventory-probe >/dev/null 2>&1 || livekit-cli room create inventory-probe >/dev/null 2>&1 || true'
docker compose -p "$INVENTORY_PROJECT" -f docker-compose.inventory.yml exec -T livekit-cli sh -c 'lk load-test --room inventory-probe --publishers 1 --duration 10s >/dev/null 2>&1 || livekit-cli load-test --room inventory-probe --publishers 1 --duration 10s >/dev/null 2>&1 || true'

docker compose -p "$INVENTORY_PROJECT" -f docker-compose.inventory.yml exec -T livekit-cli sh -c 'wget -qO- http://livekit:6789/metrics 2>/dev/null || curl -fsS http://livekit:6789/metrics' > .state/metrics-text.prom

python3 - .state/metrics-text.prom .state/metrics-inventory.json <<'PY'
import json
import pathlib
import re
import sys

src, dest = sys.argv[1:3]
help_by_name = {}
type_by_name = {}
labels_by_name = {}
sample_re = re.compile(r"^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{([^}]*)\})?\s")

for line in pathlib.Path(src).read_text(encoding="utf-8").splitlines():
    if line.startswith("# HELP "):
        _, _, name, *rest = line.split(" ")
        help_by_name[name] = " ".join(rest)
        continue
    if line.startswith("# TYPE "):
        _, _, name, metric_type = line.split(" ", 3)
        type_by_name[name] = metric_type
        continue
    match = sample_re.match(line)
    if match is None:
        continue
    name = match.group(1)
    label_text = match.group(2) or ""
    labels = labels_by_name.setdefault(name, set())
    for label in re.findall(r"([a-zA-Z_][a-zA-Z0-9_]*)=", label_text):
        labels.add(label)

metrics = [
    {
        "name": name,
        "type": type_by_name.get(name, "untyped"),
        "help": help_by_name.get(name, ""),
        "labels": sorted(labels_by_name.get(name, set())),
    }
    for name in sorted(set(help_by_name) | set(type_by_name) | set(labels_by_name))
]

pathlib.Path(dest).write_text(json.dumps({"metrics": metrics}, indent=2) + "\n", encoding="utf-8")
livekit_count = sum(1 for metric in metrics if metric["name"].startswith("livekit_"))
print(f"{len(metrics)} metric families captured ({livekit_count} livekit_*) -> {dest}")
if livekit_count == 0:
    raise SystemExit("ERROR: no livekit_* metrics captured")
PY
