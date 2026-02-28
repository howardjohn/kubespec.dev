/**
 * kubespec render — generate self-contained HTML widgets from a CRD YAML file.
 *
 * Entirely standalone: no dependency on the kubespec.dev project structure.
 * Any valid CRD YAML file (apiVersion: apiextensions.k8s.io/v1) can be used.
 *
 * Usage:
 *   npx tsx scripts/render.ts <crd.yaml> [--output file.html] [--version v1]
 *
 * Examples:
 *   npx tsx scripts/render.ts my-crds.yaml
 *   npx tsx scripts/render.ts cert-manager.yaml --output cert-manager.html
 *   npx tsx scripts/render.ts gateway-api-crds.yaml --version v1 --output httproute.html
 *
 * If the YAML contains multiple CRDs (multi-document YAML), each one is rendered
 * and concatenated into the output.  Use --version to restrict to a single schema
 * version within each CRD.
 */

import { readFile, writeFile } from "node:fs/promises";
import { parseAllDocuments } from "yaml";

// ---------------------------------------------------------------------------
// Types (inline — no @lib/kube dependency)
// ---------------------------------------------------------------------------

type OpenAPIV3Schema = {
  description?: string;
  type?: string;
  properties?: Record<string, OpenAPIV3Schema>;
  items?: OpenAPIV3Schema;
  required?: string[];
  "x-kubernetes-preserve-unknown-fields"?: boolean;
};

type CRDVersion = {
  name: string;
  schema?: {
    openAPIV3Schema?: OpenAPIV3Schema;
  };
};

type PropertyDef = {
  description: string;
  type: string;
  required: boolean;
  isArray: boolean;
  definition?: PropertyMap;
};

type PropertyMap = {
  description: string;
  properties: Record<string, PropertyDef>;
};

// ---------------------------------------------------------------------------
// Schema conversion
// ---------------------------------------------------------------------------

function toPropertyMap(schema: OpenAPIV3Schema, parentRequired: string[] = []): PropertyMap {
  const def: PropertyMap = {
    description: schema.description ?? "",
    properties: {},
  };

  for (const [name, prop] of Object.entries(schema.properties ?? {})) {
    const isArray = prop.type === "array";
    const required = parentRequired.includes(name);

    let type = prop.type ?? "";
    let definition: PropertyMap | undefined;

    if (isArray) {
      const items = prop.items ?? {};
      const itemType = items.type ?? "object";
      type = `${itemType}[]`;
      if (items.properties) {
        definition = toPropertyMap(items, items.required ?? []);
      }
    } else if (prop.properties) {
      definition = toPropertyMap(prop, prop.required ?? []);
    } else if (prop["x-kubernetes-preserve-unknown-fields"]) {
      type = type || "object";
    }

    def.properties[name] = { description: prop.description ?? "", type, required, isArray, definition };
  }

  return def;
}

// ---------------------------------------------------------------------------
// HTML rendering
// ---------------------------------------------------------------------------

function esc(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function typeClass(type: string, hasChildren: boolean): string {
  if (hasChildren) return "ks-type-complex";
  switch (type.replace(/\[\]$/, "")) {
    case "string":  return "ks-type-string";
    case "boolean": return "ks-type-boolean";
    case "integer":
    case "number":  return "ks-type-integer";
    case "object":  return "ks-type-object";
    default:        return "ks-type-other";
  }
}

function renderTree(def: PropertyMap, scope: string, level: number, path: string): string {
  const entries = Object.entries(def.properties ?? {});
  if (entries.length === 0) return "";

  const nestedClass = level > 0 ? " ks-nested" : "";
  const items = entries.map(([name, prop]) => {
    const propPath = `${path}.${name}`;
    const hasChildren = Object.keys(prop.definition?.properties ?? {}).length > 0;
    const isRequired = prop.required || (scope === "Namespaced" && propPath === ".metadata.namespace");

    const reqMark = isRequired ? `<span class="ks-required" title="Required">*</span>` : "";
    const typeCls = typeClass(prop.type, hasChildren);
    const typeHtml = `<span class="ks-type ${typeCls}">${esc(prop.type)}</span>`;
    const descHtml = prop.description ? `<pre class="ks-desc">${esc(prop.description)}</pre>` : "";

    if (hasChildren || prop.description) {
      const openAttr = level === 0 && hasChildren ? " open" : "";
      const childrenHtml = hasChildren && prop.definition ? renderTree(prop.definition, scope, level + 1, propPath) : "";
      return (
        `<li class="ks-row">` +
        `<details${openAttr}>` +
        `<summary class="ks-summary">${reqMark}<span class="ks-name">${esc(name)}</span>${typeHtml}</summary>` +
        descHtml +
        childrenHtml +
        `</details></li>`
      );
    }

    return (
      `<li class="ks-row ks-leaf">` +
      `<span class="ks-leaf-line">${reqMark}<span class="ks-name">${esc(name)}</span>${typeHtml}</span>` +
      `</li>`
    );
  }).join("\n");

  return `<ul class="ks-tree${nestedClass}">\n${items}\n</ul>`;
}

const CSS = `
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
`.trim();

function renderWidget(
  kind: string,
  group: string,
  version: string,
  scope: string,
  def: PropertyMap
): string {
  const apiVersion = group ? `${group}/${version}` : version;
  const scopeLabel = scope === "Namespaced" ? "Namespaced Resource" : "Cluster-scoped Resource";
  const canonicalUrl = `https://kubespec.dev/${[group, version, kind].filter(Boolean).join("/")}`;

  return (
    `<!-- kubespec widget: ${esc(kind)} (${esc(apiVersion)}) -->
<div class="ks-schema">
<style>
${CSS}
</style>
<div class="ks-header">
  <div class="ks-apiversion">${esc(apiVersion)}</div>
  <div class="ks-scope">${esc(scopeLabel)}</div>
  <h2 class="ks-kind">${esc(kind)}</h2>
  ${def.description ? `<pre class="ks-resource-desc">${esc(def.description)}</pre>` : ""}
</div>
${renderTree(def, scope, 0, "")}
<div class="ks-footer">
  View full docs on <a href="${esc(canonicalUrl)}" target="_blank" rel="noopener">kubespec.dev ↗</a>
</div>
</div>`
  );
}

// ---------------------------------------------------------------------------
// CLI args
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);
if (args.length === 0 || args[0] === "--help") {
  console.error(
    "Usage: npx tsx scripts/render.ts <crd.yaml> [--output file.html] [--version v1]\n\n" +
    "Examples:\n" +
    "  npx tsx scripts/render.ts my-crd.yaml\n" +
    "  npx tsx scripts/render.ts cert-manager.yaml --output cert-manager.html\n" +
    "  npx tsx scripts/render.ts gateway-crds.yaml --version v1 --output httproute.html\n"
  );
  process.exit(1);
}

const inputFile = args[0];
const outputFlag = args.indexOf("--output");
const outputFile = outputFlag !== -1 ? args[outputFlag + 1] : null;
const versionFlag = args.indexOf("--version");
const versionFilter = versionFlag !== -1 ? args[versionFlag + 1] : null;

// ---------------------------------------------------------------------------
// Parse CRD YAML
// ---------------------------------------------------------------------------

const yamlText = await readFile(inputFile, "utf-8").catch((e: NodeJS.ErrnoException): never => {
  console.error(`Error reading ${inputFile}: ${e.message}`);
  process.exit(1);
});
const docs = parseAllDocuments(yamlText);

const widgets: string[] = [];

for (const doc of docs) {
  if (doc.errors.length > 0) {
    console.error(`Warning: YAML parse error — ${doc.errors.map((e) => e.message).join(", ")}`);
    continue;
  }

  const crd = doc.toJSON();
  // Accept both v1 and v1beta1; v1beta1 has the same spec shape for our purposes
  if (!crd || crd.kind !== "CustomResourceDefinition" || !crd.apiVersion?.startsWith("apiextensions.k8s.io/")) {
    continue;
  }

  const group: string = crd.spec?.group ?? "";
  const kind: string = crd.spec?.names?.kind ?? "";
  const scope: string = crd.spec?.scope ?? "Cluster";
  const versions: CRDVersion[] = crd.spec?.versions ?? [];

  for (const ver of versions) {
    if (versionFilter && ver.name !== versionFilter) continue;

    const schema: OpenAPIV3Schema | undefined = ver.schema?.openAPIV3Schema;
    if (!schema) {
      console.error(`Warning: no openAPIV3Schema for ${kind} version ${ver.name}, skipping`);
      continue;
    }

    const def = toPropertyMap(schema, schema.required ?? []);
    widgets.push(renderWidget(kind, group, ver.name, scope, def));
  }
}

if (widgets.length === 0) {
  console.error(
    `No CRDs found in ${inputFile}.\n` +
    "Make sure the file contains at least one document with:\n" +
    "  apiVersion: apiextensions.k8s.io/v1\n" +
    "  kind: CustomResourceDefinition\n"
  );
  process.exit(1);
}

const html = widgets.join("\n\n");

if (outputFile) {
  await writeFile(outputFile, html, "utf-8");
  console.log(`Written to ${outputFile} (${widgets.length} widget${widgets.length !== 1 ? "s" : ""})`);
} else {
  process.stdout.write(html + "\n");
}

