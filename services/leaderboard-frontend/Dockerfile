# ── Build stage ──────────────────────────────────────────────────────────────
FROM node:20-bookworm-slim AS builder

WORKDIR /app

# Install dependencies first for layer caching
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts

# Copy source and build
COPY . .
RUN npm run build

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM nginx:1.27-alpine

# Remove default nginx static files
RUN rm -rf /usr/share/nginx/html/*

# Copy built SPA
COPY --from=builder /app/dist /usr/share/nginx/html

# Nginx config: SPA fallback + proxy /api to leaderboard-service
RUN printf 'server {\n\
  listen 3000;\n\
  root /usr/share/nginx/html;\n\
  index index.html;\n\
\n\
  # REST API proxy — strips /api prefix before forwarding\n\
  location /api/ {\n\
    proxy_pass http://leaderboard-service:8082/;\n\
    proxy_set_header Host $host;\n\
    proxy_set_header X-Real-IP $remote_addr;\n\
    proxy_read_timeout 30s;\n\
  }\n\
\n\
  # WebSocket proxy — /api/v1/leaderboard/ws -> /v1/ws/leaderboard\n\
  location /api/v1/leaderboard/ws {\n\
    proxy_pass http://leaderboard-service:8082/v1/ws/leaderboard;\n\
    proxy_http_version 1.1;\n\
    proxy_set_header Upgrade $http_upgrade;\n\
    proxy_set_header Connection "Upgrade";\n\
    proxy_set_header Host $host;\n\
    proxy_read_timeout 3600s;\n\
  }\n\
\n\
  # SPA fallback\n\
  location / {\n\
    try_files $uri $uri/ /index.html;\n\
  }\n\
}\n' \
    > /etc/nginx/conf.d/default.conf

EXPOSE 3000

CMD ["nginx", "-g", "daemon off;"]
