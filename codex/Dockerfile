ARG BASE_IMAGE=agent-base:latest
FROM ${BASE_IMAGE}

ARG CODEX_VERSION=0.116.0
RUN npm install -g @openai/codex@${CODEX_VERSION}

COPY codex/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.codex && chown -R agent:agent /home/agent

USER agent

ENV GOPATH="/home/agent/go"
ENV PATH="${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["codex"]
