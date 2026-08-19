# Multi-stage build: compile a static Go binary, then ship it in a minimal
# runtime image. The build stage needs normal internet access to fetch Go
# modules on the first build in a fresh environment (no local module cache
# yet) — that's expected to happen wherever this image is built (your
# machine, CI, or the VPS itself), not inside any restricted/offline
# environment.

FROM golang:1.25-bookworm AS build
WORKDIR /src

# go.sum is checked into the repo, so `go mod download` resolves exactly
# the versions it pins rather than `go mod tidy` re-resolving (and
# potentially drifting) on every build. Copying go.mod/go.sum before the
# rest of the source also lets Docker cache this layer across builds that
# only change application code, not dependencies.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
