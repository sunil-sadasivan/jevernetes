# Supply operator-approved digest-pinned Go 1.25+ and Debian-compatible base images.
ARG GO_IMAGE
ARG RUNTIME_IMAGE
FROM ${GO_IMAGE} AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/jevernetes ./cmd/jevernetes

FROM ${RUNTIME_IMAGE} AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /var/lib/jevernetes && chown 65532:65532 /var/lib/jevernetes \
    && chmod 700 /var/lib/jevernetes
COPY --from=build /out/jevernetes /usr/local/bin/jevernetes
RUN ln -s /usr/local/bin/jevernetes /usr/local/bin/jev
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/jevernetes"]
