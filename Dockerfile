# Stage 1: compile. The Go toolchain is only in this stage.
FROM golang:1.22-alpine AS build
WORKDIR /src
# Download modules first so this layer is cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO off = a fully static binary that runs on an image with no libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/uptime-monitor ./cmd/uptime-monitor

# Stage 2: run. distroless/static has CA certificates and tzdata but no shell
# or package manager, so there is very little to attack or patch.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/uptime-monitor /uptime-monitor
COPY sites.yaml /etc/uptime-monitor/sites.yaml
ENV CONFIG_PATH=/etc/uptime-monitor/sites.yaml
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/uptime-monitor"]
CMD ["run"]
