FROM busybox:1.37
ARG TARGETARCH
WORKDIR /
COPY bin/kelos-session-runner-linux-${TARGETARCH} kelos-session-runner
USER 61100:61100
ENTRYPOINT ["/kelos-session-runner"]
