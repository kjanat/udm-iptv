# syntax=docker/dockerfile:1
ARG FIRMWARE_IMAGE=ghcr.io/kjanat/unifi-os:udmpro-5.1.33@sha256:5d6138dd461c4d265380eb550f5e609b072268e50205007cdd050d09f1d38bf8
FROM --platform=linux/arm64 ${FIRMWARE_IMAGE} AS vendor

FROM debian:bookworm-slim AS tools
RUN apt-get update && apt-get install -y --no-install-recommends \
    python3 iproute2 tcpdump procps util-linux binutils ca-certificates \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /lab
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1
COPY *.py /lab/
RUN python3 -m unittest -v test_fixture
ENTRYPOINT ["python3", "/lab/lab.py"]

FROM tools AS source
RUN apt-get update && apt-get install -y --no-install-recommends curl gcc make libc6-dev patch \
    && rm -rf /var/lib/apt/lists/*
ARG IMPROXY_COMMIT=5a9d153d10e19ef835af9fc5f2a971799677a7ed
RUN curl -fsSL --retry 3 "https://codeload.github.com/haibbo/improxy/tar.gz/${IMPROXY_COMMIT}" \
    -o /tmp/improxy.tar.gz \
    && mkdir /src && tar -xzf /tmp/improxy.tar.gz --strip-components=1 -C /src \
    && printf '%s\n' "${IMPROXY_COMMIT}" > /src/revision

FROM source AS source-build
RUN make -C /src CC=gcc

FROM tools AS upstream
COPY --from=source-build /src/improxy /opt/proxy/improxy
COPY --from=source-build /src/revision /opt/proxy/revision
ENV PROXY_IMPLEMENTATION=upstream

FROM source AS patched-build
COPY --from=improxy_patch / /patches/improxy/
RUN cd /src \
    && patch -p1 --fuzz=0 < /patches/improxy/0001-ipv4-querier-election.patch \
    && bash /patches/improxy/test.sh /src \
    && make -B CC=gcc LDFLAGS=-static \
    && { cat revision; sha256sum /patches/improxy/*.patch; } > /tmp/revision \
    && mv /tmp/revision revision \
    && { gcc --version; ld --version; \
         dpkg-query -W gcc libc6-dev binutils; \
         printf '%s\n' 'make -B CC=gcc LDFLAGS=-static'; \
         sha256sum /tmp/improxy.tar.gz improxy; \
         find /patches/improxy -type f -exec sha256sum {} +; \
       } > /src/build-metadata.txt

FROM tools AS patched
COPY --from=patched-build /src/improxy /opt/proxy/improxy
COPY --from=patched-build /src/revision /opt/proxy/revision
COPY --from=patched-build /src/build-metadata.txt /opt/proxy/build-metadata.txt
COPY --from=patched-build /tmp/improxy.tar.gz /opt/proxy/upstream-source.tar.gz
ENV PROXY_IMPLEMENTATION=patched

# No vendor program runs during the build. Keep the original loader and libraries.
FROM tools AS firmware
ARG FIRMWARE_IMAGE
LABEL org.opencontainers.image.base.name=${FIRMWARE_IMAGE}
COPY --from=vendor /usr/sbin/improxy /opt/proxy/improxy
COPY --from=vendor /lib/ /opt/proxy/lib/
COPY --from=vendor /usr/lib/ /opt/proxy/usr/lib/
COPY --from=vendor /var/lib/dpkg/status /opt/proxy/package-status
RUN printf '%s\n' "${FIRMWARE_IMAGE}" > /opt/proxy/revision
ENV PROXY_IMPLEMENTATION=firmware
