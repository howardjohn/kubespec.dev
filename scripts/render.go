// kubespec render — generate self-contained HTML widgets from a CRD YAML file.
//
// Entirely standalone: no dependency on the kubespec.dev project structure.
// Any valid CRD YAML file (apiVersion: apiextensions.k8s.io/v1) can be used.
//
// Usage:
//
//	go run render.go <crd.yaml> [-output file.html] [-version v1]
//
// Examples:
//
//	go run render.go my-crd.yaml
//	go run render.go cert-manager.yaml -output cert-manager.html
//	go run render.go gateway-api-crds.yaml -version v1 -output httproute.html
//
// If the YAML contains multiple CRDs (multi-document YAML), each one is rendered
// and concatenated into the output.  Use -version to restrict to a single schema
// version within each CRD.
//
// Run from the scripts/ directory:
//
//	cd scripts && go run render.go <crd.yaml>
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Schema conversion
// ---------------------------------------------------------------------------

type propertyDef struct {
	description string
	propType    string
	required    bool
	isArray     bool
	definition  *propertyMap
}

type propertyMap struct {
	description string
	// ordered slice of (name, propertyDef) to preserve YAML key insertion order
	keys  []string
	props map[string]propertyDef
}

// schemaNode is a raw YAML node used to walk openAPIV3Schema while preserving
// map key order via yaml.Node's Content field.
func toPropertyMap(node *yaml.Node, parentRequired []string) *propertyMap {
	if node == nil {
		return &propertyMap{props: map[string]propertyDef{}}
	}

	// Resolve aliases
	n := node
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	if n.Kind != yaml.MappingNode {
		return &propertyMap{props: map[string]propertyDef{}}
	}

	// Build a flat map from the YAML mapping node (preserving key order)
	raw := decodeMapping(n)

	pm := &propertyMap{
		description: getString(raw, "description"),
		props:       map[string]propertyDef{},
	}

	propsNode := getNode(raw, "properties")
	if propsNode == nil || propsNode.Kind != yaml.MappingNode {
		return pm
	}

	// Walk properties in declaration order (yaml.Node preserves it)
	for i := 0; i+1 < len(propsNode.Content); i += 2 {
		nameNode := propsNode.Content[i]
		propNode := propsNode.Content[i+1]
		name := nameNode.Value

		propRaw := decodeMapping(resolveAlias(propNode))
		propType := getString(propRaw, "type")
		isArray := propType == "array"
		required := contains(parentRequired, name)
		var def *propertyMap

		if isArray {
			itemsNode := getNode(propRaw, "items")
			var itemType string
			if itemsNode != nil {
				itemsRaw := decodeMapping(resolveAlias(itemsNode))
				itemType = getString(itemsRaw, "type")
				if itemType == "" {
					itemType = "object"
				}
				if getNode(itemsRaw, "properties") != nil {
					def = toPropertyMap(itemsNode, getStringSlice(itemsRaw, "required"))
				}
			} else {
				itemType = "object"
			}
			propType = itemType + "[]"
		} else if getNode(propRaw, "properties") != nil {
			def = toPropertyMap(propNode, getStringSlice(propRaw, "required"))
		} else if getBool(propRaw, "x-kubernetes-preserve-unknown-fields") {
			if propType == "" {
				propType = "object"
			}
		}

		pm.keys = append(pm.keys, name)
		pm.props[name] = propertyDef{
			description: getString(propRaw, "description"),
			propType:    propType,
			required:    required,
			isArray:     isArray,
			definition:  def,
		}
	}

	return pm
}

// ---------------------------------------------------------------------------
// YAML node helpers
// ---------------------------------------------------------------------------

type nodeMap = map[string]*yaml.Node

func resolveAlias(n *yaml.Node) *yaml.Node {
	if n != nil && n.Kind == yaml.AliasNode {
		return n.Alias
	}
	return n
}

func decodeMapping(n *yaml.Node) nodeMap {
	m := nodeMap{}
	if n == nil || n.Kind != yaml.MappingNode {
		return m
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		m[n.Content[i].Value] = n.Content[i+1]
	}
	return m
}

func getNode(m nodeMap, key string) *yaml.Node {
	if v, ok := m[key]; ok {
		return resolveAlias(v)
	}
	return nil
}

func getString(m nodeMap, key string) string {
	n := getNode(m, key)
	if n != nil && (n.Kind == yaml.ScalarNode) {
		return n.Value
	}
	return ""
}

func getBool(m nodeMap, key string) bool {
	n := getNode(m, key)
	if n != nil && n.Kind == yaml.ScalarNode {
		return n.Value == "true"
	}
	return false
}

func getStringSlice(m nodeMap, key string) []string {
	n := getNode(m, key)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var result []string
	for _, item := range n.Content {
		if item.Kind == yaml.ScalarNode {
			result = append(result, item.Value)
		}
	}
	return result
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// HTML rendering
// ---------------------------------------------------------------------------

func esc(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

func typeClass(propType string, hasChildren bool) string {
	if hasChildren {
		return "ks-type-complex"
	}
	base := strings.TrimSuffix(propType, "[]")
	switch base {
	case "string":
		return "ks-type-string"
	case "boolean":
		return "ks-type-boolean"
	case "integer", "number":
		return "ks-type-integer"
	case "object":
		return "ks-type-object"
	default:
		return "ks-type-other"
	}
}

func renderTree(pm *propertyMap, scope string, level int, path string, b *strings.Builder) {
	if pm == nil || len(pm.keys) == 0 {
		return
	}

	nestedClass := ""
	if level > 0 {
		nestedClass = " ks-nested"
	}
	fmt.Fprintf(b, "<ul class=\"ks-tree%s\">\n", nestedClass)

	for _, name := range pm.keys {
		prop := pm.props[name]
		propPath := path + "." + name
		hasChildren := prop.definition != nil && len(prop.definition.keys) > 0
		isRequired := prop.required || (scope == "Namespaced" && propPath == ".metadata.namespace")

		reqMark := ""
		if isRequired {
			reqMark = `<span class="ks-required" title="Required">*</span>`
		}
		typeCls := typeClass(prop.propType, hasChildren)
		typeHTML := fmt.Sprintf(`<span class="ks-type %s">%s</span>`, typeCls, esc(prop.propType))
		descHTML := ""
		if prop.description != "" {
			descHTML = fmt.Sprintf(`<pre class="ks-desc">%s</pre>`, esc(prop.description))
		}

		if hasChildren || prop.description != "" {
			openAttr := ""
			if level == 0 && hasChildren {
				openAttr = " open"
			}
			fmt.Fprintf(b, `<li class="ks-row"><details%s>`, openAttr)
			fmt.Fprintf(b, `<summary class="ks-summary">%s<span class="ks-name">%s</span>%s</summary>`,
				reqMark, esc(name), typeHTML)
			b.WriteString(descHTML)
			if hasChildren && prop.definition != nil {
				renderTree(prop.definition, scope, level+1, propPath, b)
			}
			b.WriteString("</details></li>\n")
		} else {
			fmt.Fprintf(b, `<li class="ks-row ks-leaf"><span class="ks-leaf-line">%s<span class="ks-name">%s</span>%s</span></li>`+"\n",
				reqMark, esc(name), typeHTML)
		}
	}

	b.WriteString("</ul>")
}

const css = `.ks-schema {
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
.ks-footer a:hover { text-decoration: underline; }`

func renderWidget(kind, group, version, scope string, pm *propertyMap) string {
	apiVersion := version
	if group != "" {
		apiVersion = group + "/" + version
	}
	scopeLabel := "Cluster-scoped Resource"
	if scope == "Namespaced" {
		scopeLabel = "Namespaced Resource"
	}
	parts := []string{}
	for _, p := range []string{group, version, kind} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	canonicalURL := "https://kubespec.dev/" + strings.Join(parts, "/")

	var b strings.Builder
	fmt.Fprintf(&b, "<!-- kubespec widget: %s (%s) -->\n", esc(kind), esc(apiVersion))
	b.WriteString(`<div class="ks-schema">` + "\n")
	b.WriteString("<style>\n" + css + "\n</style>\n")
	b.WriteString(`<div class="ks-header">` + "\n")
	fmt.Fprintf(&b, `  <div class="ks-apiversion">%s</div>`+"\n", esc(apiVersion))
	fmt.Fprintf(&b, `  <div class="ks-scope">%s</div>`+"\n", esc(scopeLabel))
	fmt.Fprintf(&b, `  <h2 class="ks-kind">%s</h2>`+"\n", esc(kind))
	if pm.description != "" {
		fmt.Fprintf(&b, `  <pre class="ks-resource-desc">%s</pre>`+"\n", esc(pm.description))
	}
	b.WriteString("</div>\n")
	renderTree(pm, scope, 0, "", &b)
	b.WriteString("\n")
	b.WriteString(`<div class="ks-footer">` + "\n")
	fmt.Fprintf(&b, `  View full docs on <a href="%s" target="_blank" rel="noopener">kubespec.dev ↗</a>`+"\n", esc(canonicalURL))
	b.WriteString("</div>\n")
	b.WriteString("</div>")
	return b.String()
}

// ---------------------------------------------------------------------------
// CRD YAML parsing
// ---------------------------------------------------------------------------

// crdDocument holds the raw yaml.Node for a single YAML document so that
// the openAPIV3Schema tree can be walked while preserving key insertion order.
type crdDocument struct {
	root *yaml.Node
}

func parseCRDDocuments(data []byte) ([]crdDocument, error) {
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	var docs []crdDocument
	for {
		var node yaml.Node
		err := dec.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: YAML parse error: %v\n", err)
			continue
		}
		docs = append(docs, crdDocument{root: &node})
	}
	return docs, nil
}

func (d crdDocument) mapping() nodeMap {
	n := d.root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	return decodeMapping(n)
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	outputFile := flag.String("output", "", "Write output to `FILE` instead of stdout")
	versionFilter := flag.String("version", "", "Only render a specific schema `VERSION` (e.g. v1)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"Usage: go run render.go [flags] <crd.yaml>\n\n"+
				"Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr,
			"\nExamples:\n"+
				"  go run render.go my-crd.yaml\n"+
				"  go run render.go cert-manager.yaml -output cert-manager.html\n"+
				"  go run render.go gateway-crds.yaml -version v1 -output httproute.html\n")
	}
	flag.Parse()

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}
	inputFile := flag.Arg(0)

	data, err := os.ReadFile(inputFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", inputFile, err)
		os.Exit(1)
	}

	docs, _ := parseCRDDocuments(data)

	var widgets []string

	for _, doc := range docs {
		m := doc.mapping()

		kind := getString(m, "kind")
		if kind != "CustomResourceDefinition" {
			continue
		}
		apiVer := getString(m, "apiVersion")
		if !strings.HasPrefix(apiVer, "apiextensions.k8s.io/") {
			continue
		}

		specNode := getNode(m, "spec")
		if specNode == nil {
			continue
		}
		spec := decodeMapping(specNode)

		group := getString(spec, "group")
		scope := getString(spec, "scope")
		if scope == "" {
			scope = "Cluster"
		}

		namesNode := getNode(spec, "names")
		var crdKind string
		if namesNode != nil {
			namesMap := decodeMapping(namesNode)
			crdKind = getString(namesMap, "kind")
		}

		versionsNode := getNode(spec, "versions")
		if versionsNode == nil || versionsNode.Kind != yaml.SequenceNode {
			continue
		}

		for _, verNode := range versionsNode.Content {
			verMap := decodeMapping(resolveAlias(verNode))
			verName := getString(verMap, "name")
			if *versionFilter != "" && verName != *versionFilter {
				continue
			}

			schemaNode := getNode(verMap, "schema")
			if schemaNode == nil {
				fmt.Fprintf(os.Stderr, "Warning: no schema for %s version %s, skipping\n", crdKind, verName)
				continue
			}
			schemaMap := decodeMapping(schemaNode)
			openAPINode := getNode(schemaMap, "openAPIV3Schema")
			if openAPINode == nil {
				fmt.Fprintf(os.Stderr, "Warning: no openAPIV3Schema for %s version %s, skipping\n", crdKind, verName)
				continue
			}

			openAPIMap := decodeMapping(openAPINode)
			pm := toPropertyMap(openAPINode, getStringSlice(openAPIMap, "required"))
			widgets = append(widgets, renderWidget(crdKind, group, verName, scope, pm))
		}
	}

	if len(widgets) == 0 {
		fmt.Fprintf(os.Stderr,
			"No CRDs found in %s.\n"+
				"Make sure the file contains at least one document with:\n"+
				"  apiVersion: apiextensions.k8s.io/v1\n"+
				"  kind: CustomResourceDefinition\n",
			inputFile)
		os.Exit(1)
	}

	html := strings.Join(widgets, "\n\n")

	if *outputFile != "" {
		if err := os.WriteFile(*outputFile, []byte(html), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", *outputFile, err)
			os.Exit(1)
		}
		n := len(widgets)
		suffix := "s"
		if n == 1 {
			suffix = ""
		}
		fmt.Printf("Written to %s (%d widget%s)\n", *outputFile, n, suffix)
	} else {
		fmt.Println(html)
	}
}
