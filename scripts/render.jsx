/**
 * kubespec render — JSX/React variant
 *
 * Generates self-contained HTML widgets from a CRD YAML file using React
 * components and ReactDOMServer.renderToStaticMarkup for server-side rendering.
 *
 * Entirely standalone: no dependency on the kubespec.dev project structure.
 * Any valid CRD YAML file (apiVersion: apiextensions.k8s.io/v1) can be used.
 *
 * Usage:
 *   npx tsx scripts/render.jsx <crd.yaml> [--output file.html] [--version v1]
 *
 * Examples:
 *   npx tsx scripts/render.jsx my-crd.yaml
 *   npx tsx scripts/render.jsx cert-manager.yaml --output cert-manager.html
 *   npx tsx scripts/render.jsx gateway-api-crds.yaml --version v1 --output httproute.html
 *
 * Requires: Node.js ≥ 18, and the `yaml`, `react`, `react-dom` npm packages.
 * (All three are already present in this project's package.json.)
 */

import { readFile, writeFile } from "node:fs/promises";
import { parseAllDocuments } from "yaml";
import React from "react";
import { renderToStaticMarkup } from "react-dom/server";

// ---------------------------------------------------------------------------
// Schema conversion (same logic as render.ts)
// ---------------------------------------------------------------------------

function toPropertyMap(schema, parentRequired = []) {
  const def = {
    description: schema.description ?? "",
    properties: {},
  };
  for (const [name, prop] of Object.entries(schema.properties ?? {})) {
    const isArray = prop.type === "array";
    const required = parentRequired.includes(name);
    let type = prop.type ?? "";
    let definition;
    if (isArray) {
      const items = prop.items ?? {};
      type = `${items.type ?? "object"}[]`;
      if (items.properties) definition = toPropertyMap(items, items.required ?? []);
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
// CSS (embedded inline in every widget)
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// React components
// ---------------------------------------------------------------------------

function typeClass(type, hasChildren) {
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

function SchemaTree({ def, scope, level, path }) {
  const entries = Object.entries(def.properties ?? {});
  if (entries.length === 0) return null;

  return (
    <ul className={`ks-tree${level > 0 ? " ks-nested" : ""}`}>
      {entries.map(([name, prop]) => {
        const propPath = `${path}.${name}`;
        const hasChildren = Object.keys(prop.definition?.properties ?? {}).length > 0;
        const isRequired = prop.required || (scope === "Namespaced" && propPath === ".metadata.namespace");
        const typeCls = typeClass(prop.type, hasChildren);

        if (hasChildren || prop.description) {
          return (
            <li key={name} className="ks-row">
              <details open={level === 0 && hasChildren ? true : undefined}>
                <summary className="ks-summary">
                  {isRequired && <span className="ks-required" title="Required">*</span>}
                  <span className="ks-name">{name}</span>
                  <span className={`ks-type ${typeCls}`}>{prop.type}</span>
                </summary>
                {prop.description && <pre className="ks-desc">{prop.description}</pre>}
                {hasChildren && prop.definition && (
                  <SchemaTree def={prop.definition} scope={scope} level={level + 1} path={propPath} />
                )}
              </details>
            </li>
          );
        }

        return (
          <li key={name} className="ks-row ks-leaf">
            <span className="ks-leaf-line">
              {isRequired && <span className="ks-required" title="Required">*</span>}
              <span className="ks-name">{name}</span>
              <span className={`ks-type ${typeCls}`}>{prop.type}</span>
            </span>
          </li>
        );
      })}
    </ul>
  );
}

function Widget({ kind, group, version, scope, def }) {
  const apiVersion = group ? `${group}/${version}` : version;
  const scopeLabel = scope === "Namespaced" ? "Namespaced Resource" : "Cluster-scoped Resource";
  const canonicalUrl = `https://kubespec.dev/${[group, version, kind].filter(Boolean).join("/")}`;

  return (
    <div className="ks-schema">
      <style dangerouslySetInnerHTML={{ __html: CSS }} />
      <div className="ks-header">
        <div className="ks-apiversion">{apiVersion}</div>
        <div className="ks-scope">{scopeLabel}</div>
        <h2 className="ks-kind">{kind}</h2>
        {def.description && <pre className="ks-resource-desc">{def.description}</pre>}
      </div>
      <SchemaTree def={def} scope={scope} level={0} path="" />
      <div className="ks-footer">
        View full docs on{" "}
        <a href={canonicalUrl} target="_blank" rel="noopener">
          kubespec.dev ↗
        </a>
      </div>
    </div>
  );
}

// ---------------------------------------------------------------------------
// CLI args
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);
if (args.length === 0 || args[0] === "--help") {
  console.error(
    "Usage: npx tsx scripts/render.jsx <crd.yaml> [--output file.html] [--version v1]\n\n" +
    "Examples:\n" +
    "  npx tsx scripts/render.jsx my-crd.yaml\n" +
    "  npx tsx scripts/render.jsx cert-manager.yaml --output cert-manager.html\n" +
    "  npx tsx scripts/render.jsx gateway-crds.yaml --version v1 --output httproute.html\n"
  );
  process.exit(1);
}

const inputFile = args[0];
const outputFlag = args.indexOf("--output");
const outputFile = outputFlag !== -1 ? args[outputFlag + 1] : null;
const versionFlag = args.indexOf("--version");
const versionFilter = versionFlag !== -1 ? args[versionFlag + 1] : null;

// ---------------------------------------------------------------------------
// Parse CRD YAML and render
// ---------------------------------------------------------------------------

const yamlText = await readFile(inputFile, "utf-8").catch((e) => {
  console.error(`Error reading ${inputFile}: ${e.message}`);
  process.exit(1);
});
const docs = parseAllDocuments(yamlText);
const widgets = [];

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

  const group = crd.spec?.group ?? "";
  const kind = crd.spec?.names?.kind ?? "";
  const scope = crd.spec?.scope ?? "Cluster";

  for (const ver of (crd.spec?.versions ?? [])) {
    if (versionFilter && ver.name !== versionFilter) continue;

    const schema = ver.schema?.openAPIV3Schema;
    if (!schema) {
      console.error(`Warning: no openAPIV3Schema for ${kind} version ${ver.name}, skipping`);
      continue;
    }

    const def = toPropertyMap(schema, schema.required ?? []);

    // renderToStaticMarkup produces clean HTML without React internals
    const widgetHtml = renderToStaticMarkup(
      <Widget kind={kind} group={group} version={ver.name} scope={scope} def={def} />
    );
    widgets.push(`<!-- kubespec widget: ${kind} (${group ? `${group}/${ver.name}` : ver.name}) -->\n${widgetHtml}`);
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
