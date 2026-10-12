# SPDX-License-Identifier: Apache-2.0
# Builds the documentation site (mkdocs.yml and docs/) and serves it with nginx on port 8080.
FROM python:3.12-slim AS build
WORKDIR /src
RUN pip install --no-cache-dir mkdocs-material==9.7.7
COPY mkdocs.yml ./
COPY docs ./docs
RUN mkdocs build --site-dir /out

FROM nginx:alpine
RUN printf 'server {\n  listen 8080;\n  server_tokens off;\n  root /usr/share/nginx/html;\n  error_page 404 /404.html;\n  location = /healthz { default_type text/plain; return 200 "ok\\n"; }\n  add_header X-Content-Type-Options nosniff always;\n  add_header Referrer-Policy strict-origin-when-cross-origin always;\n}\n' > /etc/nginx/conf.d/default.conf
COPY --from=build /out /usr/share/nginx/html
EXPOSE 8080
