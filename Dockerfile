# ABOUTME: Container image that serves "mbx mcp --http" for remote MCP clients behind a tunnel.
# ABOUTME: Builds a static mbx in a Go stage and runs it as a non-root user with state in /data.

FROM docker.io/library/golang:1.27.3-trixie@sha256:1896654cfc5c2b5ad74c07f29cb36f5d2d1cbed42e3054070fbfe444e521e83c AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Release builds set the version, for example --build-arg MBX_VERSION=v1.2.3.
ARG MBX_VERSION=""
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X github.com/hy26v/unofficial-mailboxorg-api-client/internal/cli.version=${MBX_VERSION}" \
      -o /out/mbx ./cmd/mbx \
 && mkdir -m 700 /out/data

# The static distroless image has CA certificates and time zone data, but no shell or
# package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087

COPY --from=build /out/mbx /usr/local/bin/mbx
# A new named volume takes the owner of this directory, so the non-root user can write it.
COPY --from=build --chown=65532:65532 /out/data /data

# config.toml, session.json, request-guard.json and mcp-oauth.json all live in MBX_HOME.
ENV MBX_HOME=/data
WORKDIR /data
USER 65532:65532
EXPOSE 8808
VOLUME ["/data"]

# The tunnel or proxy reaches the server through the container network, so it listens on
# every container interface. Publish the port on the host only on 127.0.0.1.
ENTRYPOINT ["/usr/local/bin/mbx"]
CMD ["mcp", "--http", "0.0.0.0:8808", "--public-url", "http://localhost:8808"]
