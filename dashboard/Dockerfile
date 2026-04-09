# dashboard/Dockerfile
#
# Two-stage build:
#   Stage 1 (builder) — Node 20 Alpine; installs deps and runs ng build --configuration production
#   Stage 2 (runtime) — Nginx Alpine; serves the built artefact
#
# Build args:
#   COORDINATOR_HOST  (default: coordinator)   — used by nginx.conf upstream block
#   COORDINATOR_PORT  (default: 8080)

# ── Stage 1: Build ────────────────────────────────────────────────────────────
FROM node:20-alpine AS builder

WORKDIR /app

# Install dependencies first (layer-cached unless package.json changes)
COPY package.json package-lock.json ./
RUN npm ci --prefer-offline --no-audit --no-fund

# Copy source
COPY . .

# Production build (output to dist/helion-dashboard)
RUN npm run build:prod

# ── Stage 2: Serve ────────────────────────────────────────────────────────────
FROM nginx:1.27-alpine AS runtime

ARG COORDINATOR_HOST=coordinator
ARG COORDINATOR_PORT=8080
ENV COORDINATOR_HOST=${COORDINATOR_HOST}
ENV COORDINATOR_PORT=${COORDINATOR_PORT}

# Remove default Nginx content
RUN rm -rf /usr/share/nginx/html/*

# Copy built Angular app
COPY --from=builder /app/dist/helion-dashboard/browser /usr/share/nginx/html

# Copy Nginx config (uses envsubst to substitute $COORDINATOR_HOST/$COORDINATOR_PORT)
COPY nginx.conf /etc/nginx/nginx.conf.template

# Entrypoint: substitute env vars into nginx.conf, then start Nginx
CMD ["/bin/sh", "-c", \
  "envsubst '${COORDINATOR_HOST} ${COORDINATOR_PORT}' < /etc/nginx/nginx.conf.template > /etc/nginx/nginx.conf && nginx -g 'daemon off;'"]

EXPOSE 80

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://localhost/healthz || exit 1
