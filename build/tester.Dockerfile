# Image for cmd/tester, which searches for the client count with the most
# HammerDB TPROC-C or pgbench throughput.
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/tester ./cmd/tester

FROM tpcorg/hammerdb:v6.1.1-postgres AS hammerdb

FROM postgres:18
COPY --from=hammerdb /home/hammerdb /opt/hammerdb
COPY --from=build /out/tester /usr/local/bin/tester
ENTRYPOINT ["tester"]
