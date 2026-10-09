FROM alpine:3.22 AS scalar-bundle
RUN apk add --no-cache curl
RUN curl --fail --location --silent --show-error \
      https://registry.npmjs.org/@scalar/api-reference/-/api-reference-1.72.2.tgz \
      -o /tmp/scalar.tgz \
    && echo '22228eb8d80a8f59f57eed66a3c7a3aa9bbdedc15eda379c289b88c70992e1c3  /tmp/scalar.tgz' | sha256sum -c - \
    && mkdir /scalar \
    && tar -xzf /tmp/scalar.tgz -C /scalar package/dist/browser/standalone.js

FROM nginxinc/nginx-unprivileged:1.29-alpine
ENV GATEWAY_URL=http://gateway:8080
COPY --from=scalar-bundle /scalar/package/dist/browser/standalone.js /usr/share/nginx/html/scalar.js
COPY api/http/index.html api/http/openapi.yaml /usr/share/nginx/html/
COPY api/http/nginx.conf /etc/nginx/templates/default.conf.template
