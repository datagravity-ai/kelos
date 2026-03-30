ARG BASE_IMAGE=agent-base:latest
FROM ${BASE_IMAGE}

ARG OPENCODE_VERSION=1.3.2
RUN npm install -g opencode-ai@${OPENCODE_VERSION}

COPY opencode/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.opencode && chown -R agent:agent /home/agent

USER agent

ENV GOPATH="/home/agent/go"
ENV PATH="${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["opencode"]
