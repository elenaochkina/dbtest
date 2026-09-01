# Image for cmd/probe, the availability prober.
#
# Unlike bench it needs no pgbench, so the runtime is a static base rather than
# postgres:16 — a couple of MB instead of ~680. distroless/static carries CA
# certificates, which RDS needs for TLS.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/probe ./cmd/probe

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/probe /usr/local/bin/probe
ENTRYPOINT ["probe"]
