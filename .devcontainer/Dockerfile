FROM rockylinux:10 AS base

ARG REPO_NAME=chat-harness-skel
ARG USERNAME=chat
ARG USER_UID=1000
ARG USER_GID=$USER_UID
ARG GO_VERSION=1.26.2

# Corporate CA trust (noop if cas.pem is empty)
COPY cas.pem /etc/pki/ca-trust/source/anchors/cas.pem
RUN update-ca-trust

RUN set -eux -o pipefail && \
    dnf install -y epel-release && \
    dnf -y update && \
    dnf install -y ca-certificates curl-minimal git iproute jq make \
                   ncurses nss-tools procps sudo wget which zsh zip && \
    dnf clean all

FROM base AS user

RUN groupadd --gid ${USER_GID} ${USERNAME} && \
    useradd --uid ${USER_UID} --gid ${USER_GID} -m ${USERNAME} -s /bin/zsh && \
    echo "${USERNAME} ALL=(root) NOPASSWD:ALL" > /etc/sudoers.d/${USERNAME} && \
    chmod 0440 /etc/sudoers.d/${USERNAME}

USER ${USERNAME}

RUN set -eux && \
    mkdir -p ${HOME}/bin && \
    sudo mkdir -p /workdir/${REPO_NAME} && \
    sudo chown ${USERNAME}:${USERNAME} /workdir

ENV HOME="/home/${USERNAME}"
ENV SDK_HOME="${HOME}/sdk"
ENV NODE_EXTRA_CA_CERTS="/etc/pki/ca-trust/source/anchors/cas.pem"
ENV GOPATH="${SDK_HOME}/go"
ENV PATH="${GOPATH}/bin:${SDK_HOME}/go/bin:${HOME}/.local/bin:${HOME}/bin:${PATH}"

RUN set -eux && \
    GO_ARCH="$(arch | sed 's/aarch64/arm64/' | sed 's/x86_64/amd64/')" && \
    mkdir -p ${SDK_HOME} && cd /tmp && \
    curl -fsSL -o go.tar.gz "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" && \
    tar xzf go.tar.gz -C ${SDK_HOME} && rm go.tar.gz && \
    cd ${SDK_HOME} && mv go go${GO_VERSION} && ln -s go${GO_VERSION} go

COPY --chown=${USERNAME}:${USERNAME} .bash_pretty ${HOME}/.bash_pretty
RUN echo "source ${HOME}/.bash_pretty" >> ${HOME}/.bashrc
