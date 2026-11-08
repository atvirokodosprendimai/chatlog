# SPDX-License-Identifier: AGPL-3.0-only
# Copyright (C) 2026 Atviro kodo sprendimai
#
# The release image: the static binary built by scripts/release-build.sh (dist/chatlog-linux-<arch>) on a distroless
# base, run as a non-root user. Built by .github/workflows/release.yml; locally:
#   sh scripts/release-build.sh dev dist && docker build -t chatlog .
# Run as Claude Code's MCP server, the history and the transcripts mounted, as your own user (the data dir is mode
# 0700, so the image's default user 65532 cannot open it):
#   docker run -i --rm --user "$(id -u):$(id -g)" -e HOME=/h -v ~/.local/share/chatlog:/data \
#     -v ~/.claude/projects:/h/.claude/projects:ro ghcr.io/atvirokodosprendimai/chatlog mcp
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG TARGETARCH
ARG VERSION=dev
LABEL org.opencontainers.image.title="chatlog" \
      org.opencontainers.image.description="The record of what you and Claude Code said and did, searchable, recalled through MCP" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.source="https://github.com/atvirokodosprendimai/chatlog" \
      org.opencontainers.image.vendor="Atviro kodo sprendimai" \
      org.opencontainers.image.version="${VERSION}"
COPY --chmod=0755 dist/chatlog-linux-${TARGETARCH} /usr/local/bin/chatlog
COPY LICENSE NOTICE /usr/share/doc/chatlog/
ENV CHATLOG_DIR=/data
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/chatlog"]
CMD ["mcp"]
