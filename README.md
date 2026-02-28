# Kubernetes Spec Explorer

👉 Live at https://kubespec.dev

- Tree view of all Kubernetes resources
- History changes since Kubernetes v1.12
- Examples that you can use copy and modify
- Links to official Kubernetes documentation and useful resources
- Support for popular CRDs

![](./screenshot.png)

## Generating API docs for Hugo (or any static site)

The `render` scripts generate self-contained HTML widgets from any CRD YAML file.
The output embeds all CSS inline (no external dependencies, no JavaScript, no iframe)
so you can paste it directly into Hugo content, shortcodes, or templates.

Four language variants are available — all produce identical output and accept the same flags:

| Script | Runtime | Dependency |
|---|---|---|
| `scripts/render.ts` | Node.js ≥ 18 + tsx | `npm install yaml` |
| `scripts/render.jsx` | Node.js ≥ 18 + tsx | `npm install yaml react react-dom` |
| `scripts/render.py` | Python ≥ 3.8 | `pip install pyyaml` |
| `scripts/render.go` | Go ≥ 1.21 | `cd scripts && go mod download` |

**Run against any CRD YAML:**

```bash
# TypeScript
npx tsx scripts/render.ts my-crd.yaml --output my-crd.html

# JSX / React
npx tsx scripts/render.jsx my-crd.yaml --output my-crd.html

# Python
python scripts/render.py my-crd.yaml --output my-crd.html

# Go (flags must precede the file path)
cd scripts && go run render.go -output ../my-crd.html ../my-crd.yaml
```

All scripts accept:
- `--output <file>` (or `-output` for Go) — write to a file instead of stdout
- `--version <v1>` (or `-version` for Go) — render only a specific schema version

Multi-document YAML bundles (e.g. cert-manager's install manifest) are supported;
non-CRD documents are silently skipped.

**Include in Hugo** with [`readFile`](https://gohugo.io/functions/os/readfile/):

```go-html-template
{{ readFile "content/api/my-crd.html" | safeHTML }}
```

## Contributing

Contributions are welcome!

- clone the repo
- run `npm install`
- run `npm run dev`

## 📃 License

MIT

## ❤️ Sponsored by

<a href="https://aptakube.com">
    <img src="https://aptakube.com/og.png" alt="Aptakube">
</a>
