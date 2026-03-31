FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/kelos-webhook-server .
USER 65532:65532
ENTRYPOINT ["/kelos-webhook-server"]