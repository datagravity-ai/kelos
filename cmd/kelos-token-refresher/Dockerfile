FROM gcr.io/distroless/static:nonroot
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-token-refresher-linux-${TARGETARCH} kelos-token-refresher
USER 65532:65532
ENTRYPOINT ["/kelos-token-refresher"]
