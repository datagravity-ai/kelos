ARG BASE_IMAGE=agent-base:latest
FROM ${BASE_IMAGE}

ARG GEMINI_CLI_VERSION=0.35.0
RUN npm install -g @google/gemini-cli@${GEMINI_CLI_VERSION}

COPY gemini/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

RUN useradd -u 61100 -m -s /bin/bash agent
RUN mkdir -p /home/agent/.gemini && chown -R agent:agent /home/agent

USER agent

ENV GOPATH="/home/agent/go"
ENV PATH="${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["gemini"]
