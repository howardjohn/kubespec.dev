#!/usr/bin/env python3
"""
kubespec render — generate self-contained HTML widgets from a CRD YAML file.

Entirely standalone: no dependency on the kubespec.dev project structure.
Any valid CRD YAML file (apiVersion: apiextensions.k8s.io/v1) can be used.

Usage:
  python scripts/render.py <crd.yaml> [--output file.html] [--version v1]

Examples:
  python scripts/render.py my-crd.yaml
  python scripts/render.py cert-manager.yaml --output cert-manager.html
  python scripts/render.py gateway-api-crds.yaml --version v1 --output httproute.html

If the YAML contains multiple CRDs (multi-document YAML), each one is rendered
and concatenated into the output.  Use --version to restrict to a single schema
version within each CRD.

Requires:
  pip install pyyaml
"""

import sys
import argparse

try:
    import yaml
except ImportError:
    print("Error: PyYAML is required. Install with: pip install pyyaml", file=sys.stderr)
    sys.exit(1)

# ---------------------------------------------------------------------------
# Schema conversion
# ---------------------------------------------------------------------------

def to_property_map(schema: dict, parent_required: list = None) -> dict:
    if parent_required is None:
        parent_required = []

    def_map = {
        "description": schema.get("description") or "",
        "properties": {},
    }

    for name, prop in (schema.get("properties") or {}).items():
        is_array = prop.get("type") == "array"
        required = name in parent_required
        prop_type = prop.get("type") or ""
        definition = None

        if is_array:
            items = prop.get("items") or {}
            prop_type = (items.get("type") or "object") + "[]"
            if items.get("properties"):
                definition = to_property_map(items, items.get("required") or [])
        elif prop.get("properties"):
            definition = to_property_map(prop, prop.get("required") or [])
        elif prop.get("x-kubernetes-preserve-unknown-fields"):
            prop_type = prop_type or "object"

        def_map["properties"][name] = {
            "description": prop.get("description") or "",
            "type": prop_type,
            "required": required,
            "is_array": is_array,
            "definition": definition,
        }

    return def_map

# ---------------------------------------------------------------------------
# HTML rendering
# ---------------------------------------------------------------------------

def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;").replace('"', "&quot;")


def type_class(prop_type: str, has_children: bool) -> str:
    if has_children:
        return "ks-type-complex"
    base = prop_type[:-2] if prop_type.endswith("[]") else prop_type
    return {
        "string":  "ks-type-string",
        "boolean": "ks-type-boolean",
        "integer": "ks-type-integer",
        "number":  "ks-type-integer",
        "object":  "ks-type-object",
    }.get(base, "ks-type-other")


def render_tree(def_map: dict, scope: str, level: int, path: str) -> str:
    props = def_map.get("properties") or {}
    if not props:
        return ""

    nested_class = " ks-nested" if level > 0 else ""
    items = []

    for name, prop in props.items():
        prop_path = f"{path}.{name}"
        has_children = bool((prop.get("definition") or {}).get("properties"))
        is_required = prop.get("required") or (scope == "Namespaced" and prop_path == ".metadata.namespace")

        req_mark = '<span class="ks-required" title="Required">*</span>' if is_required else ""
        type_cls = type_class(prop.get("type") or "", has_children)
        type_html = f'<span class="ks-type {type_cls}">{esc(prop.get("type") or "")}</span>'
        desc = prop.get("description") or ""
        desc_html = f'<pre class="ks-desc">{esc(desc)}</pre>' if desc else ""

        if has_children or desc:
            open_attr = " open" if level == 0 and has_children else ""
            children_html = (
                render_tree(prop["definition"], scope, level + 1, prop_path)
                if has_children and prop.get("definition") else ""
            )
            items.append(
                f'<li class="ks-row">'
                f'<details{open_attr}>'
                f'<summary class="ks-summary">{req_mark}<span class="ks-name">{esc(name)}</span>{type_html}</summary>'
                f'{desc_html}'
                f'{children_html}'
                f'</details></li>'
            )
        else:
            items.append(
                f'<li class="ks-row ks-leaf">'
                f'<span class="ks-leaf-line">{req_mark}<span class="ks-name">{esc(name)}</span>{type_html}</span>'
                f'</li>'
            )

    return f'<ul class="ks-tree{nested_class}">\n' + "\n".join(items) + "\n</ul>"


CSS = """
.ks-schema {
  font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  font-size: 14px;
  line-height: 1.5;
  color: #111827;
  max-width: 56rem;
}
.ks-header { margin-bottom: 1rem; }
.ks-apiversion {
  font-size: 0.875rem;
  font-weight: 600;
  color: #6b7280;
  margin-bottom: 0.25rem;
}
.ks-scope {
  display: inline-flex;
  align-items: center;
  font-size: 0.75rem;
  color: #6b7280;
  border: 1px solid #d1d5db;
  border-radius: 0.375rem;
  padding: 0.125rem 0.5rem;
  margin-bottom: 0.375rem;
}
.ks-kind {
  font-size: 1.875rem;
  font-weight: 700;
  margin: 0.25rem 0 0;
  line-height: 1.2;
}
.ks-resource-desc {
  font-size: 0.875rem;
  color: #6b7280;
  margin: 0.5rem 0 1rem;
  white-space: pre-wrap;
  max-width: 48rem;
}
.ks-tree {
  list-style: none;
  margin: 0;
  padding: 0;
  font-family: ui-monospace, "Cascadia Code", "Source Code Pro", Menlo, monospace;
  font-size: 0.8125rem;
}
.ks-tree.ks-nested {
  margin-left: 1rem;
  padding-left: 0.625rem;
  border-left: 2px solid #e5e7eb;
}
.ks-row { font-weight: 600; }
.ks-row + .ks-row { margin-top: 0.125rem; }
.ks-summary {
  display: inline-flex;
  align-items: baseline;
  gap: 0.25rem;
  padding: 0.125rem 0.375rem;
  border-radius: 0.25rem;
  cursor: pointer;
  list-style: none;
  user-select: none;
}
.ks-summary::-webkit-details-marker { display: none; }
.ks-summary::marker { display: none; }
.ks-summary:hover { background: #f3f4f6; }
.ks-leaf-line {
  display: inline-flex;
  align-items: baseline;
  gap: 0.25rem;
  padding: 0.125rem 0.375rem;
}
.ks-name { color: #111827; }
.ks-required {
  color: #dc2626;
  font-size: 0.6875rem;
  margin-right: 0.125rem;
  font-family: system-ui, sans-serif;
}
.ks-type { font-weight: 400; }
.ks-type-string  { color: #c2410c; }
.ks-type-boolean { color: #1d4ed8; }
.ks-type-integer { color: #0369a1; }
.ks-type-object  { color: #7c3aed; }
.ks-type-complex { color: #9d174d; }
.ks-type-other   { color: #065f46; }
.ks-desc {
  margin: 0.25rem 0 0.5rem 0.375rem;
  font-size: 0.75rem;
  font-weight: 400;
  font-family: system-ui, sans-serif;
  white-space: pre-wrap;
  max-width: 48rem;
  color: #374151;
}
.ks-footer {
  margin-top: 1rem;
  padding-top: 0.625rem;
  border-top: 1px solid #e5e7eb;
  font-size: 0.75rem;
  color: #6b7280;
}
.ks-footer a { color: #2563eb; text-decoration: none; }
.ks-footer a:hover { text-decoration: underline; }
""".strip()


def render_widget(kind: str, group: str, version: str, scope: str, def_map: dict) -> str:
    api_version = f"{group}/{version}" if group else version
    scope_label = "Namespaced Resource" if scope == "Namespaced" else "Cluster-scoped Resource"
    canonical_url = "https://kubespec.dev/" + "/".join(p for p in [group, version, kind] if p)

    description = def_map.get("description") or ""
    desc_html = f'  <pre class="ks-resource-desc">{esc(description)}</pre>' if description else ""
    tree_html = render_tree(def_map, scope, 0, "")

    return (
        f'<!-- kubespec widget: {esc(kind)} ({esc(api_version)}) -->\n'
        f'<div class="ks-schema">\n'
        f'<style>\n{CSS}\n</style>\n'
        f'<div class="ks-header">\n'
        f'  <div class="ks-apiversion">{esc(api_version)}</div>\n'
        f'  <div class="ks-scope">{esc(scope_label)}</div>\n'
        f'  <h2 class="ks-kind">{esc(kind)}</h2>\n'
        f'{desc_html}\n'
        f'</div>\n'
        f'{tree_html}\n'
        f'<div class="ks-footer">\n'
        f'  View full docs on <a href="{esc(canonical_url)}" target="_blank" rel="noopener">kubespec.dev ↗</a>\n'
        f'</div>\n'
        f'</div>'
    )

# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------

def main():
    parser = argparse.ArgumentParser(
        prog="render.py",
        description="Generate self-contained HTML widgets from a CRD YAML file.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "examples:\n"
            "  python render.py my-crd.yaml\n"
            "  python render.py cert-manager.yaml --output cert-manager.html\n"
            "  python render.py gateway-crds.yaml --version v1 --output httproute.html"
        ),
    )
    parser.add_argument("crd_yaml", help="Path to a CRD YAML file")
    parser.add_argument("--output", "-o", metavar="FILE", help="Write output to FILE instead of stdout")
    parser.add_argument("--version", metavar="VERSION", help="Only render a specific schema version (e.g. v1)")
    args = parser.parse_args()

    try:
        with open(args.crd_yaml) as f:
            content = f.read()
    except OSError as e:
        print(f"Error reading {args.crd_yaml}: {e}", file=sys.stderr)
        sys.exit(1)

    widgets = []

    for doc in yaml.safe_load_all(content):
        if not doc:
            continue
        if doc.get("kind") != "CustomResourceDefinition":
            continue
        api_ver = doc.get("apiVersion") or ""
        if not api_ver.startswith("apiextensions.k8s.io/"):
            continue

        spec = doc.get("spec") or {}
        group = spec.get("group") or ""
        kind = (spec.get("names") or {}).get("kind") or ""
        scope = spec.get("scope") or "Cluster"

        for ver in (spec.get("versions") or []):
            ver_name = ver.get("name") or ""
            if args.version and ver_name != args.version:
                continue

            schema = (ver.get("schema") or {}).get("openAPIV3Schema")
            if not schema:
                print(f"Warning: no openAPIV3Schema for {kind} version {ver_name}, skipping", file=sys.stderr)
                continue

            def_map = to_property_map(schema, schema.get("required") or [])
            widgets.append(render_widget(kind, group, ver_name, scope, def_map))

    if not widgets:
        print(
            f"No CRDs found in {args.crd_yaml}.\n"
            "Make sure the file contains at least one document with:\n"
            "  apiVersion: apiextensions.k8s.io/v1\n"
            "  kind: CustomResourceDefinition",
            file=sys.stderr,
        )
        sys.exit(1)

    html_output = "\n\n".join(widgets)

    if args.output:
        try:
            with open(args.output, "w") as f:
                f.write(html_output)
            n = len(widgets)
            print(f"Written to {args.output} ({n} widget{'s' if n != 1 else ''})")
        except OSError as e:
            print(f"Error writing {args.output}: {e}", file=sys.stderr)
            sys.exit(1)
    else:
        print(html_output)


if __name__ == "__main__":
    main()
