# Atrium's website

The marketing site at <https://zvibaratz.github.io/atrium/> — a single Next.js
page with the tagline, the two install commands and the prerequisites. It is not
part of the `atrium` binary.

```bash
cd web && npm install   # once
npm run dev             # http://localhost:3000
npm run build           # static export into web/out
```

`.github/workflows/deploy-pages.yml` builds this directory and deploys `web/out`
to GitHub Pages on every push to `main` that touches `web/**`; pull requests
build it without deploying.

It has its own npm toolchain and no Go in it, so the repo's gate leaves it alone:
`go test ./...` finds no package here, and `just fmt-check` filters `web/` out of
gofmt's walk. One thing does reach in — `TestSupportedAgentsAreNamedInTheDocs`, in
the repository root, fails when an adapter in `session/agent` goes unnamed by
`src/app/page.tsx` or `src/app/layout.tsx`, so the page's agent list follows the
registry rather than drifting from it.
