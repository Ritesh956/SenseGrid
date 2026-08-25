# control gets its own Dockerfile (rather than the generic go.Dockerfile)
# because, unlike most other services, it also serves the PWA sensor
# client's static files and runs the schema migrations. Build context is
# the repo root.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/control ./cmd/control

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/control /app
COPY --chown=nonroot:nonroot web/sensor-client /webroot
COPY --chown=nonroot:nonroot deploy/migrations /migrations
USER nonroot:nonroot
ENV WEBROOT=/webroot
ENV MIGRATIONS_DIR=/migrations
ENTRYPOINT ["/app"]
