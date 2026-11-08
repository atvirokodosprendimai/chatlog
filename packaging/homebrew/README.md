# The Homebrew formula

This repository is its own Homebrew tap: `Formula/chatlog.rb` at its root, tapped by URL. On every tag `v*` that is
the latest, the release workflow regenerates `Formula/chatlog.rb` with
`gen-formula.sh VERSION SHA256SUMS` from the release's checksum file and commits it to `main` with the workflow's own
`GITHUB_TOKEN` ("formula for vX"; nothing to commit is a success).

Install, once a release exists:

    brew tap atvirokodosprendimai/chatlog https://github.com/atvirokodosprendimai/chatlog
    brew install chatlog
    chatlog install --bin "$(brew --prefix)/bin/chatlog" && chatlog doctor

Rebuild the formula of any release by hand:

    curl -LO https://github.com/atvirokodosprendimai/chatlog/releases/download/v0.1.0/SHA256SUMS
    sh packaging/homebrew/gen-formula.sh v0.1.0 SHA256SUMS > Formula/chatlog.rb

Platforms: macOS arm64 and Intel, Linux amd64 and arm64 (Homebrew on Linux). The binary is static (no CGO).
