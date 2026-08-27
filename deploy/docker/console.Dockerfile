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

# Phase 8: the runtime stage doesn't need npm/yarn/corepack or a shell —
# next build's standalone output is just `node server.js` — but node:22-alpine
# ships all of that anyway, and Trivy's scan found a CRITICAL (node-tar,
# CVE-2026-59873) plus several HIGHs sitting in npm's own bundled deps
# inside that base image. None of it is reachable (nothing in this image
# ever invokes npm/yarn/npx), but distroless's nodejs image sidesteps the
# whole class the same way every Go service here already does, rather than
# relying on "unreachable" as the argument. debian13 specifically, not
# debian12: the debian12 distroless variant's own OpenSSL was still behind
# (CVE-2026-31789, a CRITICAL heap overflow) — debian13 doesn't carry it,
# and this one's real (Node's own https client uses it for the BFF's
# outbound calls to cmd/control, unlike the upstream Go-binary findings in
# timescaledb's image, which nothing here ever executes).
FROM gcr.io/distroless/nodejs22-debian13:nonroot
WORKDIR /app
ENV NODE_ENV=production
COPY --from=build --chown=nonroot:nonroot /src/.next/standalone ./
COPY --from=build --chown=nonroot:nonroot /src/.next/static ./.next/static
COPY --from=build --chown=nonroot:nonroot /src/public ./public
EXPOSE 3000
CMD ["server.js"]
