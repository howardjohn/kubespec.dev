/**
 * kubespec render — generate a self-contained HTML widget for a single API resource.
 *
 * Usage:
 *   npm run render -- <project> <gvkRef> [--output file.html]
 *
 * Examples:
 *   npm run render -- kubernetes v1/Pod
 *   npm run render -- kubernetes apps/v1/Deployment --output deployment.html
 *   npm run render -- cert-manager cert-manager.io/v1/Certificate
 *
 * The output is a self-contained HTML fragment (with embedded <style> and no
 * external dependencies) that can be pasted directly into any Hugo template,
 * shortcode, or page without an iframe.
 */

import { writeFile } from "node:fs/promises";
import { readdir } from "node:fs/promises";
import semver from "semver";
import { compareVersions, listAllBuiltInResources } from "@lib/kube/kubernetes";
import { listAllCRDs } from "@lib/kube/crds";
import { compareCRDVersion } from "@lib/kube/compare";
import ALL_PROJECTS from "@lib/kube/projects";
import type { ResourceDefinition, GVK, Project, Resource } from "@lib/kube/types";

// ---------------------------------------------------------------------------
// Inline helpers from @lib/kube/index (avoids import.meta.glob in metadata.ts)
// ---------------------------------------------------------------------------

async function listProjects(): Promise<Project[]> {
  const projects: Project[] = [];
  for (const project of ALL_PROJECTS) {
    const tags = new Set<string>();
    const baseDir = `./content/projects/${project.slug}`;
    for (const file of await readdir(baseDir, { recursive: true })) {
      const tag = file.substring(0, file.indexOf("/"));
      if (tag) tags.add(tag);
    }
    projects.push({
      name: project.name,
      slug: project.slug,
      logo: project.logo,
      tags:
        project.slug === "kubernetes"
          ? [...tags].sort(compareVersions).reverse()
          : semver.rsort([...tags]),
    });
  }
  return projects;
}

async function findProject(slug: string): Promise<Project> {
  const projects = await listProjects();
  const project = projects.find((p) => p.slug === slug);
  if (!project) throw new Error(`Project not found: ${slug}`);
  return project;
}

const resourcesCache = new Map<string, Resource[]>();

async function listAllResources(slug: string, tag: string): Promise<Resource[]> {
  const cacheKey = `${slug}/${tag}`;
  const cached = resourcesCache.get(cacheKey);
  if (cached) return cached;

  const resources =
    slug === "kubernetes"
      ? await listAllBuiltInResources(tag)
      : await listAllCRDs(slug, tag);

  const latestByKind = new Map<string, Resource>();
  for (const resource of resources) {
    const key = `${resource.gvk.group}/${resource.gvk.version.substring(0, 2)}/${resource.gvk.kind}`;
    const existing = latestByKind.get(key);
    if (existing) {
      if (compareCRDVersion(resource.gvk.version, existing.gvk.version) > 0) {
        latestByKind.set(key, resource);
      }
    } else {
      latestByKind.set(key, resource);
    }
  }

  const latestResources = Array.from(latestByKind.values());
  resourcesCache.set(cacheKey, latestResources);
  return latestResources;
}

async function findResource(slug: string, tag: string, gvk: GVK): Promise<Resource | undefined> {
  const resources = await listAllResources(slug, tag);
  return resources.find(
    (r) =>
      r.gvk.group === gvk.group &&
      r.gvk.version === gvk.version &&
      r.gvk.kind === gvk.kind
  );
}

function parseGVKRef(ref: string): GVK {
  const parts = ref.split("/");
  return parts.length === 2
    ? { group: "", version: parts[0], kind: parts[1] }
    : { group: parts[0], version: parts[1], kind: parts[2] };
}

// ---------------------------------------------------------------------------
// CLI args
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);
if (args.length < 2 || args[0] === "--help") {
  console.error(
    "Usage: npm run render -- <project> <gvkRef> [--output file.html]\n\n" +
      "Examples:\n" +
      "  npm run render -- kubernetes v1/Pod\n" +
      "  npm run render -- kubernetes apps/v1/Deployment --output deployment.html\n" +
      "  npm run render -- cert-manager cert-manager.io/v1/Certificate\n"
  );
  process.exit(1);
}

const projectSlug = args[0];
const gvkRef = args[1];
const outputFlag = args.indexOf("--output");
const outputFile = outputFlag !== -1 ? args[outputFlag + 1] : null;

// ---------------------------------------------------------------------------
// Load resource
// ---------------------------------------------------------------------------

const project = await findProject(projectSlug);
const tag = project.tags[0];
const gvk = parseGVKRef(gvkRef);
const resource = await findResource(project.slug, tag, gvk);

if (!resource) {
  const available = (await listAllResources(project.slug, tag))
    .map((r) => [r.gvk.group, r.gvk.version, r.gvk.kind].filter(Boolean).join("/"))
    .sort()
    .join("\n  ");
  console.error(
    `Resource not found: ${gvkRef} in project "${project.slug}" at tag "${tag}"\n\n` +
      `Available resources:\n  ${available}`
  );
  process.exit(1);
}

const apiVersion = gvk.group ? `${gvk.group}/${gvk.version}` : gvk.version;
const canonicalPath =
  projectSlug === "kubernetes"
    ? `/${[gvk.group, gvk.version, gvk.kind].filter(Boolean).join("/")}`
    : `/${projectSlug}/${[gvk.group, gvk.version, gvk.kind].filter(Boolean).join("/")}`;
const canonicalUrl = `https://kubespec.dev${canonicalPath}`;

// ---------------------------------------------------------------------------
// HTML rendering helpers
// ---------------------------------------------------------------------------

function esc(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

function typeClass(type: string, hasChildren: boolean): string {
  if (hasChildren) return "ks-type-complex";
  switch (type) {
    case "string":
      return "ks-type-string";
    case "boolean":
      return "ks-type-boolean";
    case "integer":
      return "ks-type-integer";
    case "Time":
    case "object":
      return "ks-type-object";
    default:
      return "ks-type-other";
  }
}

function renderTree(
  definition: ResourceDefinition,
  scope: string,
  level: number,
  path: string
): string {
  const entries = Object.entries(definition.properties || {});
  if (entries.length === 0) return "";

  const nestedClass = level > 0 ? " ks-nested" : "";
  const items = entries
    .map(([name, property]) => {
      const propPath = `${path}.${name}`;
      const hasChildren =
        Object.keys(property.definition?.properties || {}).length > 0;
      const isRequired =
        property.required ||
        (scope === "Namespaced" && propPath === ".metadata.namespace");

      const reqMark = isRequired
        ? `<span class="ks-required" title="Required">*</span>`
        : "";
      const typeCls = typeClass(property.type, hasChildren);
      const typeHtml = `<span class="ks-type ${typeCls}">${esc(property.type)}</span>`;
      const descHtml = property.description
        ? `<pre class="ks-desc">${esc(property.description)}</pre>`
        : "";

      if (hasChildren || property.description) {
        // Use <details> for expandable rows
        const isOpenAttr =
          level === 0 && hasChildren && property.type !== "ObjectMeta"
            ? " open"
            : "";
        const childrenHtml =
          hasChildren && property.definition
            ? renderTree(property.definition, scope, level + 1, propPath)
            : "";

        return (
          `<li class="ks-row">` +
          `<details${isOpenAttr}>` +
          `<summary class="ks-summary">${reqMark}<span class="ks-name">${esc(name)}</span>${typeHtml}</summary>` +
          descHtml +
          childrenHtml +
          `</details>` +
          `</li>`
        );
      }

      // Leaf node with no description — plain row
      return (
        `<li class="ks-row ks-leaf">` +
        `<span class="ks-leaf-line">${reqMark}<span class="ks-name">${esc(name)}</span>${typeHtml}</span>` +
        `</li>`
      );
    })
    .join("\n");

  return `<ul class="ks-tree${nestedClass}">\n${items}\n</ul>`;
}

// ---------------------------------------------------------------------------
// Styles (self-contained, scoped with .ks-schema prefix)
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
/* Tree */
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
/* Summary row */
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
/* Leaf row */
.ks-leaf-line {
  display: inline-flex;
  align-items: baseline;
  gap: 0.25rem;
  padding: 0.125rem 0.375rem;
}
/* Property name */
.ks-name { color: #111827; }
/* Required marker */
.ks-required {
  color: #dc2626;
  font-size: 0.6875rem;
  margin-right: 0.125rem;
  font-family: system-ui, sans-serif;
}
/* Type colors */
.ks-type { font-weight: 400; }
.ks-type-string  { color: #c2410c; }
.ks-type-boolean { color: #1d4ed8; }
.ks-type-integer { color: #0369a1; }
.ks-type-object  { color: #7c3aed; }
.ks-type-complex { color: #9d174d; }
.ks-type-other   { color: #065f46; }
/* Description */
.ks-desc {
  margin: 0.25rem 0 0.5rem 0.375rem;
  font-size: 0.75rem;
  font-weight: 400;
  font-family: system-ui, sans-serif;
  white-space: pre-wrap;
  max-width: 48rem;
  color: #374151;
}
/* Footer */
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
// Assemble output
// ---------------------------------------------------------------------------

const scopeLabel =
  resource.scope === "Namespaced" ? "Namespaced Resource" : "Cluster-scoped Resource";

const treeHtml = renderTree(resource.definition, resource.scope, 0, "");

const html =
  `<!-- kubespec widget: ${esc(gvk.kind)} (${esc(apiVersion)}) -->
<div class="ks-schema">
<style>
${CSS}
</style>
<div class="ks-header">
  <div class="ks-apiversion">${esc(apiVersion)}</div>
  <div class="ks-scope">${esc(scopeLabel)}</div>
  <h2 class="ks-kind">${esc(gvk.kind)}</h2>
  ${resource.definition.description ? `<pre class="ks-resource-desc">${esc(resource.definition.description)}</pre>` : ""}
</div>
${treeHtml}
<div class="ks-footer">
  View full docs on <a href="${esc(canonicalUrl)}" target="_blank" rel="noopener">kubespec.dev ↗</a>
</div>
</div>`.trim();

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

if (outputFile) {
  await writeFile(outputFile, html, "utf-8");
  console.log(`Written to ${outputFile}`);
} else {
  process.stdout.write(html + "\n");
}
