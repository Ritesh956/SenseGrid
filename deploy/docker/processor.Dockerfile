# processor gets its own Dockerfile (rather than the generic go.Dockerfile)
# because it also runs the schema migrations at startup and needs
# deploy/migrations on disk to do it — see internal/migrations. Phase 3
# adds deploy/rules.yaml the same way: baked in as the image's default
# ruleset, with docker-compose.yml bind-mounting over it so editing the
# host file demonstrates hot-reload without a rebuild. Build context is the
# repo root.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/processor ./cmd/processor

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/processor /app
COPY --chown=nonroot:nonroot deploy/migrations /migrations
COPY --chown=nonroot:nonroot deploy/rules.yaml /rules/rules.yaml
USER nonroot:nonroot
ENV MIGRATIONS_DIR=/migrations
ENV RULES_FILE=/rules/rules.yaml
ENTRYPOINT ["/app"]
