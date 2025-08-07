ARG LIBEEN_IMAGE=harbor.eencloud.com/vms/libeen:alpine-0.0.136
ARG GOLANG_IMAGE=harbor.eencloud.com/dockerhub_proxy/library/golang:1.23-alpine
ARG GOEEN_IMAGE=harbor.eencloud.com/vms/goeen:1.0.333

# ===== Goeen Source Stage =====
FROM ${GOEEN_IMAGE} AS goeen

# ===== Builder Stage =====
FROM ${LIBEEN_IMAGE} AS builder

# Copy golang from separate image
FROM ${GOLANG_IMAGE} AS golang
FROM builder AS build_stage

COPY --from=golang /usr/local/go/ /usr/local/go/
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH=/go
ENV CGO_ENABLED=0

RUN apk add --no-cache bash git make

# ===== Test Stage =====
FROM build_stage AS test
WORKDIR /usr/src/app
COPY --from=goeen /usr/src/app/go/src/github.com/eencloud/goeen ./goeen
COPY . ./bridge-devices-pos

WORKDIR /usr/src/app/bridge-devices-pos
RUN cat path_replacements.txt >> go.mod
RUN go mod tidy
CMD ["go", "test", "-v", "./..."]

# ===== Production Build Stage =====
FROM build_stage AS build
ARG build_tags
WORKDIR /usr/src/app
COPY --from=goeen /usr/src/app/go/src/github.com/eencloud/goeen ./goeen
COPY . ./bridge-devices-pos

WORKDIR /usr/src/app/bridge-devices-pos
RUN grep -v "replace github.com/eencloud/goeen" go.mod > go.mod.tmp && mv go.mod.tmp go.mod && cat path_replacements.txt >> go.mod
RUN go mod tidy
RUN go build -tags="${build_tags}" -buildvcs=false -o bridge-devices-pos ./cmd/bridge-devices-pos/

# ===== Final Production Image =====
FROM ${LIBEEN_IMAGE} AS production
ARG build_tags

# Install tini and supervisor for proper process management
RUN apk add --no-cache tini supervisor

# Create directories
RUN mkdir -p /opt/een/app/point_of_sale \
    /opt/een/data/point_of_sale \
    /opt/een/var/log/point_of_sale

WORKDIR /opt/een/app/point_of_sale

# Copy the compiled binary
COPY --from=build /usr/src/app/bridge-devices-pos/bridge-devices-pos .

# Copy data directory
COPY --from=build /usr/src/app/bridge-devices-pos/data ./data

# Copy the bridge Lua script
COPY --from=build /usr/src/app/bridge-devices-pos/internal/bridge ./internal/bridge

# Copy configuration files
COPY supervisord.conf /etc/supervisord.conf
COPY scripts/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

# Use tini as init system and supervisord for process management
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["./entrypoint.sh"] 