# Multi-stage build: compile a static Go binary, then ship it in a minimal
# runtime image. The build stage needs normal internet access to fetch Go
# modules (go.sum pins exact versions) — that's expected to happen wherever
# this image is built (your machine, CI, or the VPS itself), not inside any
# restricted/offline environment.

FROM golang:1.24-bookworm AS build
WORKDIR /src

# go.sum isn't checked in yet (see README "First build" — it has to be
# generated somewhere with normal internet access, which this build stage
# has). `go mod tidy` resolves versions and writes go.sum; once you've run
# a build once, commit the resulting go.sum and switch this to
# `COPY go.mod go.sum ./` + `go mod download` for fully reproducible builds.
COPY go.mod ./
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o /out/server ./cmd/server

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
