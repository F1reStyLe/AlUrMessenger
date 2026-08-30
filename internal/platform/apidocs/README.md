# Vendored Swagger UI

Official npm `swagger-ui-dist` **5.32.14**, pinned tarball SHA-512 in
`scripts/vendor-swagger.py`. Only bundle/CSS and upstream licenses are copied.
Sources: [distribution](https://swagger.io/docs/open-source-tools/swagger-ui/usage/installation/),
[configuration](https://swagger.io/docs/open-source-tools/swagger-ui/usage/configuration/).

Run `python scripts/vendor-swagger.py` to reproduce vendor files. No Node/npm/CDN
dependency at application startup or Go/Docker build. Preserve LICENSE, NOTICE and
bundle dependency notices on upgrades. Our HTML/initializer are separate files.

Public docs at `/docs/api`; no anonymous access to protected endpoints. Production
ingress may restrict docs independently. CSP limits requests to self; remote validator,
query-config overrides, cookies and persistent authorization are disabled. Users may
paste an access JWT into Authorize, but must never share a screenshot containing it.
