# Kubernetes Spec Explorer

👉 Live at https://kubespec.dev

- Tree view of all Kubernetes resources
- History changes since Kubernetes v1.12
- Examples that you can use copy and modify
- Links to official Kubernetes documentation and useful resources
- Support for popular CRDs

![](./screenshot.png)

## Including API docs in Hugo (or any static site)

The `render` script generates a self-contained HTML fragment for any API resource. The fragment embeds all its CSS (no external dependencies, no JavaScript required) so you can paste it directly into a Hugo template, shortcode, or content page.

**Kubernetes built-in resources**

```bash
npm run render -- kubernetes v1/Pod --output pod.html
npm run render -- kubernetes apps/v1/Deployment --output deployment.html
```

**CRD-based projects**

```bash
npm run render -- cert-manager cert-manager.io/v1/Certificate --output certificate.html
npm run render -- gateway-api gateway.networking.k8s.io/v1/HTTPRoute --output httproute.html
```

The output is a `<div class="ks-schema">…</div>` fragment. To include it in Hugo, pipe it into a [shortcode](https://gohugo.io/templates/shortcode-templates/) or use Hugo's [`readFile`](https://gohugo.io/functions/os/readfile/):

```go-html-template
{{ readFile "content/api/pod.html" | safeHTML }}
```

![kubespec widget rendered inline in a page](https://github.com/user-attachments/assets/ebc4107c-ca8c-4530-89be-fe244c3ba88e)

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
