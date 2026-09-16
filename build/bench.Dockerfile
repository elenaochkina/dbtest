# Image for cmd/bench, the containerized load generator.
#
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/bench ./cmd/bench

FROM postgres:16
COPY --from=build /out/bench /usr/local/bin/bench
ENTRYPOINT ["bench"]
