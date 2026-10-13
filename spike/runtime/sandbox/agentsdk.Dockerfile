# Candidate B: the Claude Agent SDK, which bundles the Claude Code CLI. A glibc
# base, because the SDK ships no musl wheel.
FROM python:3.12-slim
RUN useradd -m -d /home/agent agent
COPY agentsdk/requirements.txt /app/requirements.txt
RUN pip install --no-cache-dir -r /app/requirements.txt
COPY agentsdk/run_round.py /app/run_round.py
USER agent
WORKDIR /work
