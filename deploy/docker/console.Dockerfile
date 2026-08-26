# web/console (Phase 5) — Next.js standalone output (next.config.ts) keeps
# the runtime image small: only .next/standalone + .next/static are
# copied, not the full node_modules tree. Build context is the repo root,
# matching every other SenseGrid Dockerfile.
FROM node:22-alpine AS build
WORKDIR /src
COPY web/console/package.json web/console/package-lock.json* ./
RUN npm install
COPY web/console .
# NEXT_PUBLIC_* vars are inlined into the client bundle by `next build`
# itself — setting NEXT_PUBLIC_CONTROL_WS_URL as a container *runtime* env
# var (docker-compose's environment:) would silently do nothing, since the
# bundle is already built by then. It has to be a build arg instead.
ARG NEXT_PUBLIC_CONTROL_WS_URL
ENV NEXT_PUBLIC_CONTROL_WS_URL=$NEXT_PUBLIC_CONTROL_WS_URL
RUN npm run build

FROM node:22-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build /src/.next/standalone ./
COPY --from=build /src/.next/static ./.next/static
COPY --from=build /src/public ./public
USER node
EXPOSE 3000
CMD ["node", "server.js"]
