# Kubernetes Spec Explorer

👉 Live at https://kubespec.dev

- Tree view of all Kubernetes resources
- History changes since Kubernetes v1.12
- Examples that you can use copy and modify
- Links to official Kubernetes documentation and useful resources
- Support for popular CRDs

![](./screenshot.png)

## Embedding in Hugo (or any static site)

Every API resource has a JavaScript-free embeddable page at `/embed/{project}/{group}/{version}/{kind}`.

For Kubernetes built-in resources:

```html
<iframe src="https://kubespec.dev/embed/kubernetes/v1/Pod"
        style="width:100%;border:none;min-height:600px"></iframe>
```

For CRD-based projects (e.g. cert-manager):

```html
<iframe src="https://kubespec.dev/embed/cert-manager/cert-manager.io/v1/Certificate"
        style="width:100%;border:none;min-height:600px"></iframe>
```

The embed pages use only HTML `<details>`/`<summary>` elements — no JavaScript required.

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
