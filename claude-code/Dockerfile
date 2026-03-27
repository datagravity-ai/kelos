ARG BASE_IMAGE=agent-base:latest
FROM ${BASE_IMAGE}

ARG CLAUDE_CODE_VERSION=2.1.81
RUN npm install -g @anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}

COPY claude-code/kelos_entrypoint.sh /kelos_entrypoint.sh
RUN chmod +x /kelos_entrypoint.sh

RUN useradd -u 61100 -m -s /bin/bash claude
RUN mkdir -p /home/claude/.claude && chown -R claude:claude /home/claude

USER claude

ENV GOPATH="/home/claude/go"
ENV PATH="${GOPATH}/bin:${PATH}"

WORKDIR /workspace

ENTRYPOINT ["claude"]
