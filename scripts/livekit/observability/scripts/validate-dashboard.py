#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys
from typing import Any


BUILTINS = {
    "abs",
    "absent",
    "absent_over_time",
    "acos",
    "acosh",
    "and",
    "asin",
    "asinh",
    "atan",
    "atanh",
    "avg",
    "avg_over_time",
    "bool",
    "bottomk",
    "by",
    "ceil",
    "changes",
    "clamp",
    "clamp_max",
    "clamp_min",
    "cos",
    "cosh",
    "count",
    "count_over_time",
    "count_values",
    "day_of_month",
    "day_of_week",
    "day_of_year",
    "days_in_month",
    "deg",
    "delta",
    "deriv",
    "exp",
    "floor",
    "group",
    "group_left",
    "group_right",
    "histogram_avg",
    "histogram_count",
    "histogram_fraction",
    "histogram_quantile",
    "histogram_stddev",
    "histogram_stdvar",
    "histogram_sum",
    "hour",
    "idelta",
    "ignoring",
    "increase",
    "irate",
    "label_join",
    "label_replace",
    "last_over_time",
    "ln",
    "log10",
    "log2",
    "max",
    "max_over_time",
    "min",
    "min_over_time",
    "minute",
    "month",
    "offset",
    "on",
    "or",
    "pi",
    "predict_linear",
    "present_over_time",
    "quantile",
    "quantile_over_time",
    "rad",
    "rate",
    "resets",
    "round",
    "scalar",
    "sgn",
    "sin",
    "sinh",
    "sort",
    "sort_by_label",
    "sort_by_label_desc",
    "sort_desc",
    "sqrt",
    "stddev",
    "stddev_over_time",
    "stdvar",
    "stdvar_over_time",
    "sum",
    "sum_over_time",
    "tan",
    "tanh",
    "time",
    "timestamp",
    "topk",
    "unless",
    "vector",
    "without",
    "year",
}


def walk_panels(value: Any) -> list[dict[str, Any]]:
    if not isinstance(value, list):
        return []
    panels: list[dict[str, Any]] = []
    for item in value:
        if not isinstance(item, dict):
            continue
        panels.append(item)
        panels.extend(walk_panels(item.get("panels")))
    return panels


def expressions(dashboard: dict[str, Any]) -> list[tuple[str, str]]:
    result: list[tuple[str, str]] = []
    for panel in walk_panels(dashboard.get("panels")):
        title = str(panel.get("title", "untitled"))
        targets = panel.get("targets")
        if not isinstance(targets, list):
            continue
        for target in targets:
            if not isinstance(target, dict):
                continue
            expr = target.get("expr")
            if isinstance(expr, str) and expr.strip():
                result.append((title, expr))
    return result


def tokens_for_expr(expr: str) -> set[str]:
    without_selectors = re.sub(r"\{[^}]*\}", " ", expr)
    without_strings = re.sub(r'"(?:\\.|[^"])*"', " ", without_selectors)
    without_durations = re.sub(r"\b\d+[smhdwy]\b", " ", without_strings)
    without_numbers = re.sub(r"\b\d+(?:\.\d+)?\b", " ", without_durations)
    return set(re.findall(r"[a-zA-Z_:][a-zA-Z0-9_:]*", without_numbers))


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--dashboard",
        default="grafana/dashboards/livekit/livekit-fallback.json",
    )
    parser.add_argument("--inventory", default=".state/metrics-inventory.json")
    args = parser.parse_args()

    try:
        dashboard = json.loads(pathlib.Path(args.dashboard).read_text(encoding="utf-8"))
        inventory = json.loads(pathlib.Path(args.inventory).read_text(encoding="utf-8"))
    except OSError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    except json.JSONDecodeError as exc:
        print(f"ERROR: invalid JSON: {exc}", file=sys.stderr)
        return 2

    metrics = {
        metric.get("name")
        for metric in inventory.get("metrics", [])
        if isinstance(metric, dict) and isinstance(metric.get("name"), str)
    }

    failures: list[str] = []
    for title, expr in expressions(dashboard):
        unknown = sorted(token for token in tokens_for_expr(expr) if token not in BUILTINS and token not in metrics)
        if unknown:
            failures.append(f"{title}: {expr} -> {', '.join(unknown)}")

    if failures:
        print("Unrecognised PromQL identifiers:", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1

    print(f"Dashboard OK: {len(expressions(dashboard))} expressions validated against {len(metrics)} metrics")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
