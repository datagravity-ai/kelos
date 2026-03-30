ARG BASE_IMAGE=agent-base:latest
FROM ${BASE_IMAGE}

COPY cursor/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.cursor && chown -R agent:agent /home/agent

USER agent

ARG CURSOR_CLI_VERSION=2026.03.25-933d5a6
RUN ARCH=$(uname -m) \
    && case "$ARCH" in x86_64) ARCH="x64" ;; aarch64) ARCH="arm64" ;; esac \
    && INSTALL_DIR="$HOME/.local/share/cursor-agent/versions/${CURSOR_CLI_VERSION}" \
    && mkdir -p "$INSTALL_DIR" "$HOME/.local/bin" \
    && curl -fsSL "https://downloads.cursor.com/lab/${CURSOR_CLI_VERSION}/linux/${ARCH}/agent-cli-package.tar.gz" \
       | tar --strip-components=1 -xz -C "$INSTALL_DIR" \
    && ln -sf "$INSTALL_DIR/cursor-agent" "$HOME/.local/bin/agent" \
    && ln -sf "$INSTALL_DIR/cursor-agent" "$HOME/.local/bin/cursor-agent"

ENV GOPATH="/home/agent/go"
ENV PATH="/home/agent/.local/bin:${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["agent"]
