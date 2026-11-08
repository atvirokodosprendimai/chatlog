# The Homebrew tap

The tap is the repository `github.com/atvirokodosprendimai/homebrew-chatlog` with one file, `Formula/chatlog.rb`.
It is generated, never edited: `gen-formula.sh VERSION SHA256SUMS` writes it from a release's checksum file (the
release workflow does this on every tag `v*` and pushes it to the tap with the `TAP_TOKEN` secret).

Install, once the tap and a release exist:

    brew install atvirokodosprendimai/chatlog/chatlog
    chatlog install && chatlog doctor

Rebuild the formula of any release by hand:

    curl -LO https://github.com/atvirokodosprendimai/chatlog/releases/download/v0.1.0/SHA256SUMS
    sh packaging/homebrew/gen-formula.sh v0.1.0 SHA256SUMS > Formula/chatlog.rb

Platforms: macOS arm64 and Intel, Linux amd64 and arm64 (Homebrew on Linux). The binary is static (no CGO).
