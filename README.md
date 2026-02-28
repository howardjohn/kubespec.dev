# Kubernetes Spec Explorer

👉 Live at https://kubespec.dev

- Tree view of all Kubernetes resources
- History changes since Kubernetes v1.12
- Examples that you can use copy and modify
- Links to official Kubernetes documentation and useful resources
- Support for popular CRDs

![](./screenshot.png)

## Generating API docs for Hugo (or any static site)

The `render` script generates self-contained HTML widgets from any CRD YAML file. The output embeds all CSS inline (no external dependencies, no JavaScript, no iframe) so you can paste it directly into Hugo content, shortcodes, or templates.

**Install the only runtime dependency:**

```bash
npm install yaml   # already present if you have a Node.js project
```

**Run against any CRD YAML:**

```bash
# Single widget from a single-CRD file
npx tsx scripts/render.ts my-crd.yaml --output my-crd.html

# All CRDs from a multi-resource YAML (e.g. cert-manager bundle)
npx tsx scripts/render.ts cert-manager.yaml --output cert-manager.html

# Restrict to a specific schema version
npx tsx scripts/render.ts gateway-api-crds.yaml --version v1 --output httproute.html

# Pipe to stdout
npx tsx scripts/render.ts my-crd.yaml
```

The script is entirely standalone — it only needs Node.js ≥ 18 and the `yaml` npm package. No knowledge of this repository's structure is required.

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
