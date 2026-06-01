FROM node:22-alpine AS builder

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .

# Build metadata surfaced on the dashboard's System page. next.config.ts
# maps these to NEXT_PUBLIC_DASHBOARD_* (falling back to package.json
# version when unset). Supplied by the build; see docs/reference/configuration.md.
ARG VERSION=dev
ARG BUILD_DATE=
ENV DASHBOARD_VERSION=$VERSION
ENV DASHBOARD_BUILD_DATE=$BUILD_DATE
RUN npm run build

FROM node:22-alpine
RUN apk upgrade --no-cache
WORKDIR /app
COPY --from=builder --chown=node:node /app/.next/standalone ./
COPY --from=builder --chown=node:node /app/.next/static ./.next/static
COPY --from=builder --chown=node:node /app/public ./public

EXPOSE 3000
ENV PORT=3000
ENV HOSTNAME="0.0.0.0"
USER node
CMD ["node", "server.js"]
