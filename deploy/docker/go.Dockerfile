# Generic multi-stage build for every SenseGrid Go binary. Build context is
# the repo root; pass the binary's cmd/ directory name as SERVICE, e.g.:
#   docker build -f deploy/docker/go.Dockerfile --build-arg SERVICE=ingest .
FROM golang:1.24-alpine AS build
ARG SERVICE
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/app ./cmd/${SERVICE}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
