// kubespec render — generate self-contained HTML widgets from either:
// 1) a Kubernetes CRD YAML file, or
// 2) a plain JSON Schema file (JSON or YAML).
//
// Entirely standalone: no dependency on the kubespec.dev project structure.
// Any valid CRD YAML file (apiVersion: apiextensions.k8s.io/v1) can be used.
// Any valid JSON Schema document can also be used directly.
//
// Usage:
//
//	go run render.go <schema.yaml|schema.json> [-output file.html] [-version v1]
//
// Examples:
//
//	go run render.go my-crd.yaml
//	go run render.go cert-manager.yaml -output cert-manager.html
//	go run render.go gateway-api-crds.yaml -version v1 -output httproute.html
//	go run render.go schema.json -output schema.html
//
// If the YAML contains multiple CRDs or multiple JSON Schema docs, each one is
// rendered and concatenated into the output. Use -version to restrict to a
// single schema version within each CRD.
//
// Run from the scripts/ directory:
//
//	cd scripts && go run render.go <schema.yaml|schema.json>
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
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
	return toPropertyMapWithResolver(node, parentRequired, nil, nil)
}

func toPropertyMapWithResolver(node *yaml.Node, parentRequired []string, resolver *schemaResolver, visiting map[*yaml.Node]bool) *propertyMap {
	if node == nil {
		return &propertyMap{props: map[string]propertyDef{}}
	}
	if visiting == nil {
		visiting = map[*yaml.Node]bool{}
	}

	// Resolve aliases
	n := node
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	local := n
	n = resolveSchemaNode(n, resolver)
	if n.Kind != yaml.MappingNode {
		return &propertyMap{props: map[string]propertyDef{}}
	}

	// Build a flat map from the YAML mapping node (preserving key order)
	raw := decodeMapping(n)
	if resolver != nil {
		// JSON Schema allows siblings next to $ref; local keys take precedence.
		raw = mergeNodeMaps(decodeMapping(local), raw)
	}
	if visiting[n] {
		return &propertyMap{
			description: getString(raw, "description"),
			props:       map[string]propertyDef{},
		}
	}
	visiting[n] = true
	defer delete(visiting, n)

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

		resolvedPropNode := resolveSchemaNode(resolveAlias(propNode), resolver)
		propRaw := decodeMapping(resolvedPropNode)
		if resolver != nil {
			propRaw = mergeNodeMaps(decodeMapping(resolveAlias(propNode)), propRaw)
		}
		propType := getTypeString(propRaw, "type")
		isArray := propType == "array"
		required := contains(parentRequired, name)
		var def *propertyMap

		if isArray {
			itemsNode := getNode(propRaw, "items")
			var itemType string
			if itemsNode != nil {
				resolvedItemsNode := resolveSchemaNode(resolveAlias(itemsNode), resolver)
				itemsRaw := decodeMapping(resolvedItemsNode)
				if resolver != nil {
					itemsRaw = mergeNodeMaps(decodeMapping(resolveAlias(itemsNode)), itemsRaw)
				}
				itemType = getTypeString(itemsRaw, "type")
				if itemType == "" {
					itemType = "object"
				}
				if getNode(itemsRaw, "properties") != nil {
					def = toPropertyMapWithResolver(itemsNode, getStringSlice(itemsRaw, "required"), resolver, visiting)
				}
			} else {
				itemType = "object"
			}
			propType = itemType + "[]"
		} else if getNode(propRaw, "properties") != nil {
			def = toPropertyMapWithResolver(propNode, getStringSlice(propRaw, "required"), resolver, visiting)
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

type schemaResolver struct {
	root *yaml.Node
}

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

func mergeNodeMaps(primary, secondary nodeMap) nodeMap {
	out := nodeMap{}
	for k, v := range secondary {
		out[k] = v
	}
	for k, v := range primary {
		out[k] = v
	}
	return out
}

func resolveSchemaNode(node *yaml.Node, resolver *schemaResolver) *yaml.Node {
	return resolveSchemaNodeWithState(resolveAlias(node), resolver, map[*yaml.Node]bool{}, 0)
}

func resolveSchemaNodeWithState(node *yaml.Node, resolver *schemaResolver, seen map[*yaml.Node]bool, depth int) *yaml.Node {
	n := resolveAlias(node)
	if n == nil || resolver == nil {
		return n
	}
	if n.Kind != yaml.MappingNode {
		return n
	}
	if depth > 64 || seen[n] {
		return n
	}
	seen[n] = true
	defer delete(seen, n)

	raw := decodeMapping(n)

	// Resolve direct $ref chains first.
	if ref := getString(raw, "$ref"); ref != "" {
		resolved := resolver.resolveRef(ref)
		if resolved != nil {
			return resolveSchemaNodeWithState(resolved, resolver, seen, depth+1)
		}
	}

	// For nullable unions like anyOf:[{$ref:...},{type:null}], pick the best
	// non-null branch so fields become visible in the tree.
	for _, key := range []string{"anyOf", "oneOf"} {
		alts := getNode(raw, key)
		if alts == nil || alts.Kind != yaml.SequenceNode {
			continue
		}
		best := (*yaml.Node)(nil)
		bestScore := -1
		for _, alt := range alts.Content {
			candidate := resolveSchemaNodeWithState(alt, resolver, seen, depth+1)
			if candidate == nil || candidate.Kind != yaml.MappingNode {
				continue
			}
			candidateRaw := decodeMapping(candidate)
			t := getTypeString(candidateRaw, "type")
			if t == "null" {
				continue
			}
			score := 0
			if getNode(candidateRaw, "properties") != nil {
				score += 4
			}
			if getNode(candidateRaw, "items") != nil {
				score += 3
			}
			if t != "" {
				score += 2
			}
			if getString(candidateRaw, "$ref") != "" {
				score++
			}
			if score > bestScore {
				bestScore = score
				best = candidate
			}
		}
		if best != nil {
			return best
		}
	}

	// allOf usually composes multiple schemas; for rendering we pick the first
	// branch that contributes concrete shape information.
	allOf := getNode(raw, "allOf")
	if allOf != nil && allOf.Kind == yaml.SequenceNode {
		for _, alt := range allOf.Content {
			candidate := resolveSchemaNodeWithState(alt, resolver, seen, depth+1)
			if candidate == nil || candidate.Kind != yaml.MappingNode {
				continue
			}
			candidateRaw := decodeMapping(candidate)
			if getNode(candidateRaw, "properties") != nil || getNode(candidateRaw, "items") != nil || getTypeString(candidateRaw, "type") != "" {
				return candidate
			}
		}
	}

	return n
}

func (r *schemaResolver) resolveRef(ref string) *yaml.Node {
	if r == nil || r.root == nil || ref == "" {
		return nil
	}
	if !strings.HasPrefix(ref, "#") {
		// External refs are intentionally not resolved in this standalone script.
		return nil
	}

	n := r.root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if ref == "#" {
		return resolveAlias(n)
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}

	cur := resolveAlias(n)
	for _, token := range strings.Split(ref[2:], "/") {
		key := decodeJSONPointerToken(token)
		switch cur.Kind {
		case yaml.MappingNode:
			next := (*yaml.Node)(nil)
			for i := 0; i+1 < len(cur.Content); i += 2 {
				if cur.Content[i].Value == key {
					next = cur.Content[i+1]
					break
				}
			}
			if next == nil {
				return nil
			}
			cur = resolveAlias(next)
		case yaml.SequenceNode:
			idx, err := strconv.Atoi(key)
			if err != nil || idx < 0 || idx >= len(cur.Content) {
				return nil
			}
			cur = resolveAlias(cur.Content[idx])
		default:
			return nil
		}
	}
	return cur
}

func decodeJSONPointerToken(token string) string {
	token = strings.ReplaceAll(token, "~1", "/")
	token = strings.ReplaceAll(token, "~0", "~")
	return token
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

func getTypeString(m nodeMap, key string) string {
	n := getNode(m, key)
	if n == nil {
		return ""
	}
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	if n.Kind != yaml.SequenceNode {
		return ""
	}

	var types []string
	for _, item := range n.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		t := item.Value
		if t == "null" {
			continue
		}
		types = append(types, t)
	}
	if len(types) == 0 {
		for _, item := range n.Content {
			if item.Kind == yaml.ScalarNode {
				return item.Value
			}
		}
		return ""
	}
	return strings.Join(types, "|")
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
	} else if scope == "Schema" {
		scopeLabel = "JSON Schema"
	}
	canonicalURL := ""
	if scope != "Schema" {
		parts := []string{}
		for _, p := range []string{group, version, kind} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		canonicalURL = "https://kubespec.dev/" + strings.Join(parts, "/")
	}

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
	if canonicalURL != "" {
		b.WriteString(`<div class="ks-footer">` + "\n")
		fmt.Fprintf(&b, `  View full docs on <a href="%s" target="_blank" rel="noopener">kubespec.dev ↗</a>`+"\n", esc(canonicalURL))
		b.WriteString("</div>\n")
	}
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

func (d crdDocument) rootMappingNode() *yaml.Node {
	n := d.root
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	return resolveAlias(n)
}

func isCRD(m nodeMap) bool {
	kind := getString(m, "kind")
	if kind != "CustomResourceDefinition" {
		return false
	}
	apiVer := getString(m, "apiVersion")
	return strings.HasPrefix(apiVer, "apiextensions.k8s.io/")
}

func looksLikeJSONSchema(m nodeMap) bool {
	if getString(m, "$schema") != "" {
		return true
	}
	if getNode(m, "properties") != nil {
		return true
	}
	if getNode(m, "$defs") != nil || getNode(m, "definitions") != nil {
		return true
	}
	if getNode(m, "allOf") != nil || getNode(m, "oneOf") != nil || getNode(m, "anyOf") != nil {
		return true
	}
	t := getTypeString(m, "type")
	return t != "" && (getString(m, "title") != "" || getNode(m, "required") != nil)
}

func renderJSONSchemaWidget(doc crdDocument) (string, bool) {
	m := doc.mapping()
	if !looksLikeJSONSchema(m) {
		return "", false
	}

	title := getString(m, "title")
	if title == "" {
		title = "JSON Schema"
	}
	schemaVersion := getString(m, "$schema")
	if schemaVersion == "" {
		schemaVersion = "json-schema"
	}
	required := getStringSlice(m, "required")

	root := doc.rootMappingNode()
	if root == nil || root.Kind != yaml.MappingNode {
		return "", false
	}
	pm := toPropertyMapWithResolver(root, required, &schemaResolver{root: root}, nil)
	return renderWidget(title, "", schemaVersion, "Schema", pm), true
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	outputFile := flag.String("output", "", "Write output to `FILE` instead of stdout")
	versionFilter := flag.String("version", "", "Only render a specific schema `VERSION` (e.g. v1)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr,
			"Usage: go run render.go [flags] <schema.yaml|schema.json>\n\n"+
				"Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr,
			"\nExamples:\n"+
				"  go run render.go my-crd.yaml\n"+
				"  go run render.go cert-manager.yaml -output cert-manager.html\n"+
				"  go run render.go gateway-crds.yaml -version v1 -output httproute.html\n"+
				"  go run render.go schema.json -output schema.html\n")
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

		if !isCRD(m) {
			if widget, ok := renderJSONSchemaWidget(doc); ok {
				widgets = append(widgets, widget)
			}
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
			"No CRDs or JSON Schemas found in %s.\n"+
				"Expected one of:\n"+
				"  1) A CRD document with apiVersion: apiextensions.k8s.io/v1 and kind: CustomResourceDefinition\n"+
				"  2) A plain JSON Schema document (for example containing $schema/properties/type)\n",
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
