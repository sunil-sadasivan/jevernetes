# Supply approved, digest-pinned Rust 1.94+ and Debian bookworm-slim-compatible images.
# No registry/release is selected by this repository.
ARG RUST_IMAGE
ARG RUNTIME_IMAGE
FROM ${RUST_IMAGE} AS build
WORKDIR /build
COPY Cargo.toml Cargo.lock ./
COPY src ./src
COPY tests/rust ./tests/rust
RUN cargo build --locked --release

FROM ${RUNTIME_IMAGE} AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /var/lib/jevernetes && chown 65532:65532 /var/lib/jevernetes
COPY --from=build /build/target/release/jevernetes /usr/local/bin/jevernetes
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/jevernetes"]
