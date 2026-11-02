# A throwaway fleet node for end-to-end tests: plain sshd with fish as the
# login shell, exercising a non-POSIX login shell.
FROM docker.io/library/debian:trixie-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends openssh-server fish tar gzip ca-certificates ncurses-bin tmux \
 && rm -rf /var/lib/apt/lists/*
RUN useradd --create-home --shell /usr/bin/fish agent \
 && mkdir -p /run/sshd /home/agent/.ssh \
 && chmod 700 /home/agent/.ssh && chown agent:agent /home/agent/.ssh \
 && printf 'PasswordAuthentication no\nPermitRootLogin no\n' > /etc/ssh/sshd_config.d/fleet-test.conf
COPY fake-claude /usr/local/bin/claude
CMD ["/usr/sbin/sshd", "-D", "-e"]
