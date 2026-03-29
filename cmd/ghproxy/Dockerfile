FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/ghproxy .
USER 65532:65532
ENTRYPOINT ["/ghproxy"]
