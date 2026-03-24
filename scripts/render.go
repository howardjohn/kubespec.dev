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
	validation  []validationItem
	definition  *propertyMap
}

type validationItem struct {
	label   string
	value   string
	isBlock bool
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
			validation:  collectValidationItems(propRaw),
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

func getNodeScalarString(n *yaml.Node) string {
	n = resolveAlias(n)
	if n == nil {
		return ""
	}
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	out, err := yaml.Marshal(n)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func getSequenceScalarStrings(m nodeMap, key string) []string {
	n := getNode(m, key)
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	var values []string
	for _, item := range n.Content {
		value := getNodeScalarString(item)
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func appendValidationItem(items []validationItem, label, value string, isBlock bool) []validationItem {
	if strings.TrimSpace(value) == "" {
		return items
	}
	return append(items, validationItem{label: label, value: value, isBlock: isBlock})
}

func appendScalarValidation(items []validationItem, raw nodeMap, key, label string) []validationItem {
	return appendValidationItem(items, label, getNodeScalarString(getNode(raw, key)), false)
}

func collectValidationItems(raw nodeMap) []validationItem {
	var items []validationItem

	if values := getSequenceScalarStrings(raw, "enum"); len(values) > 0 {
		items = appendValidationItem(items, "Enum", strings.Join(values, ", "), false)
	}
	items = appendScalarValidation(items, raw, "const", "Const")
	items = appendScalarValidation(items, raw, "default", "Default")
	items = appendScalarValidation(items, raw, "format", "Format")
	items = appendScalarValidation(items, raw, "pattern", "Pattern")
	items = appendScalarValidation(items, raw, "multipleOf", "Multiple of")
	items = appendScalarValidation(items, raw, "minLength", "Min length")
	items = appendScalarValidation(items, raw, "maxLength", "Max length")
	items = appendScalarValidation(items, raw, "minItems", "Min items")
	items = appendScalarValidation(items, raw, "maxItems", "Max items")
	items = appendScalarValidation(items, raw, "uniqueItems", "Unique items")
	items = appendScalarValidation(items, raw, "minProperties", "Min properties")
	items = appendScalarValidation(items, raw, "maxProperties", "Max properties")
	items = appendScalarValidation(items, raw, "nullable", "Nullable")
	items = appendScalarValidation(items, raw, "x-kubernetes-int-or-string", "Int or string")
	items = appendScalarValidation(items, raw, "x-kubernetes-preserve-unknown-fields", "Preserve unknown fields")
	items = appendScalarValidation(items, raw, "x-kubernetes-list-type", "List type")
	items = appendScalarValidation(items, raw, "x-kubernetes-map-type", "Map type")

	minimum := getNodeScalarString(getNode(raw, "minimum"))
	switch getNodeScalarString(getNode(raw, "exclusiveMinimum")) {
	case "true":
		if minimum != "" {
			items = appendValidationItem(items, "Minimum", "> "+minimum, false)
		}
	case "":
		items = appendValidationItem(items, "Minimum", minimum, false)
	default:
		items = appendValidationItem(items, "Exclusive minimum", getNodeScalarString(getNode(raw, "exclusiveMinimum")), false)
		if minimum != "" {
			items = appendValidationItem(items, "Minimum", minimum, false)
		}
	}

	maximum := getNodeScalarString(getNode(raw, "maximum"))
	switch getNodeScalarString(getNode(raw, "exclusiveMaximum")) {
	case "true":
		if maximum != "" {
			items = appendValidationItem(items, "Maximum", "< "+maximum, false)
		}
	case "":
		items = appendValidationItem(items, "Maximum", maximum, false)
	default:
		items = appendValidationItem(items, "Exclusive maximum", getNodeScalarString(getNode(raw, "exclusiveMaximum")), false)
		if maximum != "" {
			items = appendValidationItem(items, "Maximum", maximum, false)
		}
	}

	if values := getSequenceScalarStrings(raw, "x-kubernetes-list-map-keys"); len(values) > 0 {
		items = appendValidationItem(items, "List map keys", strings.Join(values, ", "), false)
	}

	validations := getNode(raw, "x-kubernetes-validations")
	if validations != nil && validations.Kind == yaml.SequenceNode {
		for _, item := range validations.Content {
			m := decodeMapping(resolveAlias(item))
			message := getString(m, "message")
			items = appendValidationItem(items, "Rule", message, true)
		}
	}

	return items
}

func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func renderValidationDetails(items []validationItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<details class="ks-validation"><summary class="ks-validation-summary">Validation</summary><div class="ks-validation-body">`)
	for _, item := range items {
		b.WriteString(`<div class="ks-validation-item">`)
		fmt.Fprintf(&b, `<span class="ks-validation-label">%s</span>`, esc(item.label))
		if item.isBlock || strings.Contains(item.value, "\n") {
			fmt.Fprintf(&b, `<div class="ks-validation-value ks-validation-value-block">%s</div>`, esc(item.value))
		} else {
			fmt.Fprintf(&b, `<span class="ks-validation-value">%s</span>`, esc(item.value))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</div></details>`)
	return b.String()
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

type searchEntry struct {
	nodeID   string
	path     string
	propType string
}

func renderTree(pm *propertyMap, scope string, level int, path, widgetID string, counter *int, searchIndex *[]searchEntry, b *strings.Builder) {
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
		searchPath := strings.TrimPrefix(propPath, ".")
		hasChildren := prop.definition != nil && len(prop.definition.keys) > 0
		isRequired := prop.required || (scope == "Namespaced" && propPath == ".metadata.namespace")
		nodeID := fmt.Sprintf("%s-node-%d", widgetID, *counter)
		*counter++
		*searchIndex = append(*searchIndex, searchEntry{
			nodeID:   nodeID,
			path:     searchPath,
			propType: prop.propType,
		})

		reqMark := ""
		if isRequired {
			reqMark = `<span class="ks-required" title="Required">*</span>`
		}
		typeCls := typeClass(prop.propType, hasChildren)
		typeHTML := fmt.Sprintf(`<span class="ks-type %s">%s</span>`, typeCls, esc(prop.propType))
		metaHTML := ""
		var metaParts []string
		if prop.description != "" {
			metaParts = append(metaParts, fmt.Sprintf(`<div class="ks-desc">%s</div>`, esc(prop.description)))
		}
		if validationHTML := renderValidationDetails(prop.validation); validationHTML != "" {
			metaParts = append(metaParts, validationHTML)
		}
		if len(metaParts) > 0 {
			metaHTML = `<div class="ks-meta">` + strings.Join(metaParts, "") + `</div>`
		}

		if hasChildren || metaHTML != "" {
			openAttr := ""
			if level == 0 && hasChildren {
				openAttr = " open"
			}
			fmt.Fprintf(b, `<li class="ks-row" data-ks-path="%s"><details%s data-ks-has-children="%t" data-ks-has-description="%t">`, esc(searchPath), openAttr, hasChildren, metaHTML != "")
			fmt.Fprintf(b, `<summary class="ks-summary" id="%s" data-ks-node-id="%s" data-ks-path="%s">%s<span class="ks-name">%s</span>%s</summary>`,
				esc(nodeID), esc(nodeID), esc(searchPath), reqMark, esc(name), typeHTML)
			b.WriteString(metaHTML)
			if hasChildren && prop.definition != nil {
				renderTree(prop.definition, scope, level+1, propPath, widgetID, counter, searchIndex, b)
			}
			b.WriteString("</details></li>\n")
		} else {
			fmt.Fprintf(b, `<li class="ks-row ks-leaf" data-ks-path="%s"><span class="ks-leaf-line" id="%s" data-ks-node-id="%s" data-ks-path="%s">%s<span class="ks-name">%s</span>%s</span></li>`+"\n",
				esc(searchPath), esc(nodeID), esc(nodeID), esc(searchPath), reqMark, esc(name), typeHTML)
		}
	}

	b.WriteString("</ul>")
}

const css = `.ks-schema {
  font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  font-size: 14px;
  line-height: 1.5;
  color: #0f172a;
  max-width: 56rem;
}
.ks-schema,
.ks-schema * {
  box-sizing: border-box;
}
.ks-schema :where(div, span, ul, li, details, summary, input, button, a) {
  margin: 0;
  padding: 0;
  border: 0;
  font: inherit;
  line-height: inherit;
  color: inherit;
  letter-spacing: inherit;
  text-transform: none;
  text-decoration: none;
}
.ks-schema :where(ul, li) {
  list-style: none;
}
.ks-schema :where(pre, code) {
  margin: 0;
  padding: 0;
  border: 0;
  background: transparent;
  font: inherit;
  color: inherit;
  white-space: inherit;
  overflow: visible;
}
.ks-schema :where(button, input) {
  background: none;
}
.ks-header { margin-bottom: 1rem; }
.ks-apiversion {
  font-size: 0.875rem;
  font-weight: 700;
  letter-spacing: 0.01em;
  color: #0f766e;
  margin-bottom: 0.4rem;
}
.ks-kind {
  font-size: 1.875rem;
  font-weight: 700;
  margin: 0;
  line-height: 1.2;
  letter-spacing: -0.02em;
  color: #020617;
}
.ks-resource-desc {
  margin: 0.75rem 0 1rem;
  padding: 0.8rem 0.95rem;
  border-left: 4px solid #14b8a6;
  border-radius: 0.75rem;
  background: linear-gradient(135deg, #ecfeff 0%, #f8fafc 100%);
  font-size: 0.875rem;
  color: #164e63;
  line-height: 1.65;
  white-space: pre-wrap;
  max-width: 48rem;
  box-shadow: inset 0 0 0 1px rgba(20, 184, 166, 0.08);
}
.ks-search {
  position: relative;
  margin: 0.875rem 0 1rem;
  max-width: 32rem;
}
.ks-search-input {
  width: 100%;
  box-sizing: border-box;
  padding: 0.625rem 0.75rem;
  border: 1px solid #cbd5e1;
  border-radius: 0.65rem;
  background: #ffffff;
  color: #0f172a;
  font: inherit;
  box-shadow: 0 8px 24px rgba(15, 23, 42, 0.06);
}
.ks-search-input::placeholder { color: #9ca3af; }
.ks-search-input:focus {
  outline: none;
  border-color: #0f766e;
  box-shadow: 0 0 0 3px rgba(20, 184, 166, 0.14);
}
.ks-search-results {
  position: absolute;
  top: calc(100% + 0.375rem);
  left: 0;
  right: 0;
  z-index: 20;
  padding: 0.375rem;
  border: 1px solid #cbd5e1;
  border-radius: 0.75rem;
  background: rgba(255, 255, 255, 0.98);
  box-shadow: 0 18px 40px rgba(15, 23, 42, 0.14);
  backdrop-filter: blur(8px);
}
.ks-search-results[hidden] { display: none; }
.ks-search-result {
  display: block;
  width: 100%;
  padding: 0.5rem 0.625rem;
  border: 0;
  border-radius: 0.5rem;
  background: transparent;
  color: inherit;
  text-align: left;
  cursor: pointer;
}
.ks-search-result:hover,
.ks-search-result.is-active { background: #ccfbf1; }
.ks-search-result-path {
  display: block;
  font-family: ui-monospace, "Cascadia Code", "Source Code Pro", Menlo, monospace;
  font-size: 0.8125rem;
  font-weight: 600;
  color: #0f172a;
}
.ks-search-result-type {
  display: block;
  margin-top: 0.125rem;
  font-size: 0.75rem;
  color: #475569;
}
.ks-search-empty {
  padding: 0.5rem 0.625rem;
  font-size: 0.8125rem;
  color: #475569;
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
  border-left: 2px solid #cbd5e1;
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
.ks-summary::before { content: none !important; display: none !important; }
.ks-summary:hover { background: #f0fdfa; }
.ks-summary.ks-search-hit { background: #99f6e4; }
.ks-leaf-line {
  display: inline-flex;
  align-items: baseline;
  gap: 0.25rem;
  padding: 0.125rem 0.375rem;
}
.ks-leaf-line.ks-search-hit { background: #99f6e4; border-radius: 0.25rem; }
.ks-name { color: #0f172a; }
.ks-required {
  color: #e11d48;
  font-size: 0.6875rem;
  margin-right: 0.125rem;
  font-family: system-ui, sans-serif;
}
.ks-type { font-weight: 400; }
.ks-type-string  { color: #ea580c; }
.ks-type-boolean { color: #2563eb; }
.ks-type-integer { color: #0284c7; }
.ks-type-object  { color: #7c3aed; }
.ks-type-complex { color: #db2777; }
.ks-type-other   { color: #0f766e; }
.ks-meta {
  margin: 0.35rem 0 0.65rem 0.375rem;
  padding: 0.6rem 0.75rem;
  border-radius: 0.65rem;
  background: #f8fafc;
  max-width: 48rem;
  box-shadow: inset 0 0 0 1px rgba(203, 213, 225, 0.55);
}
.ks-desc {
  margin: 0;
  font-size: 0.75rem;
  font-weight: 400;
  font-family: system-ui, sans-serif;
  white-space: pre-wrap;
  color: #334155;
}
.ks-hide-desc > .ks-meta { display: none; }
.ks-validation {
  margin-top: 0.55rem;
  border-top: 1px solid #dbe2ea;
  padding-top: 0.45rem;
}
.ks-validation-summary {
  display: flex;
  align-items: center;
  padding: 0;
  cursor: pointer;
  list-style: none;
  font-size: 0.7rem;
  font-weight: 600;
  letter-spacing: 0.01em;
  color: #64748b;
  user-select: none;
}
.ks-validation-summary::-webkit-details-marker { display: none; }
.ks-validation-summary::marker { display: none; }
.ks-validation-summary::after { content: none !important; display: none !important; }
.ks-validation-summary:hover { color: #0f766e; }
.ks-validation-summary::before {
  content: "▸" !important;
  margin-right: 0.4rem;
  color: #94a3b8;
  transition: transform 120ms ease;
}
.ks-validation[open] > .ks-validation-summary::before {
  transform: rotate(90deg);
}
.ks-validation-body {
  padding-top: 0.45rem;
}
.ks-validation-item {
  display: grid;
  grid-template-columns: minmax(0, 7rem) minmax(0, 1fr);
  gap: 0.25rem 0.6rem;
  padding-top: 0.35rem;
}
.ks-validation-label {
  font-size: 0.68rem;
  font-weight: 600;
  color: #64748b;
}
.ks-validation-value {
  min-width: 0;
  font-size: 0.68rem;
  font-weight: 400;
  color: #334155;
  white-space: pre-wrap;
  word-break: break-word;
}
.ks-validation-value-block {
  margin: 0;
  font-family: inherit;
  background: transparent;
  border-radius: 0;
  padding: 0;
}
`

func renderSearchScript(hostID, templateID string, searchIndex []searchEntry) string {
	var entries strings.Builder
	entries.WriteString("[\n")
	for i, entry := range searchIndex {
		if i > 0 {
			entries.WriteString(",\n")
		}
		fmt.Fprintf(&entries, "  { nodeId: %s, path: %s, propType: %s }",
			strconv.Quote(entry.nodeID),
			strconv.Quote(entry.path),
			strconv.Quote(entry.propType),
		)
	}
	entries.WriteString("\n]")

	var b strings.Builder
	fmt.Fprintf(&b, `<script>
(() => {
  const host = document.getElementById(%s);
  const template = document.getElementById(%s);
  if (!host || !template || host.dataset.ksWidgetReady === "true") {
    return;
  }
  host.dataset.ksWidgetReady = "true";

  const shadow = host.shadowRoot || host.attachShadow({ mode: "open" });
  shadow.innerHTML = "";
  shadow.appendChild(template.content.cloneNode(true));

  const root = shadow.querySelector(".ks-schema");
  if (!root) {
    return;
  }

  const entries = %s;
  const input = shadow.querySelector("[data-ks-search-input]");
  const results = shadow.querySelector("[data-ks-search-results]");
  if (!input || !results || entries.length === 0) {
    return;
  }
  const nodeByID = new Map();
  shadow.querySelectorAll("[data-ks-node-id]").forEach((element) => {
    nodeByID.set(element.getAttribute("data-ks-node-id"), element);
  });
  const entryByPath = new Map(entries.map((entry) => [normalize(entry.path), entry]));

  let matches = [];
  let activeIndex = -1;
  let highlightTimer = 0;

  function normalize(value) {
    return value.toLowerCase().trim().replace(/^\./, "");
  }

  function scoreSegment(query, target) {
    if (!query) {
      return 0;
    }
    let qi = 0;
    let score = 0;
    let streak = 0;
    let lastIndex = -1;
    for (let i = 0; i < target.length && qi < query.length; i += 1) {
      if (target[i] !== query[qi]) {
        continue;
      }
      score += 1;
      if (i === 0) {
        score += 12;
      }
      if (lastIndex === i - 1) {
        streak += 1;
        score += 8 + Math.min(streak, 4);
      } else {
        streak = 0;
      }
      lastIndex = i;
      qi += 1;
    }
    if (qi !== query.length) {
      return -1;
    }
    if (target.startsWith(query)) {
      score += 18;
    }
    return score - (target.length - query.length);
  }

  function fuzzyScore(query, target) {
    const queryParts = normalize(query).split(".").filter(Boolean);
    const targetParts = normalize(target).split(".").filter(Boolean);
    if (queryParts.length === 0) {
      return -1;
    }

    let total = 0;
    let searchFrom = 0;
    for (const queryPart of queryParts) {
      let bestIndex = -1;
      let bestScore = -1;
      for (let i = searchFrom; i < targetParts.length; i += 1) {
        const partScore = scoreSegment(queryPart, targetParts[i]);
        if (partScore > bestScore) {
          bestIndex = i;
          bestScore = partScore;
        }
        if (targetParts[i].startsWith(queryPart)) {
          break;
        }
      }
      if (bestIndex === -1 || bestScore < 0) {
        return -1;
      }
      total += bestScore;
      if (bestIndex === searchFrom) {
        total += 6;
      }
      searchFrom = bestIndex + 1;
    }

    const normalizedTarget = normalize(target);
    const normalizedQuery = normalize(query);
    if (normalizedTarget.startsWith(normalizedQuery)) {
      total += 24;
    }
    return total - Math.max(0, targetParts.length - queryParts.length);
  }

  function escapeHtml(value) {
    return value
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function hideResults() {
    results.hidden = true;
    results.innerHTML = "";
    matches = [];
    activeIndex = -1;
  }

  function setActiveIndex(nextIndex) {
    activeIndex = nextIndex;
    const buttons = results.querySelectorAll("[data-ks-search-result]");
    buttons.forEach((button, index) => {
      button.classList.toggle("is-active", index === activeIndex);
    });
  }

  function renderMatches() {
    const query = input.value.trim();
    if (!query) {
      hideResults();
      return;
    }

    matches = entries
      .map((entry) => ({ ...entry, score: fuzzyScore(query, entry.path) }))
      .filter((entry) => entry.score >= 0)
      .sort((a, b) => b.score - a.score || a.path.length - b.path.length || a.path.localeCompare(b.path))
      .slice(0, 8);

    if (matches.length === 0) {
      results.innerHTML = '<div class="ks-search-empty">No matching fields</div>';
      results.hidden = false;
      activeIndex = -1;
      return;
    }

    results.innerHTML = matches.map((entry, index) => (
      '<button type="button" class="ks-search-result' + (index === 0 ? ' is-active' : '') + '" data-ks-search-result data-node-id="' + escapeHtml(entry.nodeId) + '">' +
        '<span class="ks-search-result-path">' + escapeHtml(entry.path) + '</span>' +
        '<span class="ks-search-result-type">' + escapeHtml(entry.propType || "field") + '</span>' +
      '</button>'
    )).join("");
    results.hidden = false;
    activeIndex = 0;
  }

  function clearHighlight() {
    shadow.querySelectorAll(".ks-search-hit").forEach((element) => {
      element.classList.remove("ks-search-hit");
    });
  }

  function revealNode(node) {
    let current = node;
    while (current && current !== root) {
      if (current.tagName === "DETAILS") {
        current.open = true;
      }
      current = current.parentElement;
    }
  }

  function setHash(path) {
    const nextHash = "#" + encodeURIComponent(path);
    if (window.location.hash === nextHash) {
      return;
    }
    if (window.history && typeof window.history.replaceState === "function") {
      window.history.replaceState(null, "", nextHash);
      return;
    }
    window.location.hash = path;
  }

  function selectEntry(entry, options = {}) {
    const { updateHash = false, scroll = true } = options;
    const node = nodeByID.get(entry.nodeId);
    if (!node) {
      return;
    }
    revealNode(node);
    clearHighlight();
    node.classList.add("ks-search-hit");
    window.clearTimeout(highlightTimer);
    highlightTimer = window.setTimeout(clearHighlight, 1800);
    if (scroll) {
      node.scrollIntoView({ behavior: "smooth", block: "center" });
    }
    input.value = entry.path;
    hideResults();
    if (updateHash) {
      setHash(entry.path);
    }
  }

  function selectHashTarget() {
    const rawHash = window.location.hash.replace(/^#/, "");
    if (!rawHash) {
      return;
    }
    const entry = entryByPath.get(normalize(decodeURIComponent(rawHash)));
    if (entry) {
      selectEntry(entry);
    }
  }

  function toggleSubtree(summary, includeDescriptions) {
    const detail = summary.closest("details");
    if (!detail) {
      return;
    }
    const subtree = [detail, ...detail.querySelectorAll("details")];
    const toggled = subtree.filter((candidate) => (
      includeDescriptions || candidate.dataset.ksHasChildren === "true"
    ));
    if (toggled.length === 0) {
      return;
    }
    const shouldOpen = toggled.some((candidate) => !candidate.open);
    if (!includeDescriptions) {
      subtree.forEach((candidate) => {
        if (candidate.dataset.ksHasChildren !== "true") {
          candidate.open = false;
          candidate.classList.remove("ks-hide-desc");
        }
      });
    }
    toggled.forEach((candidate) => {
      candidate.open = shouldOpen;
      if (includeDescriptions) {
        candidate.classList.remove("ks-hide-desc");
        return;
      }
      candidate.classList.toggle("ks-hide-desc", shouldOpen);
    });
  }

  input.addEventListener("input", renderMatches);
  input.addEventListener("focus", () => {
    if (input.value.trim()) {
      renderMatches();
    }
  });
  input.addEventListener("keydown", (event) => {
    if (results.hidden || matches.length === 0) {
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActiveIndex((activeIndex + 1) %% matches.length);
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      setActiveIndex((activeIndex - 1 + matches.length) %% matches.length);
      return;
    }
    if (event.key === "Enter" && activeIndex >= 0) {
      event.preventDefault();
      selectEntry(matches[activeIndex]);
      return;
    }
    if (event.key === "Escape") {
      hideResults();
    }
  });

  results.addEventListener("mousedown", (event) => {
    event.preventDefault();
  });
  results.addEventListener("click", (event) => {
    const target = event.target;
    const button = target instanceof Element ? target.closest("[data-node-id]") : null;
    if (!button) {
      return;
    }
    const entry = matches.find((candidate) => candidate.nodeId === button.getAttribute("data-node-id"));
    if (entry) {
      selectEntry(entry);
    }
  });

  document.addEventListener("click", (event) => {
    if (!host.contains(event.target)) {
      hideResults();
    }
  });
  root.addEventListener("click", (event) => {
    const target = event.target;
    const selectable = target instanceof Element ? target.closest(".ks-summary, .ks-leaf-line") : null;
    if (!selectable || !root.contains(selectable)) {
      return;
    }
    const path = selectable.getAttribute("data-ks-path");
    if (!path) {
      return;
    }
    if (event.ctrlKey || event.metaKey) {
      return;
    }
    setHash(path);
  });
  root.addEventListener("click", (event) => {
    const target = event.target;
    const summary = target instanceof Element ? target.closest("summary.ks-summary") : null;
    if (!summary || !root.contains(summary)) {
      return;
    }
    if (!event.ctrlKey && !event.metaKey) {
      return;
    }
    event.preventDefault();
    toggleSubtree(summary, event.shiftKey);
  });
  window.addEventListener("hashchange", selectHashTarget);
  selectHashTarget();
})();
</script>`,
		strconv.Quote(hostID),
		strconv.Quote(templateID),
		entries.String(),
	)
	return b.String()
}

func renderWidget(kind, group, version, scope string, pm *propertyMap, widgetID string) string {
	apiVersion := version
	if group != "" {
		apiVersion = group + "/" + version
	}

	var b strings.Builder
	searchIndex := make([]searchEntry, 0, 64)
	nodeCounter := 0
	templateID := widgetID + "-template"
	fmt.Fprintf(&b, "<!-- kubespec widget: %s (%s) -->\n", esc(kind), esc(apiVersion))
	fmt.Fprintf(&b, `<div id="%s"></div>`+"\n", esc(widgetID))
	fmt.Fprintf(&b, `<template id="%s">`+"\n", esc(templateID))
	b.WriteString("<style>\n" + css + "\n</style>\n")
	b.WriteString(`<div class="ks-schema">` + "\n")
	b.WriteString(`<div class="ks-header">` + "\n")
	fmt.Fprintf(&b, `  <div class="ks-apiversion">%s</div>`+"\n", esc(apiVersion))
	fmt.Fprintf(&b, `  <h2 class="ks-kind">%s</h2>`+"\n", esc(kind))
	if pm.description != "" {
		fmt.Fprintf(&b, `  <div class="ks-resource-desc">%s</div>`+"\n", esc(pm.description))
	}
	fmt.Fprintf(&b, `  <div class="ks-search"><input class="ks-search-input" type="search" placeholder="Search fields like spec.template.spec.containers" autocomplete="off" spellcheck="false" aria-label="Search schema fields" data-ks-search-input /><div class="ks-search-results" data-ks-search-results hidden></div></div>`+"\n")
	b.WriteString("</div>\n")
	renderTree(pm, scope, 0, "", widgetID, &nodeCounter, &searchIndex, &b)
	b.WriteString("\n")
	b.WriteString("</div>\n")
	b.WriteString("</template>\n")
	b.WriteString(renderSearchScript(widgetID, templateID, searchIndex) + "\n")
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

func renderJSONSchemaWidget(doc crdDocument, widgetID string) (string, bool) {
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
	return renderWidget(title, "", schemaVersion, "Schema", pm, widgetID), true
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
			widgetID := fmt.Sprintf("ks-widget-%d", len(widgets)+1)
			if widget, ok := renderJSONSchemaWidget(doc, widgetID); ok {
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
			widgetID := fmt.Sprintf("ks-widget-%d", len(widgets)+1)
			widgets = append(widgets, renderWidget(crdKind, group, verName, scope, pm, widgetID))
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
