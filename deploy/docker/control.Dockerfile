# control gets its own Dockerfile (rather than the generic go.Dockerfile)
# because, unlike the other services, it also serves the PWA sensor
# client's static files. Build context is the repo root.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/control ./cmd/control

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/control /app
COPY --chown=nonroot:nonroot web/sensor-client /webroot
USER nonroot:nonroot
ENV WEBROOT=/webroot
ENTRYPOINT ["/app"]
