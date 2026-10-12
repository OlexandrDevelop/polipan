# SPDX-License-Identifier: Apache-2.0
FROM node:20-alpine AS build
WORKDIR /site
COPY site.config.json build.mjs ./
COPY assets ./assets
COPY i18n ./i18n
RUN node build.mjs

FROM nginx:alpine
# The short install address and the repository. Change them with environment variables.
ENV SITE_GET_HOST=get.polipan.dev \
    SITE_REPO=polipan/polipan
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /site/dist /usr/share/nginx/html
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
