# The fixture image for goreleaser.yaml: the baseline binary and nothing else.
FROM scratch
ARG TARGETOS TARGETARCH
COPY ${TARGETOS}/${TARGETARCH}/baseline /baseline
ENTRYPOINT ["/baseline"]
